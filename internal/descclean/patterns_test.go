package descclean

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// hostileBound is how long rejecting a hostile pattern may take, including the
// parse. A pattern that is only rejected after being expanded would blow it.
const hostileBound = 100 * time.Millisecond

func TestParsePatterns_AcceptsOrdinaryPatterns(t *testing.T) {
	good := []string{
		`follow me on (twitter|instagram|tiktok)`,
		`\bcode\s+\w+`,
		`(?m)^follow me.*$`,
		`\pL{20}`,
		`(a+)+$`,
		`(?-i)CaseSensitive`,
	}
	for _, p := range good {
		t.Run(p, func(t *testing.T) {
			got, err := ParsePatterns([]string{p})
			if err != nil || len(got) != 1 {
				t.Fatalf("ParsePatterns(%q) = %v, %v; want one pattern", p, got, err)
			}
			if got[0].Source != p {
				t.Errorf("Source = %q, want %q", got[0].Source, p)
			}
		})
	}
}

func TestParsePatterns_NormalisesList(t *testing.T) {
	got, err := ParsePatterns([]string{"  a ", "", "   ", "b", "a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b"}; strings.Join(Sources(got), ",") != strings.Join(want, ",") {
		t.Errorf("Sources = %q, want %q (trimmed, blanks dropped, duplicates removed, order kept)", Sources(got), want)
	}
}

func TestParsePatterns_MatchesCaseInsensitivelyUnlessOptedOut(t *testing.T) {
	got, err := ParsePatterns([]string{"hello", "(?-i)World"})
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Matches("HELLO there") {
		t.Error("default matching must be case-insensitive")
	}
	if got[1].Matches("world") || !got[1].Matches("World") {
		t.Error("(?-i) must switch case-insensitivity off")
	}
}

func TestParsePatterns_RejectsHostileAndInvalid(t *testing.T) {
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("word%d", i)
	}
	tests := []struct {
		name string
		in   []string
		want error
	}{
		{"repeat 1000", []string{`\pL{1000}`}, ErrTooComplex},
		{"repeat 101", []string{`\pL{101}`}, ErrTooComplex},
		{"repeat 100 costs too much", []string{`\pL{100}`}, ErrTooComplex},
		{"wide class repeat 1000", []string{`[\x{0}-\x{10FFFF}]{1000}`}, ErrTooComplex},
		{"nested product 2500", []string{`(\pL{50}){50}`}, ErrTooComplex},
		{"nested product 1100", []string{`((a{10}){10}){11}`}, ErrTooComplex},
		{"open repeat over cap", []string{`a{101,}`}, ErrTooComplex},
		{"alternation of classes", []string{`\p{Lu}{12}|\p{Ll}{12}|\p{Lo}{12}|\p{Nd}{12}|\p{Mn}{12}|\p{So}{12}`}, ErrTooComplex},
		{"combined cost", []string{`\pL{15}a`, `\pL{15}b`, `\pL{15}c`, `\pL{15}d`}, ErrTooComplexTogether},
		{"21 patterns", many, ErrTooMany},
		{"501 bytes", []string{strings.Repeat("a", 501)}, ErrTooLong},
		{"NUL", []string{"a\x00b"}, ErrSyntax},
		{"unclosed group", []string{`(unclosed`}, ErrSyntax},
		{"lookahead", []string{`a(?=b)`}, ErrSyntax},
		{"backreference", []string{`(a)\1`}, ErrSyntax},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got, err := ParsePatterns(tc.in)
			elapsed := time.Since(start)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParsePatterns error = %v, want %v", err, tc.want)
			}
			if got != nil {
				t.Errorf("rejected input must yield no patterns, got %d", len(got))
			}
			if elapsed > hostileBound {
				t.Errorf("rejection took %v, want under %v", elapsed, hostileBound)
			}
		})
	}
}

func TestParsePatterns_500BytesIsAccepted(t *testing.T) {
	if _, err := ParsePatterns([]string{strings.Repeat("a", 500)}); err != nil {
		t.Errorf("a 500-byte pattern must be accepted: %v", err)
	}
	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = fmt.Sprintf("word%d", i)
	}
	if _, err := ParsePatterns(twenty); err != nil {
		t.Errorf("20 patterns must be accepted: %v", err)
	}
}

func TestParsePatterns_ErrorNeverEchoesLongPattern(t *testing.T) {
	canary := strings.Repeat("CANARY", 20) + `(`
	_, err := ParsePatterns([]string{canary})
	if err == nil {
		t.Fatal("expected a syntax error")
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), strings.Repeat("CANARY", 10)) {
		t.Errorf("error text carries more than a short excerpt: %q", err.Error())
	}
}

func TestParsePatterns_NestedQuantifierIsFastOnLongInput(t *testing.T) {
	got, err := ParsePatterns([]string{`(a+)+$`})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("a", 4_999) + "!"
	start := time.Now()
	if got[0].Matches(text) {
		t.Error("pattern must not match text ending in '!'")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("match took %v, want linear-time behaviour", elapsed)
	}
}

func TestProgramCost_UsesSimplifiedProgramAndClassRanges(t *testing.T) {
	letters, err := ParsePatterns([]string{`\pL`})
	if err != nil {
		t.Fatal(err)
	}
	twenty, err := ParsePatterns([]string{`\pL{20}`})
	if err != nil {
		t.Fatal(err)
	}
	// If the cost were measured on the raw tree, a repeat would cost the same as
	// its body. On the simplified program it grows with the count.
	if twenty[0].Cost < 15*letters[0].Cost {
		t.Errorf("cost of \\pL{20} = %d vs \\pL = %d; repeats must be unrolled when measured", twenty[0].Cost, letters[0].Cost)
	}
	plain, _ := ParsePatterns([]string{`a`})
	if letters[0].Cost <= plain[0].Cost+1 {
		t.Errorf("cost of \\pL = %d vs a = %d; wide classes must be charged for their ranges", letters[0].Cost, plain[0].Cost)
	}
	// (?i) expands letters into ranges; the folded class must cost more than the
	// same class with folding disabled.
	folded, _ := ParsePatterns([]string{`[a-z]{50}`})
	exact, _ := ParsePatterns([]string{`(?-i)[a-z]{50}`})
	if folded[0].Cost < exact[0].Cost {
		t.Errorf("case-folded cost %d is lower than exact cost %d", folded[0].Cost, exact[0].Cost)
	}
}
