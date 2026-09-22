package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
)

// errNoProfiles reports an archive with no media profile to download with,
// which cannot happen on a normally seeded install.
var errNoProfiles = errors.New("no media profile exists; create one in the web UI first")

// defaultSearchLimit caps search results when the caller does not say.
const defaultSearchLimit = 25

// jobLogLimit caps how many captured log lines get_job returns.
const jobLogLimit = 100

// dateLayout renders dates in tool output.
const dateLayout = "2006-01-02"

// sourceSummary is one tracked source as tool output.
type sourceSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url,omitempty"`
	Type        string `json:"type" jsonschema:"channel, playlist, or singles (the one-off bucket)"`
	Enabled     bool   `json:"enabled" jsonschema:"false means scanning is paused"`
	Videos      int    `json:"videos_on_disk"`
	SizeBytes   int64  `json:"size_bytes"`
	TitleFilter string `json:"title_filter,omitempty" jsonschema:"regex; only matching titles are downloaded"`
}

func (t *toolset) listSources(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, []sourceSummary, error) {
	sources, err := t.deps.Sources.ListSources(ctx)
	if err != nil {
		return toolError[[]sourceSummary](err)
	}
	stats, err := t.deps.Library.SourceStats(ctx)
	if err != nil {
		return toolError[[]sourceSummary](err)
	}

	out := make([]sourceSummary, 0, len(sources))
	for _, source := range sources {
		out = append(out, sourceSummary{
			ID:          source.ID,
			Name:        source.Name,
			URL:         source.URL,
			Type:        string(source.CollectionType),
			Enabled:     source.Enabled,
			Videos:      stats[source.ID].Files,
			SizeBytes:   stats[source.ID].Bytes,
			TitleFilter: source.TitleFilterPattern,
		})
	}
	return nil, out, nil
}

// profileSummary is one media profile as tool output.
type profileSummary struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind" jsonschema:"video or audio"`
	MetadataFormat string `json:"metadata_format" jsonschema:"episode or movie sidecar layout"`
	DownloadDir    string `json:"download_dir,omitempty" jsonschema:"set when this profile routes downloads into a separate library"`
}

func (t *toolset) listProfiles(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, []profileSummary, error) {
	profiles, err := t.deps.Profiles.ListProfiles(ctx)
	if err != nil {
		return toolError[[]profileSummary](err)
	}
	out := make([]profileSummary, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, profileSummary{
			ID:             profile.ID,
			Name:           profile.Name,
			Kind:           string(profile.Kind),
			MetadataFormat: string(profile.MetadataFormat),
			DownloadDir:    profile.DownloadDir,
		})
	}
	return nil, out, nil
}

// searchLibraryInput narrows a library search; every field is optional.
type searchLibraryInput struct {
	Query    string `json:"query,omitempty" jsonschema:"case-insensitive text to find in video titles"`
	Status   string `json:"status,omitempty" jsonschema:"downloaded, downloading, queued, failed, unavailable, skipped, or deleted"`
	SourceID int64  `json:"source_id,omitempty" jsonschema:"limit to one source's videos"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max results, default 25"`
}

// mediaSummary is one archived video as tool output.
type mediaSummary struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	Published string `json:"published,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	FilePath  string `json:"file_path,omitempty"`
	WatchURL  string `json:"watch_url,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

func (t *toolset) searchLibrary(ctx context.Context, _ *mcp.CallToolRequest, in searchLibraryInput) (*mcp.CallToolResult, []mediaSummary, error) {
	status, err := mediaStatusFilter(in.Status)
	if err != nil {
		return toolError[[]mediaSummary](err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	items, err := t.deps.Library.ListMedia(ctx, library.MediaQuery{
		Status:   status,
		SourceID: in.SourceID,
		Search:   in.Query,
		Limit:    limit,
	})
	if err != nil {
		return toolError[[]mediaSummary](err)
	}

	out := make([]mediaSummary, 0, len(items))
	for _, item := range items {
		out = append(out, mediaSummary{
			ID:        item.Media.ID,
			Title:     item.Media.Metadata.Title,
			Source:    item.SourceName,
			Status:    string(item.Media.Status),
			Published: formatDate(item.Media.Metadata.UploadDate),
			SizeBytes: item.Media.FileSize,
			FilePath:  item.Media.FilePath,
			WatchURL:  item.Media.WatchURL(),
			LastError: item.Media.LastError,
		})
	}
	return nil, out, nil
}

// queueStatusOutput summarises the background queue.
type queueStatusOutput struct {
	Running   int          `json:"running"`
	Queued    int          `json:"queued"`
	Failed    int          `json:"failed"`
	Succeeded int          `json:"succeeded"`
	Active    []jobSummary `json:"active_jobs" jsonschema:"the jobs currently running or next in line"`
}

// jobSummary is one queue entry as tool output.
type jobSummary struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Source    string `json:"source,omitempty"`
	Title     string `json:"title,omitempty"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// activeJobsLimit caps how many in-flight jobs queue_status lists.
const activeJobsLimit = 15

func (t *toolset) queueStatus(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, queueStatusOutput, error) {
	counts, err := t.deps.Jobs.CountsByStatus(ctx)
	if err != nil {
		return toolError[queueStatusOutput](err)
	}
	active, err := t.deps.Jobs.ListJobs(ctx, library.JobFilter{Limit: activeJobsLimit})
	if err != nil {
		return toolError[queueStatusOutput](err)
	}

	out := queueStatusOutput{
		Running:   counts[jobs.StatusRunning],
		Queued:    counts[jobs.StatusPending],
		Failed:    counts[jobs.StatusFailed],
		Succeeded: counts[jobs.StatusSucceeded],
	}
	for _, item := range active {
		if item.Task.Status != jobs.StatusRunning && item.Task.Status != jobs.StatusPending {
			continue // the unfiltered listing puts live work first; stop at history
		}
		out.Active = append(out.Active, summarizeJob(item))
	}
	return nil, out, nil
}

// getJobInput names the job to inspect.
type getJobInput struct {
	JobID int64 `json:"job_id"`
}

// jobDetailOutput is one job with the log lines it produced.
type jobDetailOutput struct {
	jobSummary
	MediaID  int64    `json:"media_id,omitempty"`
	SourceID int64    `json:"source_id,omitempty"`
	WatchURL string   `json:"watch_url,omitempty"`
	Logs     []string `json:"logs" jsonschema:"the job's captured log lines, oldest first, including yt-dlp output"`
}

func (t *toolset) getJob(ctx context.Context, _ *mcp.CallToolRequest, in getJobInput) (*mcp.CallToolResult, jobDetailOutput, error) {
	job, err := t.deps.Jobs.GetJob(ctx, in.JobID)
	if err != nil {
		return toolError[jobDetailOutput](fmt.Errorf("no job %d: %w", in.JobID, err))
	}

	out := jobDetailOutput{
		jobSummary: summarizeJob(job),
		WatchURL:   domain.WatchURL(job.MediaExternalID),
	}
	if job.MediaID != nil {
		out.MediaID = *job.MediaID
	}
	if job.SourceID != nil {
		out.SourceID = *job.SourceID
	}
	for _, record := range t.deps.Logs.ForTask(in.JobID, jobLogLimit) {
		out.Logs = append(out.Logs, record.Time.Format(time.TimeOnly)+" "+record.Level+" "+record.Message)
	}
	return nil, out, nil
}

// saveVideoInput is one pasted video to download.
type saveVideoInput struct {
	URL       string `json:"url" jsonschema:"a single-video link: watch, youtu.be, or Shorts URL"`
	ProfileID int64  `json:"profile_id,omitempty" jsonschema:"media profile to download with; omit for the default. A profile with its own download folder routes the file there (e.g. a Movies profile into the Plex movie library)"`
}

// saveVideoOutput reports where the queued video can be watched.
type saveVideoOutput struct {
	MediaID     int64  `json:"media_id"`
	LibraryPath string `json:"library_path" jsonschema:"the video's page in the web UI"`
}

func (t *toolset) saveVideo(ctx context.Context, _ *mcp.CallToolRequest, in saveVideoInput) (*mcp.CallToolResult, saveVideoOutput, error) {
	id, err := t.deps.Sources.DownloadVideo(ctx, in.URL, in.ProfileID)
	if err != nil {
		return toolError[saveVideoOutput](err)
	}
	return nil, saveVideoOutput{
		MediaID:     id,
		LibraryPath: fmt.Sprintf("/library/%d", id),
	}, nil
}

// addSourceInput describes a channel or playlist to start tracking.
type addSourceInput struct {
	URL         string `json:"url" jsonschema:"the channel or playlist link"`
	Type        string `json:"type,omitempty" jsonschema:"channel or playlist; default channel"`
	Name        string `json:"name,omitempty" jsonschema:"display name; omitted, it is filled from the channel on first scan"`
	ProfileID   int64  `json:"profile_id,omitempty" jsonschema:"media profile; omit for the default"`
	CutoffDays  int    `json:"cutoff_days,omitempty" jsonschema:"only download videos published within this many days (rolling); omit for the whole back catalog"`
	TitleFilter string `json:"title_filter,omitempty" jsonschema:"Go regular expression; only videos whose title matches are downloaded (use (?i) for case-insensitive)"`
}

// addSourceOutput reports the created source.
type addSourceOutput struct {
	SourceID   int64  `json:"source_id"`
	Name       string `json:"name,omitempty"`
	SourcePath string `json:"source_path" jsonschema:"the source's page in the web UI"`
}

// defaultIndexFrequency matches the web form's first re-scan option.
const defaultIndexFrequency = 6 * time.Hour

func (t *toolset) addSource(ctx context.Context, _ *mcp.CallToolRequest, in addSourceInput) (*mcp.CallToolResult, addSourceOutput, error) {
	collection, err := collectionTypeFor(in.Type)
	if err != nil {
		return toolError[addSourceOutput](err)
	}
	profileID := in.ProfileID
	if profileID == 0 {
		profileID, err = t.firstProfileID(ctx)
		if err != nil {
			return toolError[addSourceOutput](err)
		}
	}

	input := library.AddSourceInput{
		Name:               in.Name,
		URL:                in.URL,
		CollectionType:     collection,
		MediaProfileID:     profileID,
		IndexFrequency:     defaultIndexFrequency,
		CookieBehavior:     domain.CookieWhenNeeded,
		ShortsRule:         domain.InclusionExclude,
		LivestreamsRule:    domain.InclusionExclude,
		CutoffWindow:       time.Duration(in.CutoffDays) * 24 * time.Hour,
		TitleFilterPattern: in.TitleFilter,
	}
	source, err := t.deps.Sources.AddSource(ctx, input)
	if err != nil {
		return toolError[addSourceOutput](err)
	}
	return nil, addSourceOutput{
		SourceID:   source.ID,
		Name:       source.Name,
		SourcePath: fmt.Sprintf("/sources/%d", source.ID),
	}, nil
}

// scanNowInput names the source to scan.
type scanNowInput struct {
	SourceID int64 `json:"source_id"`
}

// scanNowOutput says whether a scan was started or one was already underway.
type scanNowOutput struct {
	Result string `json:"result"`
}

func (t *toolset) scanNow(ctx context.Context, _ *mcp.CallToolRequest, in scanNowInput) (*mcp.CallToolResult, scanNowOutput, error) {
	types, err := t.deps.Jobs.UnfinishedTypesForSource(ctx, in.SourceID)
	if err != nil {
		return toolError[scanNowOutput](err)
	}
	if types[jobs.TaskIndexSource] {
		return nil, scanNowOutput{Result: "a scan is already queued or running for this source"}, nil
	}
	if err := t.deps.Sources.RequestScan(ctx, in.SourceID); err != nil {
		return toolError[scanNowOutput](err)
	}
	return nil, scanNowOutput{Result: "scan queued"}, nil
}

// retryFailedInput names the source whose failures to requeue.
type retryFailedInput struct {
	SourceID int64 `json:"source_id"`
}

// retryFailedOutput reports how many downloads were requeued.
type retryFailedOutput struct {
	Requeued int `json:"requeued"`
}

func (t *toolset) retryFailed(ctx context.Context, _ *mcp.CallToolRequest, in retryFailedInput) (*mcp.CallToolResult, retryFailedOutput, error) {
	count, err := t.deps.Library.RetryAllFailed(ctx, in.SourceID)
	if err != nil {
		return toolError[retryFailedOutput](err)
	}
	return nil, retryFailedOutput{Requeued: count}, nil
}

// deleteSourceInput names the source to remove and how much of it goes.
type deleteSourceInput struct {
	SourceID int64 `json:"source_id"`
	// DeleteFiles must be asked for explicitly: losing the records is
	// recoverable, losing the media is not.
	DeleteFiles bool `json:"delete_files,omitempty" jsonschema:"also delete the downloaded video files from disk — IRREVERSIBLE; false (the default) keeps the files, and re-adding the source adopts them again"`
}

// deleteSourceOutput states what was removed.
type deleteSourceOutput struct {
	Result string `json:"result"`
}

func (t *toolset) deleteSource(ctx context.Context, _ *mcp.CallToolRequest, in deleteSourceInput) (*mcp.CallToolResult, deleteSourceOutput, error) {
	source, err := t.deps.Sources.GetSource(ctx, in.SourceID)
	if err != nil {
		return toolError[deleteSourceOutput](fmt.Errorf("no source %d: %w", in.SourceID, err))
	}
	stats, err := t.deps.Library.SourceStats(ctx)
	if err != nil {
		return toolError[deleteSourceOutput](err)
	}

	opts := library.DeleteSourceOptions{DeleteFiles: in.DeleteFiles}
	if err := t.deps.Sources.DeleteSource(ctx, in.SourceID, opts); err != nil {
		return toolError[deleteSourceOutput](err)
	}

	outcome := fmt.Sprintf("deleted source %q; its %d downloaded file(s) were kept on disk and will be re-adopted if the source is re-added",
		source.Name, stats[in.SourceID].Files)
	if in.DeleteFiles {
		outcome = fmt.Sprintf("deleted source %q and its %d downloaded file(s) (%d bytes) from disk",
			source.Name, stats[in.SourceID].Files, stats[in.SourceID].Bytes)
	}
	return nil, deleteSourceOutput{Result: outcome}, nil
}

// deleteMediaInput lists the videos to delete.
type deleteMediaInput struct {
	MediaIDs []int64 `json:"media_ids" jsonschema:"the media ids to delete; find them with search_library"`
}

// deleteMediaResult is the outcome for one requested id.
type deleteMediaResult struct {
	MediaID int64  `json:"media_id"`
	Result  string `json:"result" jsonschema:"deleted, or the reason it was not"`
}

// deleteMediaOutput reports every requested id's outcome.
type deleteMediaOutput struct {
	Deleted int                 `json:"deleted"`
	Results []deleteMediaResult `json:"results"`
}

func (t *toolset) deleteMedia(ctx context.Context, _ *mcp.CallToolRequest, in deleteMediaInput) (*mcp.CallToolResult, deleteMediaOutput, error) {
	if len(in.MediaIDs) == 0 {
		return toolError[deleteMediaOutput](errors.New("media_ids must name at least one video"))
	}

	out := deleteMediaOutput{}
	for _, id := range in.MediaIDs {
		// One refusal must not abandon the rest of the list undone; each id
		// reports its own outcome.
		if err := t.deps.Media.DeleteMedia(ctx, id); err != nil {
			out.Results = append(out.Results, deleteMediaResult{MediaID: id, Result: err.Error()})
			continue
		}
		out.Deleted++
		out.Results = append(out.Results, deleteMediaResult{MediaID: id, Result: "deleted"})
	}
	return nil, out, nil
}

// summarizeJob maps a queue entry onto tool output.
func summarizeJob(item library.JobListItem) jobSummary {
	return jobSummary{
		ID:        item.Task.ID,
		Type:      string(item.Task.Type),
		Status:    string(item.Task.Status),
		Source:    item.SourceName,
		Title:     item.MediaTitle,
		Attempts:  item.Task.Attempts,
		LastError: item.Task.LastError,
		UpdatedAt: item.Task.UpdatedAt.Format(time.RFC3339),
	}
}

// mediaStatusFilter maps a tool status string onto the domain status, with ""
// meaning all.
func mediaStatusFilter(value string) (domain.MediaStatus, error) {
	switch value {
	case "":
		return "", nil
	case "downloaded":
		return domain.MediaDownloaded, nil
	case "downloading":
		return domain.MediaDownloading, nil
	case "queued":
		return domain.MediaPending, nil
	case "failed":
		return domain.MediaFailed, nil
	case "unavailable":
		return domain.MediaUnavailable, nil
	case "skipped":
		return domain.MediaSkipped, nil
	case "deleted":
		return domain.MediaDeleted, nil
	default:
		return "", fmt.Errorf("unknown status %q: use downloaded, downloading, queued, failed, unavailable, skipped, or deleted", value)
	}
}

// collectionTypeFor maps a tool type string onto the domain collection type,
// defaulting to channel.
func collectionTypeFor(value string) (domain.CollectionType, error) {
	switch value {
	case "", "channel":
		return domain.CollectionChannel, nil
	case "playlist":
		return domain.CollectionPlaylist, nil
	default:
		return "", fmt.Errorf("unknown source type %q: use channel or playlist", value)
	}
}

// formatDate renders a date for tool output, or empty when unknown.
func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(dateLayout)
}
