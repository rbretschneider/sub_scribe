// Package descclean removes boilerplate paragraphs (repeated channel text,
// sponsor reads, user-chosen patterns) from a video description at output time.
//
// It is a pure transform: the stored description is never changed, the same
// input always gives the same output, and it depends only on the standard
// library. Cleaning works one whole paragraph at a time, where a paragraph is
// text separated by one or more blank lines.
package descclean

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (

	// MaxDescriptionBytes is the largest description that is cleaned at all.
	// Anything bigger is returned as it is.
	MaxDescriptionBytes = 50_000

	// maxParagraphMatchBytes and maxDescriptionMatchBytes bound how much text any
	// user pattern is run against, per paragraph and per description. With the
	// program cost caps they bound the worst-case matching work.
	maxParagraphMatchBytes   = 5_000
	maxDescriptionMatchBytes = 20_000

	// memoSize bounds the cleaned-result cache; patternCacheSize bounds the
	// compiled-pattern cache.
	memoSize         = 10_000
	patternCacheSize = 256

	// userPatternCallLimit is how long the user patterns may take on a single
	// description before they count as timed out.
	userPatternCallLimit = 250 * time.Millisecond

	// DefaultBatchBudget is the cleaning time allowed for one batch (a feed
	// regeneration or a refresh run).
	DefaultBatchBudget = 2 * time.Second

	// excerptInLogs and excerptInUI cap how much of a pattern is ever shown.
	excerptInLogs = 40
	excerptInUI   = 80
)

// algorithmVersion is part of every cache key. Bump it whenever the built-in
// phrase lists or the rules change, so older cached results are not reused. It is
// a variable only so a test can prove that bumping it invalidates the caches.
var algorithmVersion = 1

// Errors returned by Clean. In every case the caller should fall back to the
// original description.
var (
	// ErrTooLarge means the description exceeds MaxDescriptionBytes.
	ErrTooLarge = errors.New("description too large to clean")
	// ErrTimeout means the user patterns were too slow for this description.
	ErrTimeout = errors.New("user patterns timed out")
	// ErrBudgetExhausted means the batch's time budget ran out. It is a property
	// of the batch, not of the description, so it is never cached.
	ErrBudgetExhausted = errors.New("cleaning time budget exhausted")
)

// Error classes used in log lines, so logs carry a fixed word and never text.
const (
	ClassTimeout         = "timeout"
	ClassTooLarge        = "too_large"
	ClassPatternRejected = "pattern_rejected"
	ClassHistory         = "history_unavailable"
	ClassPanic           = "panic"
	ClassCanceled        = "canceled"
)

// ErrorClass maps an error from this package to its fixed log class.
func ErrorClass(err error) string {
	switch {
	case errors.Is(err, ErrTooLarge):
		return ClassTooLarge
	case errors.Is(err, ErrTimeout), errors.Is(err, ErrBudgetExhausted):
		return ClassTimeout
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ClassCanceled
	default:
		return ClassPanic
	}
}

// Reason says which layer removed a paragraph.
type Reason string

const (
	ReasonRepeated    Reason = "repeated"
	ReasonSponsor     Reason = "sponsor"
	ReasonUserPattern Reason = "user_pattern"
)

// Removal is one paragraph that cleanup removed, with why.
type Removal struct {
	Text   string
	Reason Reason
	// Detail is a short human-readable specific: counts, the matched sponsor
	// phrase, or a pattern excerpt.
	Detail string
}

// Label is the text shown next to a removed paragraph.
func (r Removal) Label() string {
	var prefix string
	switch r.Reason {
	case ReasonRepeated:
		prefix = "Repeated in channel"
	case ReasonSponsor:
		prefix = "Sponsor rule"
	case ReasonUserPattern:
		prefix = "Your pattern"
	default:
		prefix = string(r.Reason)
	}
	if r.Detail == "" {
		return prefix
	}
	return prefix + ": " + r.Detail
}

// Result is the outcome of cleaning one description. Treat it as read-only: the
// cleaner shares Removed between cached copies.
type Result struct {
	// Cleaned is the text to write out. It is the original string, byte for byte,
	// when nothing was removed or when everything would have been.
	Cleaned string
	// Removed lists the removed paragraphs in order. When FellBack is set they
	// were not actually removed.
	Removed []Removal
	// FellBack is set when every paragraph matched, so the original is kept.
	FellBack bool
	// PatternsRejected is set when the stored patterns failed validation on read
	// and were ignored.
	PatternsRejected bool
	// UserPatternsSkipped is set when user patterns were not run because they
	// timed out, for this description or earlier in the batch.
	UserPatternsSkipped bool
	// PatternInputCapped is set when the per-description cap stopped some
	// paragraphs from being tested against the user patterns.
	PatternInputCapped bool
	// HistorySize is the number of channel descriptions repetition was judged
	// against, 0 when the layer was unavailable.
	HistorySize int
}

// Input is everything Clean needs.
type Input struct {
	Description string
	// SourceID scopes the per-batch "user patterns disabled" flag.
	SourceID int64
	// Mode is only mixed into the cache key; cleaning itself does not depend on it.
	Mode string
	// Patterns are the stored user patterns, re-validated before first use.
	Patterns []string
	// Stats is the channel statistics, nil when repetition is unavailable.
	Stats *ChannelStats
}

// Cleaner applies the cleanup layers and keeps the bounded caches. It is safe for
// concurrent use.
type Cleaner struct {
	mu       sync.Mutex
	stats    *lruCache[string, cachedStats]
	memo     *lruCache[[sha256.Size]byte, memoEntry]
	patterns *lruCache[[sha256.Size]byte, compiledList]
}

// memoEntry is a cached result, or a cached failure (the negative cache).
type memoEntry struct {
	res Result
	err error
}

type compiledList struct {
	patterns []Pattern
	err      error
}

// NewCleaner returns an empty Cleaner.
func NewCleaner() *Cleaner {
	return &Cleaner{
		stats:    newLRU[string, cachedStats](statsCacheSize),
		memo:     newLRU[[sha256.Size]byte, memoEntry](memoSize),
		patterns: newLRU[[sha256.Size]byte, compiledList](patternCacheSize),
	}
}

// Budget is the shared cleaning time allowance of one batch, plus the per-source
// flag that switches user patterns off for the rest of the batch after a
// timeout. One Budget is passed to every Clean call of a batch.
type Budget struct {
	mu       sync.Mutex
	deadline time.Time
	now      func() time.Time
	noUser   map[int64]bool
	warned   map[string]bool
}

// NewBudget starts a budget of the given length.
func NewBudget(total time.Duration) *Budget {
	return newBudgetWithClock(total, time.Now)
}

func newBudgetWithClock(total time.Duration, now func() time.Time) *Budget {
	return &Budget{deadline: now().Add(total), now: now,
		noUser: make(map[int64]bool), warned: make(map[string]bool)}
}

// Exhausted reports whether the batch deadline has passed.
func (b *Budget) Exhausted() bool { return !b.now().Before(b.deadline) }

func (b *Budget) userDisabled(sourceID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.noUser[sourceID]
}

func (b *Budget) disableUser(sourceID int64) {
	b.mu.Lock()
	b.noUser[sourceID] = true
	b.mu.Unlock()
}

// FirstWarning reports true the first time it is called for a (source, class)
// pair in this batch, so callers log at most one warning of each kind per batch.
func (b *Budget) FirstWarning(sourceID int64, class string) bool {
	k := fmt.Sprintf("%d/%s", sourceID, class)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.warned[k] {
		return false
	}
	b.warned[k] = true
	return true
}

// fingerprintOf hashes the settings that shape a result: the cleanup mode, the
// ordered pattern list (order decides which reason is shown) and the algorithm
// version.
func fingerprintOf(mode string, patterns []string) [sha256.Size]byte {
	h := sha256.New()
	writeField(h, fmt.Sprint(algorithmVersion))
	writeField(h, mode)
	for _, p := range patterns {
		writeField(h, p)
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

type fieldWriter interface{ Write([]byte) (int, error) }

// writeField writes a length-prefixed field so adjacent fields cannot blur into
// each other.
func writeField(w fieldWriter, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = w.Write(n[:])
	_, _ = w.Write([]byte(s))
}

// resolvePatterns validates the stored list once per distinct list. A failure is
// cached too, so a bad list is not re-parsed on every description.
func (c *Cleaner) resolvePatterns(raw []string) ([]Pattern, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	key := fingerprintOf("", raw)
	c.mu.Lock()
	if e, ok := c.patterns.get(key); ok {
		c.mu.Unlock()
		return e.patterns, e.err
	}
	c.mu.Unlock()
	parsed, err := ParsePatterns(raw)
	c.mu.Lock()
	c.patterns.put(key, compiledList{patterns: parsed, err: err})
	c.mu.Unlock()
	return parsed, err
}

// Clean removes boilerplate paragraphs from in.Description. A nil budget gets a
// fresh DefaultBatchBudget. On a non-nil error the caller must use the original
// description. See the package comment for the layers and their order.
func (c *Cleaner) Clean(ctx context.Context, in Input, b *Budget) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if b == nil {
		b = NewBudget(DefaultBatchBudget)
	}
	if len(in.Description) > MaxDescriptionBytes {
		return Result{}, ErrTooLarge
	}
	if strings.TrimSpace(in.Description) == "" {
		return Result{Cleaned: in.Description}, nil
	}
	if b.Exhausted() {
		return Result{}, ErrBudgetExhausted
	}

	pats, patErr := c.resolvePatterns(in.Patterns)
	key := c.memoKey(in)
	c.mu.Lock()
	cached, hit := c.memo.get(key)
	c.mu.Unlock()
	if hit {
		return cached.res, cached.err
	}

	skipUser := len(pats) > 0 && b.userDisabled(in.SourceID)
	res, err := c.run(ctx, in, pats, skipUser, b)
	if errors.Is(err, errUserTimeout) {
		// Remember the slow (description, settings) pair so it is not retried on
		// every write, and run the built-in layers alone for this call.
		b.disableUser(in.SourceID)
		c.memoize(key, memoEntry{err: ErrTimeout})
		res, err = c.run(ctx, in, nil, true, b)
		res.UserPatternsSkipped = true
	}
	if err != nil {
		return Result{}, err
	}
	res.PatternsRejected = patErr != nil
	if skipUser {
		res.UserPatternsSkipped = true
	}
	// A result computed without the user patterns must not be stored under the
	// key that stands for "with them".
	if !res.UserPatternsSkipped {
		c.memoize(key, memoEntry{res: res})
	}
	return res, nil
}

func (c *Cleaner) memoize(key [sha256.Size]byte, e memoEntry) {
	c.mu.Lock()
	c.memo.put(key, e)
	c.mu.Unlock()
}

func (c *Cleaner) memoKey(in Input) [sha256.Size]byte {
	h := sha256.New()
	desc := sha256.Sum256([]byte(in.Description))
	_, _ = h.Write(desc[:])
	var version uint64
	if in.Stats != nil {
		version = in.Stats.Version
	}
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], version)
	_, _ = h.Write(v[:])
	fp := fingerprintOf(in.Mode, in.Patterns)
	_, _ = h.Write(fp[:])
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// errUserTimeout is internal: run reports it so Clean can retry without the
// user patterns.
var errUserTimeout = errors.New("user pattern timeout")

// run evaluates the layers over every paragraph. With skipUser the user patterns
// are not consulted at all.
func (c *Cleaner) run(ctx context.Context, in Input, pats []Pattern, skipUser bool, b *Budget) (Result, error) {
	paragraphs := splitParagraphs(in.Description)
	res := Result{}
	if in.Stats != nil {
		res.HistorySize = in.Stats.N
	}
	userStart := b.now()
	matchedBytes := 0

	kept := make([]string, 0, len(paragraphs))
	for _, p := range paragraphs {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if b.Exhausted() {
			return Result{}, ErrBudgetExhausted
		}
		guarded := isChapterList(p)

		var removal *Removal
		if !skipUser && len(pats) > 0 {
			text := truncateUTF8(collapseSpace(p), maxParagraphMatchBytes)
			if res.PatternInputCapped || matchedBytes+len(text) > maxDescriptionMatchBytes {
				// Once the cap is hit, every later paragraph is skipped too, not
				// just ones that are too big, so the rule is simple to state.
				res.PatternInputCapped = true
			} else {
				matchedBytes += len(text)
				for _, pat := range pats {
					if pat.Matches(text) {
						removal = &Removal{Text: p, Reason: ReasonUserPattern,
							Detail: SafeExcerpt(pat.Source, excerptInUI)}
						break
					}
					if b.now().Sub(userStart) > userPatternCallLimit {
						return Result{}, errUserTimeout
					}
					if b.Exhausted() {
						return Result{}, ErrBudgetExhausted
					}
				}
				if b.now().Sub(userStart) > userPatternCallLimit {
					return Result{}, errUserTimeout
				}
			}
		}
		if removal == nil && !guarded {
			if count, ok := in.Stats.repeated(p); ok {
				removal = &Removal{Text: p, Reason: ReasonRepeated,
					Detail: fmt.Sprintf("in %d of %d videos from this channel", count, in.Stats.N)}
			} else if why, ok := sponsorMatch(p); ok {
				removal = &Removal{Text: p, Reason: ReasonSponsor, Detail: why}
			}
		}
		if removal != nil {
			res.Removed = append(res.Removed, *removal)
			continue
		}
		kept = append(kept, p)
	}

	switch {
	case len(res.Removed) == 0:
		res.Cleaned = in.Description
	case len(kept) == 0:
		// Removing everything would leave an empty plot; keep the original and let
		// the preview still show what matched.
		res.Cleaned = in.Description
		res.FellBack = true
	default:
		res.Cleaned = strings.Join(kept, "\n\n")
	}
	return res, nil
}

// SafeExcerpt shortens s to at most max runes (including a trailing ellipsis when
// cut) and replaces control characters with spaces. It is the only way a pattern
// or text fragment reaches a log line or a label.
func SafeExcerpt(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	cut := false
	if len(runes) > max {
		runes = runes[:max-1]
		cut = true
	}
	for i, r := range runes {
		if r < 0x20 || r == 0x7f {
			runes[i] = ' '
		}
	}
	out := string(runes)
	if cut {
		out += "…"
	}
	return out
}

// ExcerptForLog is SafeExcerpt at the log-line cap.
func ExcerptForLog(s string) string { return SafeExcerpt(s, excerptInLogs) }
