package domain

import "time"

// SponsorBlockCategory names a segment type from the SponsorBlock database that
// can be removed or marked in downloaded media.
type SponsorBlockCategory string

const (
	SponsorBlockSponsor       SponsorBlockCategory = "sponsor"
	SponsorBlockIntro         SponsorBlockCategory = "intro"
	SponsorBlockOutro         SponsorBlockCategory = "outro"
	SponsorBlockSelfPromo     SponsorBlockCategory = "selfpromo"
	SponsorBlockInteraction   SponsorBlockCategory = "interaction"
	SponsorBlockMusicOfftopic SponsorBlockCategory = "music_offtopic"
	SponsorBlockPreview       SponsorBlockCategory = "preview"
	SponsorBlockFiller        SponsorBlockCategory = "filler"
)

// SponsorBlockMode selects how matched SponsorBlock segments are handled.
type SponsorBlockMode string

const (
	// SponsorBlockOff disables SponsorBlock processing.
	SponsorBlockOff SponsorBlockMode = "off"
	// SponsorBlockRemove cuts matched segments out of the media file.
	SponsorBlockRemove SponsorBlockMode = "remove"
	// SponsorBlockMark adds chapter markers without cutting.
	SponsorBlockMark SponsorBlockMode = "mark"
)

// IsValid reports whether the SponsorBlock mode is a recognized value.
func (m SponsorBlockMode) IsValid() bool {
	return m == SponsorBlockOff || m == SponsorBlockRemove || m == SponsorBlockMark
}

// MetadataFormat selects the sidecar layout written for downloaded media so it
// matches the target media server. Jellyfin/Kodi/Emby read TV-style
// <episodedetails> NFO natively; Plex treats each video as a movie and reads
// <movie> NFO via its NFO agent.
type MetadataFormat string

const (
	// MetadataJellyfin writes a Kodi/Jellyfin/Emby <episodedetails> NFO.
	MetadataJellyfin MetadataFormat = "jellyfin"
	// MetadataPlex writes a Plex-oriented <movie> NFO.
	MetadataPlex MetadataFormat = "plex"
)

// IsValid reports whether the metadata format is a recognized value.
func (m MetadataFormat) IsValid() bool {
	return m == MetadataJellyfin || m == MetadataPlex
}

// MediaProfile is a reusable bundle of "how and where to download" settings that
// many Sources can share. Separating it from Source lets a user define, say, a
// "1080p Plex TV" profile once and attach it to every channel, honoring DRY and
// the Open-Closed principle (new profiles, not edited Sources).
type MediaProfile struct {
	ID   int64
	Name string

	// OutputPathTemplate is the naming template (see the naming package) that
	// maps a Media's metadata to a destination path relative to the media root.
	OutputPathTemplate string

	Kind          MediaKind
	QualityFormat string // yt-dlp -f format selector, e.g. "bestvideo[height<=1080]+bestaudio"

	// MetadataFormat selects the sidecar layout (Plex vs Jellyfin) written for
	// this profile's downloads.
	MetadataFormat MetadataFormat

	EmbedMetadata bool
	// EmbedThumbnail stores the cover art inside the media file itself.
	EmbedThumbnail bool
	// WriteThumbnail saves the cover art as a JPEG beside the media file, sharing
	// its name. That is the layout Plex and Jellyfin read as episode artwork, so
	// it is what makes a downloaded video look right in a media server — and what
	// gives sub_scribe's own library screen a real image to show.
	WriteThumbnail bool
	EmbedSubtitles bool
	// SubtitleLanguages are yt-dlp language codes to fetch, e.g. []string{"en"}.
	SubtitleLanguages []string

	SponsorBlockMode       SponsorBlockMode
	SponsorBlockCategories []SponsorBlockCategory

	// RedownloadAfter, when non-zero, re-fetches media older than this age to
	// pick up quality improvements (e.g. a livestream's final VOD encode).
	RedownloadAfter time.Duration

	// ExtraYtdlpArgs are raw yt-dlp arguments appended verbatim to the download
	// command, one slice element per flag or value, for power-user tuning.
	ExtraYtdlpArgs []string
	// PostDownloadCommand, when set, is an executable or script run after each
	// successful download with the downloaded file's path as its argument.
	PostDownloadCommand string

	CreatedAt time.Time
	UpdatedAt time.Time
}
