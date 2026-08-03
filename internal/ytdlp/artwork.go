package ytdlp

import (
	"encoding/json"
	"fmt"
	"strings"

	"sub_scribe/internal/domain"
)

// Thumbnail ids YouTube attaches to a channel's imagery. The uncropped variants
// are the full-size originals; the rest are pre-cropped derivatives sized for
// YouTube's own layout, which is not the shape a media server wants.
const (
	avatarThumbnailID = "avatar"
	bannerThumbnailID = "banner"
)

// collectionDetails is the subset of yt-dlp's single-JSON output for a
// collection that this package consumes. Only the artwork is read: the rest of a
// channel's details already arrive through indexing.
type collectionDetails struct {
	Thumbnails []collectionThumbnail `json:"thumbnails"`
}

// collectionThumbnail is one entry of a collection's thumbnail list. The id
// names the role YouTube intends the image for, and the width separates the
// full-size original from its smaller derivatives.
type collectionThumbnail struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Width int    `json:"width"`
}

// buildArtworkArgs builds the yt-dlp argument list for fetching a collection's
// own details without enumerating its contents.
//
// --playlist-items 0 is what keeps this cheap: yt-dlp still resolves the
// collection and reports everything it knows about it, but matches none of its
// items, so asking a channel with a thousand videos for its avatar costs the
// same as asking one with three.
func buildArtworkArgs(url, cookiesPath, potProviderURL string, throttle Throttle) []string {
	args := []string{flagDumpSingleJSON, flagFlatPlaylist, flagPlaylistItems, noPlaylistItems}
	args = appendCookies(args, cookiesPath)
	args = appendPOTProvider(args, potProviderURL)
	args = throttle.appendRequestFlags(args)
	return append(args, url)
}

// parseArtwork reads a collection's poster and background out of yt-dlp's
// single-JSON output. Malformed JSON is an error; a collection that simply
// publishes no imagery is not, and yields an empty ChannelArtwork.
func parseArtwork(body []byte) (domain.ChannelArtwork, error) {
	var details collectionDetails
	if err := json.Unmarshal(body, &details); err != nil {
		return domain.ChannelArtwork{}, fmt.Errorf("parse collection details: %w", err)
	}
	return domain.ChannelArtwork{
		PosterURL:     posterFrom(details.Thumbnails),
		BackgroundURL: largestWithID(details.Thumbnails, bannerThumbnailID),
	}, nil
}

// posterFrom picks the image to use as the series poster: the channel avatar
// when there is one, and otherwise the collection's largest non-banner image.
//
// The fallback is what makes a playlist work. A playlist has no avatar, only a
// still lifted from one of its videos — a worse poster than a channel logo, but
// far better than the blank placeholder a media server shows without one.
func posterFrom(thumbnails []collectionThumbnail) string {
	if avatar := largestWithID(thumbnails, avatarThumbnailID); avatar != "" {
		return avatar
	}
	return largestWithID(thumbnails, "")
}

// largestWithID returns the widest thumbnail whose id contains want, or the
// widest image that is not a banner when want is empty. Width is the tiebreaker
// because YouTube lists the same image at several sizes and a media server
// scales down far more gracefully than up.
func largestWithID(thumbnails []collectionThumbnail, want string) string {
	best := collectionThumbnail{Width: -1}
	for _, thumbnail := range thumbnails {
		if thumbnail.URL == "" || !matchesRole(thumbnail.ID, want) {
			continue
		}
		if thumbnail.Width > best.Width {
			best = thumbnail
		}
	}
	return best.URL
}

// matchesRole reports whether a thumbnail id fills the requested role. An empty
// role means "any image that is not a banner", since a banner's shape makes it
// unusable as a poster.
func matchesRole(id, want string) bool {
	lowered := strings.ToLower(id)
	if want == "" {
		return !strings.Contains(lowered, bannerThumbnailID)
	}
	return strings.Contains(lowered, want)
}
