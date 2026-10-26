package descclean

import (
	"regexp"
	"strings"
)

// The built-in sponsor rule removes a paragraph only when it holds BOTH a
// sponsor phrase and a link or promo-code phrase. Either alone is far too common
// in ordinary prose ("sponsored by" in a story, a link to a source).
//
// To extend it, add a lower-case phrase to one of the lists below and bump
// algorithmVersion so cached results are recomputed. The lists are English only
// by design. A test checks that every entry is lower case and compiles.

// sponsorPhrases signal that the paragraph is a sponsor read.
var sponsorPhrases = []string{
	"sponsored by",
	"this video is sponsored",
	"this video was sponsored",
	"brought to you by",
	"in partnership with",
	"paid promotion",
	"paid partnership",
	"#ad",
	"#sponsored",
	"for sponsoring",
	"affiliate link",
	"affiliate links",
	"i earn a commission",
	"we earn a commission",
}

// promoPhrases signal a discount or promo code.
var promoPhrases = []string{
	"use code",
	"use my code",
	"promo code",
	"discount code",
	"coupon code",
	"coupon",
	"% off",
	"percent off",
	"free trial",
}

// shortenerHosts are link shorteners and affiliate redirectors that count as a
// link even without a scheme.
var shortenerHosts = []string{
	"bit.ly", "amzn.to", "geni.us", "t.ly", "tinyurl.com",
	"shorturl.at", "goo.gl", "ow.ly", "buff.ly", "rebrand.ly",
}

// phraseAlternation builds a case-insensitive alternation of literal phrases.
// A word boundary is added on a side only when the phrase starts (or ends) with
// a word character, so "#ad" does not match "#adventure" and "% off" still
// matches in "20% off".
func phraseAlternation(phrases []string) *regexp.Regexp {
	parts := make([]string, 0, len(phrases))
	for _, p := range phrases {
		part := regexp.QuoteMeta(p)
		if isWordByte(p[0]) {
			part = `\b` + part
		}
		if isWordByte(p[len(p)-1]) {
			part += `\b`
		}
		parts = append(parts, part)
	}
	return regexp.MustCompile(`(?i)(?:` + strings.Join(parts, "|") + `)`)
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

var (
	sponsorPattern = phraseAlternation(sponsorPhrases)
	promoPattern   = phraseAlternation(promoPhrases)
	urlPattern     = regexp.MustCompile(`(?i)(?:\bhttps?://|\bwww\.|\b(?:` +
		strings.ReplaceAll(strings.Join(shortenerHosts, "|"), ".", `\.`) + `)/)\S`)

	// chapterLinePattern matches a line that starts with a timestamp such as
	// 0:00, 12:34 or 1:02:03, after optional bullet or bracket punctuation.
	chapterLinePattern = regexp.MustCompile(`(?m)^[ \t\-–•*\[(]*(?:\d{1,2}:)?\d{1,2}:\d{2}\b`)
)

// isChapterList reports whether the paragraph has a line starting with a
// timestamp. Chapter lists often carry sponsor names and links and are repeated
// in shape from video to video, so the built-in layers must leave them alone.
func isChapterList(p string) bool {
	return chapterLinePattern.MatchString(p)
}

// sponsorMatch applies the built-in sponsor rule. It returns a short, fixed-form
// description of what matched, safe to show to the user.
func sponsorMatch(p string) (string, bool) {
	phrase := sponsorPattern.FindString(p)
	if phrase == "" {
		return "", false
	}
	if urlPattern.MatchString(p) {
		return strings.ToLower(phrase) + " plus a link", true
	}
	if promo := promoPattern.FindString(p); promo != "" {
		return strings.ToLower(phrase) + " plus " + strings.ToLower(promo), true
	}
	return "", false
}
