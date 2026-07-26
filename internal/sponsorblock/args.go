// Package sponsorblock converts a media profile's SponsorBlock settings into the
// yt-dlp command-line arguments that remove or mark matched segments.
package sponsorblock

import (
	"strings"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// Ensure Builder satisfies the core's injectable interface.
var _ library.SponsorBlockBuilder = (*Builder)(nil)

const (
	// flagRemove cuts matched SponsorBlock segments out of the media file.
	flagRemove = "--sponsorblock-remove"
	// flagMark adds chapter markers for matched segments without cutting.
	flagMark = "--sponsorblock-mark"
	// categorySeparator joins category names as yt-dlp expects them.
	categorySeparator = ","
)

// defaultCategories is the sensible fallback used when a profile enables
// SponsorBlock but names no categories.
var defaultCategories = []domain.SponsorBlockCategory{
	domain.SponsorBlockSponsor,
	domain.SponsorBlockSelfPromo,
	domain.SponsorBlockInteraction,
}

// Builder turns SponsorBlock settings into yt-dlp arguments. It holds no state.
type Builder struct{}

// NewBuilder returns a ready-to-use Builder.
func NewBuilder() *Builder {
	return &Builder{}
}

// Args returns the yt-dlp arguments for the given SponsorBlock mode and
// categories. It returns nil when SponsorBlock is off. When no categories are
// supplied a sensible default set is used.
func (b *Builder) Args(mode domain.SponsorBlockMode, categories []domain.SponsorBlockCategory) []string {
	flag, ok := flagForMode(mode)
	if !ok {
		return nil
	}
	return []string{flag, joinCategories(categories)}
}

// flagForMode maps an active mode to its yt-dlp flag, reporting false for modes
// that produce no arguments (off or unrecognized).
func flagForMode(mode domain.SponsorBlockMode) (string, bool) {
	switch mode {
	case domain.SponsorBlockRemove:
		return flagRemove, true
	case domain.SponsorBlockMark:
		return flagMark, true
	default:
		return "", false
	}
}

// joinCategories renders categories as a comma-separated string, falling back to
// the default set when none are provided.
func joinCategories(categories []domain.SponsorBlockCategory) string {
	if len(categories) == 0 {
		categories = defaultCategories
	}
	names := make([]string, len(categories))
	for i, c := range categories {
		names[i] = string(c)
	}
	return strings.Join(names, categorySeparator)
}
