package descclean

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func clean(t *testing.T, in Input) Result {
	t.Helper()
	res, err := NewCleaner().Clean(context.Background(), in, nil)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	return res
}

func TestClean_NothingRemovedReturnsOriginalByteForByte(t *testing.T) {
	in := "First  paragraph\r\n\r\n\r\nSecond paragraph  \n"
	res := clean(t, Input{Description: in})
	if res.Cleaned != in || len(res.Removed) != 0 {
		t.Errorf("Cleaned = %q, removed = %v; want the original untouched", res.Cleaned, res.Removed)
	}
}

func TestClean_EmptyDescription(t *testing.T) {
	for _, d := range []string{"", " \n\n "} {
		res := clean(t, Input{Description: d})
		if res.Cleaned != d {
			t.Errorf("Clean(%q) = %q, want the same", d, res.Cleaned)
		}
	}
}

func TestClean_RepetitionRemovesParagraphAndJoinsWithBlankLine(t *testing.T) {
	stats := BuildStats(descsWith(5, 3))
	desc := "Real content\n\n" + boiler + "\n\nMore content"
	res := clean(t, Input{Description: desc, Stats: stats})
	if res.Cleaned != "Real content\n\nMore content" {
		t.Errorf("Cleaned = %q", res.Cleaned)
	}
	if len(res.Removed) != 1 || res.Removed[0].Reason != ReasonRepeated {
		t.Fatalf("Removed = %+v, want one repeated removal", res.Removed)
	}
	if got := res.Removed[0].Label(); got != "Repeated in channel: in 3 of 5 videos from this channel" {
		t.Errorf("Label = %q", got)
	}
	if res.HistorySize != 5 {
		t.Errorf("HistorySize = %d, want 5", res.HistorySize)
	}
}

func TestClean_SponsorRuleRemovesParagraph(t *testing.T) {
	desc := "Good stuff\n\nThis video is sponsored by Acme. Use code SAVE at https://acme.example\n\nBye"
	res := clean(t, Input{Description: desc})
	if res.Cleaned != "Good stuff\n\nBye" {
		t.Errorf("Cleaned = %q", res.Cleaned)
	}
	if res.Removed[0].Reason != ReasonSponsor {
		t.Errorf("reason = %q, want sponsor", res.Removed[0].Reason)
	}
}

func TestClean_UserPatternRemovesAndReportsExcerpt(t *testing.T) {
	long := `nomatch ` + strings.Repeat("x", 200)
	res := clean(t, Input{Description: "Content\n\nVisit the Merch Store " + strings.Repeat("x", 200),
		Patterns: []string{long, `merch store`}})
	if res.Cleaned != "Content" {
		t.Fatalf("Cleaned = %q", res.Cleaned)
	}
	if res.Removed[0].Reason != ReasonUserPattern {
		t.Errorf("reason = %q", res.Removed[0].Reason)
	}
	if res.Removed[0].Detail != "merch store" {
		t.Errorf("detail = %q, want the matching pattern", res.Removed[0].Detail)
	}
}

func TestClean_UserPatternDetailIsCapped(t *testing.T) {
	pat := strings.Repeat("a", 200)
	res := clean(t, Input{Description: "keep\n\n" + pat, Patterns: []string{pat}})
	if n := len([]rune(res.Removed[0].Detail)); n > excerptInUI {
		t.Errorf("detail has %d characters, want at most %d", n, excerptInUI)
	}
}

func TestClean_ChapterGuard(t *testing.T) {
	chapters := "0:00 Intro\n1:23 Sponsored by Acme https://acme.example\n2:00 Outro"
	stats := BuildStats([]string{chapters, chapters, chapters, chapters, chapters})
	desc := "Text\n\n" + chapters
	t.Run("kept from sponsor and repetition layers", func(t *testing.T) {
		res := clean(t, Input{Description: desc, Stats: stats})
		if res.Cleaned != desc || len(res.Removed) != 0 {
			t.Errorf("Cleaned = %q, removed %v; chapter list must be kept", res.Cleaned, res.Removed)
		}
	})
	t.Run("removed by a user pattern", func(t *testing.T) {
		res := clean(t, Input{Description: desc, Patterns: []string{`intro`}})
		if res.Cleaned != "Text" || res.Removed[0].Reason != ReasonUserPattern {
			t.Errorf("Cleaned = %q, removed %v; user patterns are not guarded", res.Cleaned, res.Removed)
		}
	})
}

func TestClean_EverythingRemovedFallsBackToOriginal(t *testing.T) {
	desc := "Sponsored by Acme https://acme.example\n\nUse code X, 10% off, sponsored by Acme"
	res := clean(t, Input{Description: desc})
	if res.Cleaned != desc || !res.FellBack {
		t.Errorf("Cleaned = %q, FellBack = %v; want original and FellBack", res.Cleaned, res.FellBack)
	}
	if len(res.Removed) != 2 {
		t.Errorf("Removed = %d entries, want both still listed", len(res.Removed))
	}
}

func TestClean_IsDeterministic(t *testing.T) {
	history := descsWith(9, 6)
	desc := "A\n\n" + boiler + "\n\nSponsored by Acme https://a.example\n\nmerch here\n\nZ"
	in := func(h []string) Input {
		return Input{Description: desc, Stats: BuildStats(h), Patterns: []string{"merch"}}
	}
	want := clean(t, in(history))
	for i := 0; i < 100; i++ {
		shuffled := append([]string(nil), history...)
		shuffled[0], shuffled[len(shuffled)-1] = shuffled[len(shuffled)-1], shuffled[0]
		got := clean(t, in(shuffled))
		if !reflect.DeepEqual(stripVersion(got), stripVersion(want)) {
			t.Fatalf("run %d differs: %+v vs %+v", i, got, want)
		}
	}
}

func stripVersion(r Result) Result { return r }

func TestClean_OversizedDescriptionIsRejected(t *testing.T) {
	_, err := NewCleaner().Clean(context.Background(),
		Input{Description: strings.Repeat("a", MaxDescriptionBytes+1)}, nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestClean_CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewCleaner().Clean(ctx, Input{Description: "x"}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestClean_StoredHostilePatternsAreIgnoredOnRead(t *testing.T) {
	c := NewCleaner()
	desc := "Content\n\nSponsored by Acme https://acme.example"
	start := time.Now()
	res, err := c.Clean(context.Background(), Input{Description: desc,
		Patterns: []string{`\pL{1000}`, `[\x{0}-\x{10FFFF}]{1000}`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > hostileBound*2 {
		t.Errorf("took %v, want fast rejection", time.Since(start))
	}
	if !res.PatternsRejected {
		t.Error("PatternsRejected must be set")
	}
	if res.Cleaned != "Content" {
		t.Errorf("Cleaned = %q; built-in layers must still run", res.Cleaned)
	}
}

func TestClean_LargestPatternSetOnMaxSizeInputIsFast(t *testing.T) {
	pats, err := ParsePatterns([]string{`\pL{20}x`, `\pL{20}y`})
	if err != nil {
		t.Fatalf("setup patterns rejected: %v", err)
	}
	if pats[0].Cost+pats[1].Cost < maxTotalCost*8/10 {
		t.Fatalf("test set cost %d is not near the combined cap %d", pats[0].Cost+pats[1].Cost, maxTotalCost)
	}
	para := strings.Repeat("a", 4_998)
	desc := strings.Join([]string{para, para, para, para, para, para, para, para, para, para}, "\n\n")
	if len(desc) > MaxDescriptionBytes {
		desc = desc[:MaxDescriptionBytes]
	}
	start := time.Now()
	res, err := NewCleaner().Clean(context.Background(),
		Input{Description: desc, Patterns: Sources(pats)}, NewBudget(30*time.Second))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !res.PatternInputCapped {
		t.Error("the per-description matching cap must have applied")
	}
	if elapsed > time.Second {
		t.Errorf("took %v, want under 1s", elapsed)
	}
}

func TestClean_PatternInputIsCappedPerParagraphAndDescription(t *testing.T) {
	// The pattern sits past the per-paragraph cap, so it is never seen.
	para := strings.Repeat("a ", 3_000) + "needle"
	res := clean(t, Input{Description: "keep\n\n" + para, Patterns: []string{"needle"}})
	if len(res.Removed) != 0 {
		t.Error("text past the per-paragraph cap must not be matched")
	}
	// Past 20,000 total bytes, later paragraphs are not tested.
	var parts []string
	for i := 0; i < 5; i++ {
		parts = append(parts, strings.Repeat(fmt.Sprintf("w%d ", i), 1_600))
	}
	parts = append(parts, "needle")
	res = clean(t, Input{Description: strings.Join(parts, "\n\n"), Patterns: []string{"needle"}})
	if len(res.Removed) != 0 || !res.PatternInputCapped {
		t.Errorf("removed = %v, capped = %v; the description cap must stop matching", res.Removed, res.PatternInputCapped)
	}
}

func TestSafeExcerpt(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short unchanged", "abc", 10, "abc"},
		{"cut with ellipsis within the cap", "abcdefghij", 5, "abcd…"},
		{"control characters replaced", "a\nb\x00c", 10, "a b c"},
		{"unicode counted in runes", "ééééé", 3, "éé…"},
		{"zero cap", "abc", 0, ""},
	}
	for _, tc := range tests {
		if got := SafeExcerpt(tc.in, tc.max); got != tc.want {
			t.Errorf("%s: SafeExcerpt(%q, %d) = %q, want %q", tc.name, tc.in, tc.max, got, tc.want)
		}
	}
}

func TestErrorClass(t *testing.T) {
	tests := map[error]string{
		ErrTooLarge:                     ClassTooLarge,
		ErrTimeout:                      ClassTimeout,
		ErrBudgetExhausted:              ClassTimeout,
		context.Canceled:                ClassCanceled,
		errors.New("something"):         ClassPanic,
		fmt.Errorf("w: %w", ErrTimeout): ClassTimeout,
	}
	for err, want := range tests {
		if got := ErrorClass(err); got != want {
			t.Errorf("ErrorClass(%v) = %q, want %q", err, got, want)
		}
	}
}
