package descclean

import (
	"context"
	"strings"
	"sync/atomic"
)

const (
	// MinHistory is the fewest stored descriptions a channel needs before
	// repetition can remove anything. With fewer, "appears in half of them" says
	// too little.
	MinHistory = 5

	// statsCacheSize bounds how many uploaders' statistics are kept, so a library
	// with thousands of channels cannot grow memory without limit.
	statsCacheSize = 256
)

// statsSerial hands out a distinct Version to every built ChannelStats, so a
// cleaned result memoised against old statistics is never reused for new ones.
var statsSerial atomic.Uint64

// ChannelStats counts, for one uploader, how many stored descriptions contain
// each paragraph.
type ChannelStats struct {
	// N is the number of non-empty descriptions counted.
	N int
	// Version identifies this build of the statistics.
	Version uint64

	counts map[paragraphKey]int
}

// BuildStats counts paragraphs over a channel's stored descriptions. Empty
// descriptions are ignored, and a paragraph repeated inside one description
// counts once for that description.
func BuildStats(descriptions []string) *ChannelStats {
	s := &ChannelStats{counts: make(map[paragraphKey]int), Version: statsSerial.Add(1)}
	for _, d := range descriptions {
		if strings.TrimSpace(d) == "" {
			continue
		}
		s.N++
		seen := make(map[paragraphKey]bool)
		for _, p := range splitParagraphs(d) {
			k := keyOf(p)
			if !seen[k] {
				seen[k] = true
				s.counts[k]++
			}
		}
	}
	return s
}

// repeated reports how many descriptions hold the paragraph and whether that is
// enough to call it boilerplate: the channel has at least MinHistory
// descriptions and the paragraph is in at least half of them. The half test is
// integer arithmetic (2*count >= N) to avoid float rounding at exactly 50%.
func (s *ChannelStats) repeated(p string) (count int, ok bool) {
	if s == nil || s.N < MinHistory {
		return 0, false
	}
	count = s.counts[keyOf(p)]
	return count, 2*count >= s.N
}

// History is the read-only view of stored descriptions the cleaner needs. Both
// methods count only non-empty descriptions and ignore which source owns them.
type History interface {
	DescriptionsByUploader(ctx context.Context, uploader string) ([]string, error)
	CountDescriptionsByUploader(ctx context.Context, uploader string) (int, error)
}

type cachedStats struct {
	stats *ChannelStats
	// stored is the row count the statistics were built from, as the history
	// reported it. A different count means the history changed.
	stored int
}

// Stats returns the statistics for an uploader, or nil when the uploader is
// unknown (empty) or there is no history. One cheap indexed count decides whether
// the cached entry is still valid; only a change rebuilds it.
func (c *Cleaner) Stats(ctx context.Context, uploader string, hist History) (*ChannelStats, error) {
	uploader = strings.TrimSpace(uploader)
	if uploader == "" || hist == nil {
		return nil, nil
	}
	n, err := hist.CountDescriptionsByUploader(ctx, uploader)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if e, ok := c.stats.get(uploader); ok && e.stored == n {
		c.mu.Unlock()
		return e.stats, nil
	}
	c.mu.Unlock()

	descs, err := hist.DescriptionsByUploader(ctx, uploader)
	if err != nil {
		return nil, err
	}
	built := BuildStats(descs)
	c.mu.Lock()
	c.stats.put(uploader, cachedStats{stats: built, stored: n})
	c.mu.Unlock()
	return built, nil
}

// Invalidate drops an uploader's cached statistics. Code that changes stored
// descriptions calls it so the next use rebuilds.
func (c *Cleaner) Invalidate(uploader string) {
	c.mu.Lock()
	c.stats.remove(strings.TrimSpace(uploader))
	c.mu.Unlock()
}
