package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"sub_scribe/internal/domain"
)

// Form field names posted by the profile form.
const (
	fieldProfileName       = "name"
	fieldOutputTemplate    = "output_path_template"
	fieldKind              = "kind"
	fieldQualityFormat     = "quality_format"
	fieldEmbedMetadata     = "embed_metadata"
	fieldEmbedThumbnail    = "embed_thumbnail"
	fieldWriteThumbnail    = "write_thumbnail"
	fieldEmbedSubtitles    = "embed_subtitles"
	fieldSubtitleLanguages = "subtitle_languages"
	fieldSponsorBlockMode  = "sponsorblock_mode"
	fieldSponsorBlockCats  = "sponsorblock_categories"
	fieldRedownloadDays    = "redownload_days"
	fieldMetadataFormat    = "metadata_format"
	fieldExtraYtdlpArgs    = "extra_ytdlp_args"
	fieldPostDownloadCmd   = "post_download_command"
)

// defaultTemplateHint seeds the new-profile form with the season/episode layout
// media servers can actually read, so a new profile starts from something that
// works rather than a blank field.
const defaultTemplateHint = "{{ source_name }}/Season {{ upload_year }}/{{ season_episode }} - {{ title }}"

// profileFormValues holds the raw, user-entered profile fields so the form can be
// re-rendered exactly on a validation error (recognition over recall).
type profileFormValues struct {
	Name                   string
	OutputPathTemplate     string
	Kind                   string
	QualityFormat          string
	MetadataFormat         string
	EmbedMetadata          bool
	EmbedThumbnail         bool
	WriteThumbnail         bool
	EmbedSubtitles         bool
	SubtitleLanguages      string
	SponsorBlockMode       string
	SponsorBlockCategories string
	RedownloadDays         string
	ExtraYtdlpArgs         string
	PostDownloadCommand    string
}

// defaultProfileFormValues are the sensible starting values for a new profile.
func defaultProfileFormValues() profileFormValues {
	return profileFormValues{
		OutputPathTemplate: defaultTemplateHint,
		Kind:               string(domain.MediaVideo),
		QualityFormat:      "bestvideo[height<=1080]+bestaudio/best",
		MetadataFormat:     string(domain.MetadataEpisode),
		EmbedMetadata:      true,
		EmbedThumbnail:     true,
		WriteThumbnail:     true,
		SponsorBlockMode:   string(domain.SponsorBlockRemove),
		// Ticked so the form shows exactly what it will do. Sponsors alone,
		// because every other category is a judgement call the user should make
		// deliberately rather than inherit.
		SponsorBlockCategories: string(domain.SponsorBlockSponsor),
	}
}

// readProfileFormValues extracts the raw submitted profile fields.
func readProfileFormValues(r *http.Request) profileFormValues {
	// Parsed up front so the multi-valued checkbox group can be read from
	// PostForm directly, rather than depending on some other field being read
	// first to trigger parsing.
	_ = r.ParseForm()

	return profileFormValues{
		Name:               r.PostFormValue(fieldProfileName),
		OutputPathTemplate: r.PostFormValue(fieldOutputTemplate),
		Kind:               r.PostFormValue(fieldKind),
		QualityFormat:      r.PostFormValue(fieldQualityFormat),
		MetadataFormat:     r.PostFormValue(fieldMetadataFormat),
		EmbedMetadata:      isChecked(r, fieldEmbedMetadata),
		EmbedThumbnail:     isChecked(r, fieldEmbedThumbnail),
		WriteThumbnail:     isChecked(r, fieldWriteThumbnail),
		EmbedSubtitles:     isChecked(r, fieldEmbedSubtitles),
		SubtitleLanguages:  r.PostFormValue(fieldSubtitleLanguages),
		SponsorBlockMode:   r.PostFormValue(fieldSponsorBlockMode),
		// The categories are checkboxes now, so several values arrive under one
		// name; they are kept as CSV internally to match how they are stored.
		SponsorBlockCategories: strings.Join(r.PostForm[fieldSponsorBlockCats], ","),
		RedownloadDays:         r.PostFormValue(fieldRedownloadDays),
		ExtraYtdlpArgs:         r.PostFormValue(fieldExtraYtdlpArgs),
		PostDownloadCommand:    r.PostFormValue(fieldPostDownloadCmd),
	}
}

// toProfile validates the raw values and assembles a domain.MediaProfile. It
// stops at the first problem with one clear message; the naming template itself
// is validated by the service, whose error is surfaced to the user.
func (v profileFormValues) toProfile() (domain.MediaProfile, error) {
	name := strings.TrimSpace(v.Name)
	if name == "" {
		return domain.MediaProfile{}, errors.New("Please enter a name for this profile.")
	}
	template := strings.TrimSpace(v.OutputPathTemplate)
	if template == "" {
		return domain.MediaProfile{}, errors.New("Please enter an output path template.")
	}
	kind := domain.MediaKind(v.Kind)
	if !kind.IsValid() {
		return domain.MediaProfile{}, errors.New("Please choose whether this profile downloads video or audio.")
	}
	mode := domain.SponsorBlockMode(v.SponsorBlockMode)
	if !mode.IsValid() {
		return domain.MediaProfile{}, errors.New("Please choose a SponsorBlock mode.")
	}
	metadataFormat := domain.MetadataFormat(v.MetadataFormat)
	if !metadataFormat.IsValid() {
		return domain.MediaProfile{}, errors.New("Please choose a sidecar metadata layout (episode or movie).")
	}
	days := atoiOrZero(v.RedownloadDays)
	if days < 0 {
		return domain.MediaProfile{}, errors.New("Redownload days can't be negative.")
	}

	return domain.MediaProfile{
		Name:                   name,
		OutputPathTemplate:     template,
		Kind:                   kind,
		QualityFormat:          strings.TrimSpace(v.QualityFormat),
		MetadataFormat:         metadataFormat,
		EmbedMetadata:          v.EmbedMetadata,
		EmbedThumbnail:         v.EmbedThumbnail,
		WriteThumbnail:         v.WriteThumbnail,
		EmbedSubtitles:         v.EmbedSubtitles,
		SubtitleLanguages:      splitCSV(v.SubtitleLanguages),
		SponsorBlockMode:       mode,
		SponsorBlockCategories: toCategories(splitCSV(v.SponsorBlockCategories)),
		RedownloadAfter:        time.Duration(days) * hoursPerDayDuration,
		ExtraYtdlpArgs:         splitLines(v.ExtraYtdlpArgs),
		PostDownloadCommand:    strings.TrimSpace(v.PostDownloadCommand),
	}, nil
}

// splitLines parses a textarea into trimmed, non-empty lines — one yt-dlp
// argument per line, so values containing spaces need no quote parsing.
func splitLines(raw string) []string {
	var result []string
	for _, line := range strings.Split(raw, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// fromProfile maps a persisted profile back onto raw form values for editing.
func fromProfile(profile domain.MediaProfile) profileFormValues {
	return profileFormValues{
		Name:                   profile.Name,
		OutputPathTemplate:     profile.OutputPathTemplate,
		Kind:                   string(profile.Kind),
		QualityFormat:          profile.QualityFormat,
		MetadataFormat:         string(profile.MetadataFormat),
		EmbedMetadata:          profile.EmbedMetadata,
		EmbedThumbnail:         profile.EmbedThumbnail,
		WriteThumbnail:         profile.WriteThumbnail,
		EmbedSubtitles:         profile.EmbedSubtitles,
		SubtitleLanguages:      strings.Join(profile.SubtitleLanguages, ", "),
		SponsorBlockMode:       string(profile.SponsorBlockMode),
		SponsorBlockCategories: joinCategories(profile.SponsorBlockCategories),
		RedownloadDays:         optionalDaysString(profile.RedownloadAfter),
		ExtraYtdlpArgs:         strings.Join(profile.ExtraYtdlpArgs, "\n"),
		PostDownloadCommand:    profile.PostDownloadCommand,
	}
}

// isChecked reports whether a checkbox field was submitted (checked).
func isChecked(r *http.Request, field string) bool {
	return r.PostFormValue(field) != ""
}

// splitCSV parses a comma-separated list into trimmed, non-empty values, or nil.
func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// toCategories converts strings into SponsorBlock categories.
func toCategories(values []string) []domain.SponsorBlockCategory {
	if len(values) == 0 {
		return nil
	}
	categories := make([]domain.SponsorBlockCategory, len(values))
	for i, value := range values {
		categories[i] = domain.SponsorBlockCategory(value)
	}
	return categories
}

// joinCategories renders categories as a comma-separated string for the form.
func joinCategories(categories []domain.SponsorBlockCategory) string {
	parts := make([]string, len(categories))
	for i, category := range categories {
		parts[i] = string(category)
	}
	return strings.Join(parts, ", ")
}
