package domain

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

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

// MetadataFormat selects the shape of the sidecar .nfo written beside each
// download. It describes the *library layout* rather than a particular media
// server, because the sidecar has to agree with the folder structure the output
// template produces — a season-based layout with movie metadata in it is
// self-contradictory, and readers handle it badly.
//
// This deliberately does not name Plex or Jellyfin. Kodi, Jellyfin and Emby all
// read the same Kodi-derived format, and Plex reads no .nfo at all without a
// third-party agent, so a per-server distinction was never real.
type MetadataFormat string

const (
	// MetadataEpisode writes an <episodedetails> NFO, matching a season-based
	// layout such as "Channel/Season 2026/…". This is the default because the
	// default output template is season-based.
	MetadataEpisode MetadataFormat = "episode"
	// MetadataMovie writes a <movie> NFO, for a flat layout where each video
	// stands alone rather than belonging to a season.
	MetadataMovie MetadataFormat = "movie"
)

// IsValid reports whether the metadata format is a recognized value.
func (m MetadataFormat) IsValid() bool {
	return m == MetadataEpisode || m == MetadataMovie
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

	// DownloadDir, when set, is the absolute container path this profile's
	// downloads land under instead of the main media directory — the mechanism
	// that lets a "Movies" profile file IMDb-documented finds straight into a
	// Plex movie library while everything else stays in the YouTube archive.
	// Empty uses the main media directory.
	DownloadDir string

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
	// WriteSubtitles saves subtitles as .srt files beside the media file —
	// "Video.en.srt" — which is the layout Plex and Jellyfin ingest directly.
	// YouTube's auto-generated captions stand in when a video has no real
	// subtitles, so nearly every download gets one. On by default: obtaining
	// them costs almost nothing and harms nothing.
	WriteSubtitles bool
	// SubtitleLanguages are yt-dlp language codes to fetch, e.g. []string{"en"}.
	// Empty falls back to English rather than fetching nothing, so leaving the
	// field blank never silently disables the subtitles feature.
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

// heightCapPattern matches the height bound inside a yt-dlp format selector,
// e.g. the 1080 in "bestvideo[height<=1080]+bestaudio".
var heightCapPattern = regexp.MustCompile(`height\s*<=?\s*(\d+)`)

// Thresholds for IsDegradedDownload. The gap between them is deliberately wide:
// a 720p profile that gets 720p or even 540p is YouTube's normal ladder at
// work, while a profile asking for HD that receives 480p or less is the
// signature of the provider withholding every adaptive format — an outage, not
// a choice.
const (
	// degradedRequestFloor is the smallest requested height cap the check
	// applies to; a profile content with less than HD is never flagged.
	degradedRequestFloor = 720
	// degradedActualCeiling is the downloaded height at or below which an
	// HD-requesting profile's download counts as degraded.
	degradedActualCeiling = 480
)

// RequestedHeightCap returns the height bound this profile's format selector
// asks yt-dlp for, or 0 when the selector sets none (such as a bare
// "bestvideo+bestaudio").
func (p MediaProfile) RequestedHeightCap() int {
	match := heightCapPattern.FindStringSubmatch(p.QualityFormat)
	if match == nil {
		return 0
	}
	capHeight, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return capHeight
}

// IsDegradedDownload reports whether a finished download at actualHeight came
// in far below what this profile asks for: the profile wants HD — a height cap
// of 720 or more, or an uncapped bestvideo selector — but the file is 480p or
// less. That combination means the format selector's "/best" fallback fired
// because the provider offered nothing better — the symptom of a PO-token or
// extraction outage — and the archived copy is one the user will want
// re-fetched. An unknown height (0: audio-only or adopted files) is never
// degraded.
func (p MediaProfile) IsDegradedDownload(actualHeight int) bool {
	if actualHeight <= 0 || actualHeight > degradedActualCeiling {
		return false
	}
	capHeight := p.RequestedHeightCap()
	if capHeight == 0 {
		return strings.Contains(p.QualityFormat, "bestvideo")
	}
	return capHeight >= degradedRequestFloor
}
