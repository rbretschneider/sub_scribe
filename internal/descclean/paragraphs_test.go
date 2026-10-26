package descclean

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitParagraphs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"single", "one line", []string{"one line"}},
		{"blank line splits", "a\n\nb", []string{"a", "b"}},
		{"many blank lines", "a\n\n\n\nb", []string{"a", "b"}},
		{"whitespace-only line is blank", "a\n   \t \nb", []string{"a", "b"}},
		{"crlf", "a\r\n\r\nb", []string{"a", "b"}},
		{"lone cr", "a\r\rb", []string{"a", "b"}},
		{"inner lines kept", "a\nb\n\nc", []string{"a\nb", "c"}},
		{"surrounding blanks dropped", "\n\na\n\n", []string{"a"}},
		{"empty", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitParagraphs(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitParagraphs(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestKeyOf_IgnoresCaseAndWhitespace(t *testing.T) {
	a := keyOf("Follow  me\non  Twitter")
	b := keyOf("  follow me on TWITTER ")
	if a != b {
		t.Error("keys differ for text that differs only in case and whitespace")
	}
	if keyOf("follow me on twitter") == keyOf("follow me on mastodon") {
		t.Error("keys equal for different text")
	}
}

func TestTruncateUTF8_NeverSplitsARune(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := truncateUTF8(s, 5)
	if got != "éé" {
		t.Errorf("truncateUTF8 = %q, want %q", got, "éé")
	}
	if truncateUTF8("abc", 10) != "abc" {
		t.Error("short strings must be returned unchanged")
	}
}
