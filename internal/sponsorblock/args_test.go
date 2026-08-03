package sponsorblock

import (
	"reflect"
	"testing"

	"sub_scribe/internal/domain"
)

func TestBuilderArgs(t *testing.T) {
	tests := []struct {
		name       string
		mode       domain.SponsorBlockMode
		categories []domain.SponsorBlockCategory
		want       []string
	}{
		{
			name: "off returns nil",
			mode: domain.SponsorBlockOff,
			categories: []domain.SponsorBlockCategory{
				domain.SponsorBlockSponsor,
			},
			want: nil,
		},
		{
			name:       "unrecognized mode returns nil",
			mode:       domain.SponsorBlockMode("bogus"),
			categories: nil,
			want:       nil,
		},
		{
			name: "remove with categories",
			mode: domain.SponsorBlockRemove,
			categories: []domain.SponsorBlockCategory{
				domain.SponsorBlockSponsor,
				domain.SponsorBlockIntro,
			},
			want: []string{"--sponsorblock-remove", "sponsor,intro"},
		},
		{
			name: "mark with categories",
			mode: domain.SponsorBlockMark,
			categories: []domain.SponsorBlockCategory{
				domain.SponsorBlockOutro,
				domain.SponsorBlockFiller,
			},
			want: []string{"--sponsorblock-mark", "outro,filler"},
		},
		{
			// Naming no categories once meant "cut sponsor, self-promotion, and
			// interaction", chosen by nobody and shown nowhere. Cutting is
			// permanent, so silence has to mean silence.
			name:       "remove with no categories cuts nothing",
			mode:       domain.SponsorBlockRemove,
			categories: nil,
			want:       nil,
		},
		{
			name:       "mark with an empty slice marks nothing",
			mode:       domain.SponsorBlockMark,
			categories: []domain.SponsorBlockCategory{},
			want:       nil,
		},
	}

	b := NewBuilder()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := b.Args(tt.mode, tt.categories)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Args(%q, %v) = %v, want %v", tt.mode, tt.categories, got, tt.want)
			}
		})
	}
}
