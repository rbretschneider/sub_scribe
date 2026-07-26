package web

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"strconv"
	"strings"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

// dateLayout is the HTML date-input format used for cutoff parsing and display.
const dateLayout = "2006-01-02"

// dateTimeLayout is the timestamp format for queue and detail screens: precise
// to the second, because job timings are what the user is reading them for.
const dateTimeLayout = "2006-01-02 15:04:05"

// emptyValue stands in for a missing timestamp so tables never show a blank cell.
const emptyValue = "—"

// hoursPerDay converts an hour count into whole days for display.
const hoursPerDay = 24

// durationHours renders a duration as its whole number of hours, for prefilling
// the frequency select when editing.
func durationHours(d time.Duration) int {
	return int(d.Hours())
}

// durationDays renders a duration as its whole number of days.
func durationDays(d time.Duration) int {
	return int(d.Hours()) / hoursPerDay
}

// selectedAttr returns the HTML selected attribute when a select option matches
// the current value, so forms round-trip the user's choice.
func selectedAttr(current, option string) string {
	if current == option {
		return "selected"
	}
	return ""
}

// formatDate renders a time as YYYY-MM-DD, or an empty string for the zero time.
func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(dateLayout)
}

// humanizeEnum turns a snake_case enum value into Title Case words for display,
// e.g. "when_needed" becomes "When Needed".
func humanizeEnum(value string) string {
	words := strings.Split(value, "_")
	for i, word := range words {
		if word == "" {
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

// atoiOrZero parses an integer, returning zero when the input is empty or invalid;
// callers that require a value validate separately.
func atoiOrZero(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}

// durationClock formats a duration as a media timestamp: m:ss, or h:mm:ss past
// an hour. Zero renders empty so a missing duration shows nothing.
func durationClock(d time.Duration) string {
	total := int(d.Seconds())
	if total <= 0 {
		return ""
	}
	hours, minutes, seconds := total/3600, (total%3600)/60, total%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

// byteUnits are the successively larger units for humanizing byte counts.
var byteUnits = []string{"KB", "MB", "GB", "TB", "PB"}

// bytesHuman renders a byte count as a human-readable size (e.g. "214 MB").
func bytesHuman(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit && exp < len(byteUnits)-1; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), byteUnits[exp])
}

// statusClass maps a media status to its CSS status-pill modifier.
func statusClass(status domain.MediaStatus) string {
	switch status {
	case domain.MediaDownloaded:
		return "done"
	case domain.MediaDownloading:
		return "downloading"
	case domain.MediaPending:
		return "queued"
	case domain.MediaFailed:
		return "failed"
	default:
		return "skipped"
	}
}

// statusLabel maps a media status to its human label.
func statusLabel(status domain.MediaStatus) string {
	switch status {
	case domain.MediaDownloaded:
		return "Downloaded"
	case domain.MediaDownloading:
		return "Downloading"
	case domain.MediaPending:
		return "Queued"
	case domain.MediaFailed:
		return "Failed"
	case domain.MediaSkipped:
		return "Skipped"
	case domain.MediaUnavailable:
		return "Unavailable"
	default:
		return string(status)
	}
}

// jobStatusClass maps a queue status to its CSS status-pill modifier. Queue
// statuses get their own mapping rather than reusing the media one: they are a
// different vocabulary, and conflating them would break as either evolves.
func jobStatusClass(status jobs.TaskStatus) string {
	switch status {
	case jobs.StatusRunning:
		return "downloading"
	case jobs.StatusPending:
		return "queued"
	case jobs.StatusSucceeded:
		return "done"
	case jobs.StatusFailed:
		return "failed"
	default:
		return "skipped"
	}
}

// jobStatusLabel maps a queue status to its human label.
func jobStatusLabel(status jobs.TaskStatus) string {
	switch status {
	case jobs.StatusRunning:
		return "Running"
	case jobs.StatusPending:
		return "Queued"
	case jobs.StatusSucceeded:
		return "Done"
	case jobs.StatusFailed:
		return "Failed"
	default:
		return string(status)
	}
}

// cutoffWindowLabels names each rolling "published within" window in the same
// words the source form uses, so the setting reads identically wherever it is
// shown.
var cutoffWindowLabels = map[int]string{
	7:   "The last 7 days",
	30:  "The last 30 days",
	90:  "The last 3 months",
	180: "The last 6 months",
	365: "The last year",
	730: "The last 2 years",
}

// cutoffWindowLabel describes a source's rolling download window. An unset
// window is "All time", and a window with no preset name falls back to its day
// count, so a value set outside the form is still described accurately.
func cutoffWindowLabel(window time.Duration) string {
	if window <= 0 {
		return "All time"
	}
	days := int(window.Hours()) / hoursPerDay
	if label, ok := cutoffWindowLabels[days]; ok {
		return label
	}
	return fmt.Sprintf("The last %d days", days)
}

// jobTypeLabel renders a task type as readable words, e.g. "Download Media".
func jobTypeLabel(taskType jobs.TaskType) string {
	return humanizeEnum(string(taskType))
}

// formatDateTime renders a timestamp for display, accepting either a time or a
// pointer to one so optional fields need no template gymnastics. The zero time
// and a nil pointer both render as a dash, which reads better in a table than a
// blank cell.
func formatDateTime(value any) string {
	var t time.Time
	switch typed := value.(type) {
	case time.Time:
		t = typed
	case *time.Time:
		if typed == nil {
			return emptyValue
		}
		t = *typed
	default:
		return emptyValue
	}
	if t.IsZero() {
		return emptyValue
	}
	return t.Format(dateTimeLayout)
}

// thumbPairs is a curated set of dark gradient stops used as stable placeholder
// thumbnails until real thumbnail images are wired in.
var thumbPairs = [][2]string{
	{"#3a2b52", "#1b1430"}, {"#123a3a", "#0c2226"}, {"#40331a", "#221a0d"},
	{"#1f2b47", "#0f1626"}, {"#3a3016", "#1e1809"}, {"#3d1f2a", "#1f0f16"},
	{"#2b3a52", "#141f2e"}, {"#123033", "#0a1c1e"}, {"#33244a", "#1a1230"},
	{"#22384f", "#111e2b"},
}

// logTimeFormat renders a log timestamp as a compact wall-clock time.
func logTimeFormat(t time.Time) string {
	return t.Local().Format("Jan 2 15:04:05")
}

// thumbGradient derives a stable gradient from a seed (the video id) so each
// video keeps the same placeholder thumbnail across renders.
func thumbGradient(seed string) template.CSS {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(seed))
	pair := thumbPairs[int(hasher.Sum32())%len(thumbPairs)]
	return template.CSS(fmt.Sprintf("linear-gradient(135deg,%s,%s)", pair[0], pair[1]))
}
