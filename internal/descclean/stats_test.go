package descclean

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

const boiler = "Follow me on Twitter @example"

// descsWith returns n descriptions, the first `with` of which hold the boilerplate.
func descsWith(n, with int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Unique content number %d", i)
		if i < with {
			out[i] += "\n\n" + boiler
		}
	}
	return out
}

func TestChannelStats_RepetitionShare(t *testing.T) {
	tests := []struct {
		name  string
		n     int
		with  int
		want  bool
		boilr string
	}{
		{"4 descriptions never remove", 4, 4, false, boiler},
		{"5 with 3 of 5 removes", 5, 3, true, boiler},
		{"5 with 2 of 5 keeps", 5, 2, false, boiler},
		{"exactly 50 percent removes", 6, 3, true, boiler},
		{"just under 50 percent keeps", 7, 3, false, boiler},
		{"case and spacing ignored", 5, 3, true, "  FOLLOW ME   on twitter @EXAMPLE "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := BuildStats(descsWith(tc.n, tc.with))
			_, got := s.repeated(tc.boilr)
			if got != tc.want {
				t.Errorf("repeated = %v with %d of %d, want %v", got, tc.with, tc.n, tc.want)
			}
		})
	}
}

func TestChannelStats_RepeatInsideOneDescriptionCountsOnce(t *testing.T) {
	descs := descsWith(5, 0)
	descs[0] += "\n\n" + boiler + "\n\n" + boiler + "\n\n" + boiler
	s := BuildStats(descs)
	count, ok := s.repeated(boiler)
	if count != 1 || ok {
		t.Errorf("count = %d, ok = %v; want 1, false (one description holds it three times)", count, ok)
	}
}

func TestChannelStats_EmptyDescriptionsIgnored(t *testing.T) {
	descs := append(descsWith(4, 4), "", "  \n ", "")
	s := BuildStats(descs)
	if s.N != 4 {
		t.Errorf("N = %d, want 4 (empty descriptions are not counted)", s.N)
	}
	if _, ok := s.repeated(boiler); ok {
		t.Error("4 non-empty descriptions must never trigger repetition")
	}
}

func TestChannelStats_NilIsSafe(t *testing.T) {
	var s *ChannelStats
	if _, ok := s.repeated("x"); ok {
		t.Error("nil stats must never report repetition")
	}
}

type fakeHistory struct {
	mu       sync.Mutex
	byUpload map[string][]string
	counts   int
	loads    int
	err      error
}

func (f *fakeHistory) DescriptionsByUploader(_ context.Context, u string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loads++
	return f.byUpload[u], f.err
}

func (f *fakeHistory) CountDescriptionsByUploader(_ context.Context, u string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts++
	return len(f.byUpload[u]), f.err
}

func TestCleanerStats_CachesUntilCountChanges(t *testing.T) {
	h := &fakeHistory{byUpload: map[string][]string{"chan": descsWith(5, 3)}}
	c := NewCleaner()
	ctx := context.Background()

	first, err := c.Stats(ctx, " chan ", h)
	if err != nil || first.N != 5 {
		t.Fatalf("Stats = %+v, %v; want N=5", first, err)
	}
	second, _ := c.Stats(ctx, "chan", h)
	if first != second || h.loads != 1 {
		t.Errorf("second call must reuse the cache: loads = %d", h.loads)
	}
	h.byUpload["chan"] = descsWith(6, 3)
	third, _ := c.Stats(ctx, "chan", h)
	if third.N != 6 || h.loads != 2 {
		t.Errorf("a changed count must rebuild: N = %d, loads = %d", third.N, h.loads)
	}
	c.Invalidate("chan")
	c.Stats(ctx, "chan", h)
	if h.loads != 3 {
		t.Errorf("Invalidate must force a rebuild: loads = %d, want 3", h.loads)
	}
}

func TestCleanerStats_EmptyUploaderAndNilHistory(t *testing.T) {
	c := NewCleaner()
	h := &fakeHistory{}
	if s, err := c.Stats(context.Background(), "  ", h); s != nil || err != nil || h.counts != 0 {
		t.Errorf("empty uploader: %v, %v, queries = %d; want nil, nil, 0", s, err, h.counts)
	}
	if s, err := c.Stats(context.Background(), "x", nil); s != nil || err != nil {
		t.Errorf("nil history: %v, %v; want nil, nil", s, err)
	}
}

func TestCleanerStats_HistoryErrorIsReturned(t *testing.T) {
	h := &fakeHistory{err: errors.New("db down")}
	if _, err := NewCleaner().Stats(context.Background(), "x", h); err == nil {
		t.Error("a failing history must return its error")
	}
}

func TestCleanerStats_CacheIsBoundedTo256Uploaders(t *testing.T) {
	h := &fakeHistory{byUpload: map[string][]string{}}
	c := NewCleaner()
	for i := 0; i < statsCacheSize+50; i++ {
		u := fmt.Sprintf("u%d", i)
		h.byUpload[u] = descsWith(5, 0)
		if _, err := c.Stats(context.Background(), u, h); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.stats.len(); n != statsCacheSize {
		t.Errorf("stats cache holds %d uploaders, want %d", n, statsCacheSize)
	}
	// The oldest uploader was evicted, so it is loaded again.
	loads := h.loads
	c.Stats(context.Background(), "u0", h)
	if h.loads != loads+1 {
		t.Error("an evicted uploader must be rebuilt on next use")
	}
}

func TestBuildStats_VersionsDiffer(t *testing.T) {
	a, b := BuildStats(nil), BuildStats(nil)
	if a.Version == b.Version {
		t.Error("each build must get its own version")
	}
	_ = strings.TrimSpace
}
