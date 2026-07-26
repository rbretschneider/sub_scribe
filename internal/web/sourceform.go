package web

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// Form field names posted by the add-source form.
const (
	fieldName            = "name"
	fieldURL             = "url"
	fieldCollectionType  = "collection_type"
	fieldMediaProfileID  = "media_profile_id"
	fieldCookieBehavior  = "cookie_behavior"
	fieldFrequencyHours  = "frequency_hours"
	fieldCutoffWindow    = "cutoff_window"
	fieldDownloadCutoff  = "download_cutoff"
	fieldTitleFilter     = "title_filter"
	fieldShortsRule      = "shorts_rule"
	fieldLivestreamsRule = "livestreams_rule"
	fieldRetentionDays   = "retention_days"
	fieldRetentionPreset = "retention_preset"
)

// hoursPerDayDuration converts a whole-day count into a duration.
const hoursPerDayDuration = hoursPerDay * time.Hour

// sourceFormValues holds the raw, user-entered strings so the form can be
// re-rendered with the exact values on a validation error (recognition over
// recall — never make the user re-type everything).
type sourceFormValues struct {
	Name            string
	URL             string
	CollectionType  string
	MediaProfileID  string
	CookieBehavior  string
	FrequencyHours  string
	CutoffWindow    string
	DownloadCutoff  string
	TitleFilter     string
	ShortsRule      string
	LivestreamsRule string
	// RetentionPreset is the dropdown selection: a day count, "custom" to use
	// RetentionDays, or empty to keep everything.
	RetentionPreset string
	RetentionDays   string
}

// fromSource maps a persisted source back onto raw form values so the edit form
// renders pre-filled with the user's existing choices. Zero/optional fields
// render as empty strings, matching their placeholders.
func fromSource(source domain.Source) sourceFormValues {
	return sourceFormValues{
		Name:            source.Name,
		URL:             source.URL,
		CollectionType:  string(source.CollectionType),
		MediaProfileID:  strconv.FormatInt(source.MediaProfileID, 10),
		CookieBehavior:  string(source.CookieBehavior),
		FrequencyHours:  strconv.Itoa(durationHours(source.IndexFrequency)),
		CutoffWindow:    cutoffWindowValue(source),
		DownloadCutoff:  cutoffString(source.DownloadCutoff),
		TitleFilter:     source.TitleFilterPattern,
		ShortsRule:      string(source.ShortsRule),
		LivestreamsRule: string(source.LivestreamsRule),
		RetentionPreset: retentionPresetValue(source.RetentionAfter),
		RetentionDays:   optionalDaysString(source.RetentionAfter),
	}
}

// retentionPresetOptions are the day counts the dropdown offers directly. Any
// other stored value falls back to the custom field so an existing source is
// never silently rounded to a preset.
var retentionPresetOptions = map[int]bool{30: true, 60: true, 90: true, 180: true, 365: true}

// retentionPresetValue derives the dropdown selection from a stored retention
// period: the day count when it matches an offered period, "custom" for any
// other non-zero value, or empty when retention is off.
func retentionPresetValue(retention time.Duration) string {
	if retention <= 0 {
		return ""
	}
	days := durationDays(retention)
	if retentionPresetOptions[days] {
		return strconv.Itoa(days)
	}
	return retentionPresetCustom
}

// cutoffWindowValue derives the "published within" dropdown selection from a
// source: the window in days when a rolling window is set, "custom" when a fixed
// date is set, or empty (all time) when neither is.
func cutoffWindowValue(source domain.Source) string {
	if source.CutoffWindow > 0 {
		return strconv.Itoa(durationDays(source.CutoffWindow))
	}
	if source.DownloadCutoff != nil {
		return cutoffWindowCustom
	}
	return ""
}

// cutoffString renders an optional cutoff time as YYYY-MM-DD, or empty when unset.
func cutoffString(cutoff *time.Time) string {
	if cutoff == nil {
		return ""
	}
	return formatDate(cutoff.UTC())
}

// optionalDaysString renders a retention duration as whole days, or empty when
// zero so the "keep forever" placeholder shows instead of a literal 0.
func optionalDaysString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return strconv.Itoa(durationDays(d))
}

// readFormValues extracts the raw submitted values from the request.
func readFormValues(r *http.Request) sourceFormValues {
	return sourceFormValues{
		Name:            r.PostFormValue(fieldName),
		URL:             r.PostFormValue(fieldURL),
		CollectionType:  r.PostFormValue(fieldCollectionType),
		MediaProfileID:  r.PostFormValue(fieldMediaProfileID),
		CookieBehavior:  r.PostFormValue(fieldCookieBehavior),
		FrequencyHours:  r.PostFormValue(fieldFrequencyHours),
		CutoffWindow:    r.PostFormValue(fieldCutoffWindow),
		DownloadCutoff:  r.PostFormValue(fieldDownloadCutoff),
		TitleFilter:     r.PostFormValue(fieldTitleFilter),
		ShortsRule:      r.PostFormValue(fieldShortsRule),
		LivestreamsRule: r.PostFormValue(fieldLivestreamsRule),
		RetentionPreset: r.PostFormValue(fieldRetentionPreset),
		RetentionDays:   r.PostFormValue(fieldRetentionDays),
	}
}

// toInput validates the raw values and assembles an AddSourceInput. It stops at
// the first problem and returns a user-facing message, so the form can show one
// clear, actionable error rather than a wall of them.
func (v sourceFormValues) toInput() (library.AddSourceInput, error) {
	builder := &inputBuilder{}
	builder.setName(v.Name)
	builder.setURL(v.URL)
	builder.setCollectionType(v.CollectionType)
	builder.setProfileID(v.MediaProfileID)
	builder.setCookieBehavior(v.CookieBehavior)
	builder.setFrequency(v.FrequencyHours)
	builder.setCutoff(v.CutoffWindow, v.DownloadCutoff)
	builder.setTitleFilter(v.TitleFilter)
	builder.setShortsRule(v.ShortsRule)
	builder.setLivestreamsRule(v.LivestreamsRule)
	builder.setRetention(v.RetentionPreset, v.RetentionDays)
	return builder.input, builder.err
}

// inputBuilder accumulates a validated AddSourceInput, short-circuiting on the
// first error so each setter stays tiny and single-purpose.
type inputBuilder struct {
	input library.AddSourceInput
	err   error
}

// setName accepts a blank name: when left empty, the service fills it in from the
// channel's own name on the first scan.
func (b *inputBuilder) setName(value string) {
	if b.err != nil {
		return
	}
	b.input.Name = strings.TrimSpace(value)
}

func (b *inputBuilder) setURL(value string) {
	if b.err != nil {
		return
	}
	url := strings.TrimSpace(value)
	if url == "" {
		b.err = errors.New("Please enter the channel or playlist URL.")
		return
	}
	b.input.URL = url
}

func (b *inputBuilder) setCollectionType(value string) {
	if b.err != nil {
		return
	}
	collectionType := domain.CollectionType(value)
	if !collectionType.IsValid() {
		b.err = errors.New("Please choose whether this is a channel or a playlist.")
		return
	}
	b.input.CollectionType = collectionType
}

func (b *inputBuilder) setProfileID(value string) {
	if b.err != nil {
		return
	}
	id := atoiOrZero(value)
	if id <= 0 {
		b.err = errors.New("Please pick a media profile.")
		return
	}
	b.input.MediaProfileID = int64(id)
}

func (b *inputBuilder) setCookieBehavior(value string) {
	if b.err != nil {
		return
	}
	behavior := domain.CookieBehavior(value)
	if !behavior.IsValid() {
		b.err = errors.New("Please choose a cookie behavior.")
		return
	}
	b.input.CookieBehavior = behavior
}

func (b *inputBuilder) setFrequency(value string) {
	if b.err != nil {
		return
	}
	hours := atoiOrZero(value)
	if hours <= 0 {
		b.err = errors.New("Please choose how often to check for new videos.")
		return
	}
	b.input.IndexFrequency = time.Duration(hours) * time.Hour
}

// cutoffWindowCustom is the dropdown value that means "use the explicit date"
// rather than a rolling window.
const cutoffWindowCustom = "custom"

// setCutoff interprets the "published within" dropdown: a positive number selects
// a rolling window in days; "custom" uses the explicit date field; empty (or 0)
// means no cutoff at all.
func (b *inputBuilder) setCutoff(windowValue, dateValue string) {
	if b.err != nil {
		return
	}
	switch window := strings.TrimSpace(windowValue); window {
	case "", "0":
		return
	case cutoffWindowCustom:
		b.setCustomCutoffDate(dateValue)
	default:
		days := atoiOrZero(window)
		if days <= 0 {
			b.err = errors.New("Please choose how far back to download.")
			return
		}
		b.input.CutoffWindow = time.Duration(days) * hoursPerDayDuration
	}
}

// setCustomCutoffDate parses the explicit YYYY-MM-DD cutoff date. A blank date
// (custom selected but nothing entered) simply means no cutoff.
func (b *inputBuilder) setCustomCutoffDate(value string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	cutoff, err := time.Parse(dateLayout, trimmed)
	if err != nil {
		b.err = errors.New("The custom date must be a valid date (YYYY-MM-DD).")
		return
	}
	b.input.DownloadCutoff = &cutoff
}

func (b *inputBuilder) setTitleFilter(value string) {
	if b.err != nil {
		return
	}
	pattern := strings.TrimSpace(value)
	if pattern == "" {
		return
	}
	if _, err := regexp.Compile(pattern); err != nil {
		b.err = errors.New("That title filter isn't a valid pattern. Leave it blank to keep every video.")
		return
	}
	b.input.TitleFilterPattern = pattern
}

func (b *inputBuilder) setShortsRule(value string) {
	if b.err != nil {
		return
	}
	rule := domain.InclusionRule(value)
	if !rule.IsValid() {
		b.err = errors.New("Please choose how to handle Shorts.")
		return
	}
	b.input.ShortsRule = rule
}

func (b *inputBuilder) setLivestreamsRule(value string) {
	if b.err != nil {
		return
	}
	rule := domain.InclusionRule(value)
	if !rule.IsValid() {
		b.err = errors.New("Please choose how to handle livestreams.")
		return
	}
	b.input.LivestreamsRule = rule
}

// retentionPresetCustom is the dropdown value that means "use the typed number
// of days" rather than one of the offered periods.
const retentionPresetCustom = "custom"

// setRetention interprets the "delete downloads older than" dropdown, mirroring
// how the cutoff dropdown works: a positive number selects that many days,
// "custom" takes the typed value, and empty means keep everything.
func (b *inputBuilder) setRetention(preset, days string) {
	if b.err != nil {
		return
	}
	value := preset
	if strings.TrimSpace(preset) == retentionPresetCustom {
		value = days
	}
	if strings.TrimSpace(value) == "" {
		return
	}

	parsed := atoiOrZero(value)
	if parsed < 0 {
		b.err = errors.New("Retention days can't be negative.")
		return
	}
	b.input.RetentionAfter = time.Duration(parsed) * hoursPerDayDuration
}
