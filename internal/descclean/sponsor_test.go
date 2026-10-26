package descclean

import (
	"regexp"
	"strings"
	"testing"
)

func TestSponsorRule(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"phrase plus link", "This video is sponsored by Acme. Visit https://acme.example/deal", true},
		{"phrase plus www link", "Sponsored by Acme www.acme.example", true},
		{"phrase plus shortener", "Brought to you by Acme: bit.ly/acme", true},
		{"phrase plus promo phrase", "Sponsored by Acme. Use code SAVE for 20% off", true},
		{"phrase plus percent off", "Paid promotion. 20% off today", true},
		{"hashtag ad plus link", "#ad https://example.com/x", true},
		{"phrase only", "This episode is sponsored by my patient wife.", false},
		{"link only", "Source: https://example.com/paper", false},
		{"promo only", "Use code word to open the door.", false},
		{"non-english phrase", "Patrocinado por Acme https://acme.example", false},
		{"hashtag prefix is not ad", "#adventure https://example.com", false},
		{"upper case", "SPONSORED BY ACME HTTPS://ACME.EXAMPLE", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, got := sponsorMatch(tc.in)
			if got != tc.want {
				t.Errorf("sponsorMatch(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSponsorMatch_NamesWhatMatched(t *testing.T) {
	why, ok := sponsorMatch("Sponsored By Acme https://acme.example")
	if !ok || why != "sponsored by plus a link" {
		t.Errorf("sponsorMatch = %q, %v; want %q, true", why, ok, "sponsored by plus a link")
	}
}

func TestChapterGuard(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"minutes seconds", "0:00 Intro\n1:23 Topic", true},
		{"hours", "1:02:03 Part two", true},
		{"bullet", "- 12:34 Something", true},
		{"bracket", "[0:00] Start", true},
		{"later line", "Chapters\n0:00 Intro", true},
		{"plain text", "Thanks for watching", false},
		{"time mid line", "We met at 10:30 yesterday", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isChapterList(tc.in); got != tc.want {
				t.Errorf("isChapterList(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestPhraseLists_AreLowerCaseAndCompile(t *testing.T) {
	for _, list := range [][]string{sponsorPhrases, promoPhrases} {
		for _, p := range list {
			if p == "" || p != strings.ToLower(p) {
				t.Errorf("phrase %q must be non-empty and lower case", p)
			}
			if _, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(p)); err != nil {
				t.Errorf("phrase %q does not compile: %v", p, err)
			}
		}
	}
}
