package descclean

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeClock advances by step on every reading, so a "slow" operation is one that
// reads the clock several times.
type fakeClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)
	return c.now
}

func newClock(step time.Duration) *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0), step: step}
}

const patDesc = "Content\n\nBuy the merch now"

func TestBudget_ExhaustionIsNotMemoised(t *testing.T) {
	c := NewCleaner()
	clk := newClock(time.Second)
	b := newBudgetWithClock(time.Millisecond, clk.Now)
	if _, err := c.Clean(context.Background(), Input{Description: patDesc, Patterns: []string{"merch"}}, b); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
	// A fresh batch retries and succeeds: the exhaustion was not cached as a failure.
	res, err := c.Clean(context.Background(), Input{Description: patDesc, Patterns: []string{"merch"}}, NewBudget(time.Minute))
	if err != nil || res.Cleaned != "Content" {
		t.Errorf("second batch = %q, %v; want the cleaned text", res.Cleaned, err)
	}
}

func TestBudget_OneBudgetCoversWholeBatch(t *testing.T) {
	c := NewCleaner()
	// Each clock reading costs 1ms, so a 50ms budget allows only a few items.
	clk := newClock(time.Millisecond)
	b := newBudgetWithClock(50*time.Millisecond, clk.Now)
	done, stopped := 0, 0
	for i := 0; i < 1000; i++ {
		_, err := c.Clean(context.Background(), Input{Description: fmt.Sprintf("Item %d\n\nmore", i)}, b)
		if errors.Is(err, ErrBudgetExhausted) {
			stopped++
			continue
		}
		done++
	}
	if done == 0 || stopped < 900 {
		t.Errorf("done = %d, stopped = %d; the shared budget must stop the batch early", done, stopped)
	}
}

func TestBudget_UserPatternTimeoutDisablesPatternsForRestOfBatch(t *testing.T) {
	c := NewCleaner()
	// Reading the clock is slow enough that one description exceeds the
	// per-call limit for user patterns, while the batch budget is ample.
	clk := newClock(100 * time.Millisecond)
	b := newBudgetWithClock(time.Hour, clk.Now)
	in := func(d string) Input {
		return Input{Description: d, SourceID: 7, Patterns: []string{"zzz", "merch"}}
	}

	first, err := c.Clean(context.Background(), in("A\n\nB merch\n\nC"), b)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !first.UserPatternsSkipped {
		t.Error("a timed-out call must report UserPatternsSkipped")
	}
	if first.Cleaned != "A\n\nB merch\n\nC" {
		t.Errorf("first Cleaned = %q; user patterns must not have applied", first.Cleaned)
	}

	// Built-in layers still run for later items in the batch, and user patterns
	// stay off for that source.
	second, err := c.Clean(context.Background(), in("Keep\n\nSponsored by Acme https://acme.example\n\nmerch"), b)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.UserPatternsSkipped || second.Cleaned != "Keep\n\nmerch" {
		t.Errorf("second = %q skipped=%v; want built-in layers only", second.Cleaned, second.UserPatternsSkipped)
	}

	// A different source in the same batch is unaffected by the flag.
	if b.userDisabled(8) {
		t.Error("another source must not be disabled")
	}
}

func TestBudget_TimedOutPairIsNegativelyCached(t *testing.T) {
	c := NewCleaner()
	slow := newBudgetWithClock(time.Hour, newClock(100*time.Millisecond).Now)
	in := Input{Description: "A\n\nB merch\n\nC", SourceID: 1, Patterns: []string{"zzz", "merch"}}
	if _, err := c.Clean(context.Background(), in, slow); err != nil {
		t.Fatal(err)
	}
	// A later batch, with a fast clock, hits the cached failure instead of
	// running the patterns again.
	_, err := c.Clean(context.Background(), in, NewBudget(time.Hour))
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("second batch err = %v, want the cached ErrTimeout", err)
	}
	// A different pattern list is a different key and runs normally.
	other := in
	other.Patterns = []string{"merch"}
	res, err := c.Clean(context.Background(), other, NewBudget(time.Hour))
	if err != nil || res.Cleaned != "A\n\nC" {
		t.Errorf("other settings = %q, %v; want a normal result", res.Cleaned, err)
	}
}

func TestBudget_FirstWarningOncePerSourceAndClass(t *testing.T) {
	b := NewBudget(time.Minute)
	if !b.FirstWarning(1, ClassTimeout) || b.FirstWarning(1, ClassTimeout) {
		t.Error("the same (source, class) must warn once")
	}
	if !b.FirstWarning(2, ClassTimeout) || !b.FirstWarning(1, ClassTooLarge) {
		t.Error("a different source or class must warn separately")
	}
}

func TestMemo_IsReusedAndKeyedBySettings(t *testing.T) {
	c := NewCleaner()
	ctx := context.Background()
	desc := "Content\n\nBuy the merch now"
	base := Input{Description: desc, Mode: "on", Patterns: []string{"merch"}}

	if _, err := c.Clean(ctx, base, nil); err != nil {
		t.Fatal(err)
	}
	if c.memo.len() != 1 {
		t.Fatalf("memo has %d entries after one clean, want 1", c.memo.len())
	}
	c.Clean(ctx, base, nil)
	if c.memo.len() != 1 {
		t.Error("the same input must reuse its memo entry")
	}

	variants := map[string]Input{
		"mode":          {Description: desc, Mode: "off", Patterns: []string{"merch"}},
		"other pattern": {Description: desc, Mode: "on", Patterns: []string{"buy"}},
		"no pattern":    {Description: desc, Mode: "on"},
		"stats":         {Description: desc, Mode: "on", Patterns: []string{"merch"}, Stats: BuildStats(nil)},
	}
	seen := 1
	for name, v := range variants {
		if _, err := c.Clean(ctx, v, nil); err != nil {
			t.Fatal(err)
		}
		seen++
		if c.memo.len() != seen {
			t.Errorf("%s: memo has %d entries, want %d (settings must not share an entry)", name, c.memo.len(), seen)
		}
	}
}

func TestMemo_PatternOrderIsPartOfTheKey(t *testing.T) {
	c := NewCleaner()
	a := Input{Description: patDesc, Patterns: []string{"merch", "buy"}}
	b := Input{Description: patDesc, Patterns: []string{"buy", "merch"}}
	ra, _ := c.Clean(context.Background(), a, nil)
	rb, _ := c.Clean(context.Background(), b, nil)
	if c.memo.len() != 2 {
		t.Errorf("memo has %d entries, want 2", c.memo.len())
	}
	if ra.Removed[0].Detail != "merch" || rb.Removed[0].Detail != "buy" {
		t.Errorf("details = %q, %q; the first listed matching pattern is the reason", ra.Removed[0].Detail, rb.Removed[0].Detail)
	}
}

func TestMemo_AlgorithmVersionInvalidates(t *testing.T) {
	c := NewCleaner()
	in := Input{Description: patDesc}
	c.Clean(context.Background(), in, nil)
	old := algorithmVersion
	algorithmVersion++
	defer func() { algorithmVersion = old }()
	c.Clean(context.Background(), in, nil)
	if c.memo.len() != 2 {
		t.Errorf("memo has %d entries, want 2: a new algorithm version must not reuse old entries", c.memo.len())
	}
}

func TestMemo_IsBounded(t *testing.T) {
	c := NewCleaner()
	for i := 0; i < memoSize+25; i++ {
		if _, err := c.Clean(context.Background(), Input{Description: fmt.Sprintf("item %d", i)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.memo.len(); n != memoSize {
		t.Errorf("memo holds %d entries, want exactly %d", n, memoSize)
	}
}

func TestMemo_ResultEqualsFreshResult(t *testing.T) {
	c := NewCleaner()
	in := Input{Description: "A\n\nSponsored by Acme https://a.example\n\nB", Patterns: []string{"b$"}}
	fresh, _ := c.Clean(context.Background(), in, nil)
	cached, _ := c.Clean(context.Background(), in, nil)
	if fmt.Sprint(fresh) != fmt.Sprint(cached) {
		t.Errorf("cached %+v differs from fresh %+v", cached, fresh)
	}
}
