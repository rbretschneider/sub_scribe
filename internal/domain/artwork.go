package domain

// ChannelArtwork is the imagery a provider publishes for a collection as a
// whole: the channel's avatar and its banner. It describes the series a media
// server shows, not any one video, which is why it belongs to the source rather
// than to media metadata.
//
// The fields are URLs rather than bytes. Fetching them is a decision for the
// layer that knows whether the files are already on disk, and the value stays
// cheap to pass around and to compare.
type ChannelArtwork struct {
	// PosterURL is the square channel avatar, which media servers use as the
	// series poster.
	PosterURL string
	// BackgroundURL is the wide channel banner, used as the series backdrop. A
	// channel need not have one, so this is often empty even when a poster exists.
	BackgroundURL string
}

// IsEmpty reports whether the provider offered no usable imagery at all, which
// is the one case where there is nothing for a caller to write.
func (a ChannelArtwork) IsEmpty() bool {
	return a.PosterURL == "" && a.BackgroundURL == ""
}
