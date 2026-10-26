package descclean

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
)

// Limits on user strip patterns. They are fixed on purpose and not settings.
//
// RE2 guarantees time linear in (text length x program size), but a program can
// itself be huge: a 500-byte pattern such as stacked \pL{1000} unrolls to
// hundreds of thousands of instructions, and one MatchString call cannot be
// interrupted. So the program size is bounded up front, before anything large
// is built, and a deadline is only a secondary guard.
const (
	maxPatterns      = 20   // patterns per source
	maxPatternBytes  = 500  // bytes per pattern
	maxRepeat        = 100  // largest counted repeat, {n} or {n,m}
	maxRepeatProduct = 1000 // product of nested counted-repeat bounds

	// maxPatternCost and maxTotalCost cap the measured program cost of one
	// pattern and of all patterns of a source together. The cost is the
	// instruction count plus one unit per rangesPerCostUnit rune ranges of every
	// character class, so wide Unicode classes (\pL has hundreds of ranges) are
	// priced by their real size. These two numbers may be tuned against real
	// patterns; the caps above may not be removed.
	maxPatternCost = 1000
	maxTotalCost   = 2000

	rangesPerCostUnit = 16
)

// Typed errors from ParsePatterns, matched with errors.Is so the form can pick a
// friendly message.
var (
	ErrSyntax             = errors.New("invalid pattern")
	ErrTooMany            = errors.New("too many patterns")
	ErrTooLong            = errors.New("pattern too long")
	ErrTooComplex         = errors.New("pattern too complex")
	ErrTooComplexTogether = errors.New("patterns too complex together")
)

// PatternError describes why a pattern list was rejected. It carries only a
// short excerpt of the offending pattern, never the whole text.
type PatternError struct {
	Kind    error  // one of the Err* values above
	Index   int    // zero-based position in the cleaned list, -1 when not about one pattern
	Excerpt string // SafeExcerpt of the pattern, may be empty
	Detail  string // short reason, never contains the full pattern
}

func (e *PatternError) Error() string {
	var b strings.Builder
	b.WriteString(e.Kind.Error())
	if e.Excerpt != "" {
		fmt.Fprintf(&b, " %q", e.Excerpt)
	}
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	return b.String()
}

func (e *PatternError) Unwrap() error { return e.Kind }

// Pattern is a validated, compiled user strip pattern.
type Pattern struct {
	// Source is the pattern as the user wrote it (trimmed), without the
	// case-insensitivity prefix added for matching.
	Source string
	// Cost is the measured program cost.
	Cost int
	re   *regexp.Regexp
}

// Matches reports whether the pattern matches anywhere in text.
func (p Pattern) Matches(text string) bool { return p.re.MatchString(text) }

// Sources returns the pattern texts of a parsed list, in order.
func Sources(ps []Pattern) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Source
	}
	return out
}

// ParsePatterns validates and compiles a list of user strip patterns. It is the
// only way to get a usable Pattern, used at save time and again for values read
// back from the database. Blank entries are dropped and duplicates removed,
// keeping order. Matching is case-insensitive; a pattern can opt out with (?-i).
//
// Order of work, cheapest and safest first: byte and count limits, syntax parse,
// repeat caps on the parse tree, then Simplify + Compile to measure cost, and
// only then regexp.Compile. Simplify and Compile unroll counted repeats, which is
// where memory and time go, so they never see a tree that failed the repeat caps.
func ParsePatterns(raw []string) ([]Pattern, error) {
	cleaned := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, r := range raw {
		p := strings.TrimSpace(r)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		cleaned = append(cleaned, p)
	}
	if len(cleaned) > maxPatterns {
		return nil, &PatternError{Kind: ErrTooMany, Index: -1,
			Detail: fmt.Sprintf("at most %d patterns", maxPatterns)}
	}

	out := make([]Pattern, 0, len(cleaned))
	total := 0
	for i, p := range cleaned {
		parsed, err := parseOne(i, p)
		if err != nil {
			return nil, err
		}
		total += parsed.Cost
		if total > maxTotalCost {
			return nil, &PatternError{Kind: ErrTooComplexTogether, Index: -1,
				Detail: "remove or simplify some patterns"}
		}
		out = append(out, parsed)
	}
	return out, nil
}

func parseOne(index int, p string) (Pattern, error) {
	fail := func(kind error, detail string) (Pattern, error) {
		return Pattern{}, &PatternError{Kind: kind, Index: index, Excerpt: SafeExcerpt(p, excerptInLogs), Detail: detail}
	}
	if len(p) > maxPatternBytes {
		return fail(ErrTooLong, fmt.Sprintf("at most %d bytes", maxPatternBytes))
	}
	if strings.ContainsRune(p, 0) {
		return fail(ErrSyntax, "contains a NUL character")
	}

	tree, err := syntax.Parse("(?i)"+p, syntax.Perl)
	if err != nil {
		var se *syntax.Error
		if errors.As(err, &se) {
			switch se.Code {
			case syntax.ErrInvalidRepeatSize, syntax.ErrLarge, syntax.ErrNestingDepth:
				return fail(ErrTooComplex, string(se.Code))
			}
			// Only the code is reported: the error's own text embeds the pattern.
			return fail(ErrSyntax, string(se.Code))
		}
		return fail(ErrSyntax, "could not be parsed")
	}
	if !repeatsWithinCaps(tree, 1) {
		return fail(ErrTooComplex, "repeats something too many times")
	}

	cost := programCost(tree)
	if cost > maxPatternCost {
		return fail(ErrTooComplex, "compiled program is too large")
	}
	re, err := regexp.Compile("(?i)" + p)
	if err != nil {
		return fail(ErrSyntax, "could not be compiled")
	}
	return Pattern{Source: p, Cost: cost, re: re}, nil
}

// repeatsWithinCaps walks the parse tree and rejects any counted repeat above
// maxRepeat, or nesting whose product of repeat bounds exceeds maxRepeatProduct.
// It runs on the raw tree, before Simplify expands the repeats.
func repeatsWithinCaps(re *syntax.Regexp, product int) bool {
	if re.Op == syntax.OpRepeat {
		bound := re.Max
		if bound < re.Min {
			bound = re.Min // {n,} is unbounded above; its minimum is what unrolls
		}
		if bound > maxRepeat {
			return false
		}
		if bound > 1 {
			product *= bound
		}
		if product > maxRepeatProduct {
			return false
		}
	}
	for _, sub := range re.Sub {
		if !repeatsWithinCaps(sub, product) {
			return false
		}
	}
	return true
}

// programCost measures the program that will actually run: the Simplify output
// (repeats unrolled) compiled, counting instructions plus a charge for the rune
// ranges of every class. Case folding from the (?i) prefix is already expanded
// into the ranges by the parser, so it is included.
func programCost(tree *syntax.Regexp) int {
	prog, err := syntax.Compile(tree.Simplify())
	if err != nil {
		return maxPatternCost + 1
	}
	cost := len(prog.Inst)
	for i := range prog.Inst {
		inst := &prog.Inst[i]
		if inst.Op != syntax.InstRune {
			continue
		}
		if len(inst.Rune) <= 2 {
			continue // one character or one range: already paid for as an instruction
		}
		ranges := len(inst.Rune) / 2
		cost += (ranges + rangesPerCostUnit - 1) / rangesPerCostUnit
	}
	return cost
}
