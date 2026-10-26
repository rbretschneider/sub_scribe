package descclean

import (
	"crypto/sha256"
	"strings"
	"unicode"
)

// keySize is how many bytes of the SHA-256 identify a paragraph. Sixteen bytes
// keep a channel's statistics to a few dozen bytes per distinct paragraph while
// making an accidental collision practically impossible.
const keySize = 16

// paragraphKey identifies a paragraph ignoring case and whitespace.
type paragraphKey [keySize]byte

// normalizeNewlines turns CRLF and lone CR line endings into LF so that the same
// text saved by different tools splits the same way.
func normalizeNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// splitParagraphs splits a description into paragraphs: runs of lines separated
// by one or more blank lines, where a blank line holds only whitespace. Each
// paragraph keeps its inner lines untouched; the blank lines around it are
// dropped.
func splitParagraphs(s string) []string {
	var (
		paragraphs []string
		current    []string
	)
	flush := func() {
		if len(current) > 0 {
			paragraphs = append(paragraphs, strings.Join(current, "\n"))
			current = nil
		}
	}
	for _, line := range strings.Split(normalizeNewlines(s), "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		current = append(current, line)
	}
	flush()
	return paragraphs
}

// collapseSpace lower-cases nothing: it only replaces every run of whitespace
// with one space and trims the ends.
func collapseSpace(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// normalizeParagraph is the comparison form for repetition: lower case with
// whitespace collapsed. That is exactly "ignoring whitespace and letter case".
func normalizeParagraph(p string) string {
	return strings.ToLower(collapseSpace(p))
}

// keyOf hashes a paragraph's normalised form.
func keyOf(p string) paragraphKey {
	sum := sha256.Sum256([]byte(normalizeParagraph(p)))
	var key paragraphKey
	copy(key[:], sum[:keySize])
	return key
}

// truncateUTF8 shortens s to at most max bytes without cutting a rune in half.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// isRuneStart reports whether b can begin a UTF-8 encoded rune.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
