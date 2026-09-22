// Package mcpserver exposes sub_scribe's application core as an MCP (Model
// Context Protocol) server, so an AI session can manage the archive with tool
// calls — add a source, save a one-off video, search the library, inspect the
// queue — instead of driving the web UI.
//
// It is a peer of the web layer: a thin adapter over the same library service
// interfaces, mounted on the same HTTP server, with its own bearer-token gate
// because MCP clients cannot complete a browser login. Deliberately absent
// from v1 is anything destructive — there is no delete tool.
package mcpserver

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/library"
)

// serverName and serverVersion identify this implementation to MCP clients.
const (
	serverName    = "sub_scribe"
	serverVersion = "1.0.0"
)

// LogReader supplies the captured log lines for a job, mirroring the web
// layer's port of the same name. It is declared here rather than imported from
// the web package because the two are peer adapters, not layers of each other.
type LogReader interface {
	// ForTask returns the records a single job produced, oldest first.
	ForTask(taskID int64, limit int) []applog.Record
}

// Deps are the collaborators the MCP tools call into — the same service
// interfaces the web layer depends on.
type Deps struct {
	Sources  library.SourceService
	Profiles library.ProfileService
	Library  library.LibraryReader
	Media    library.MediaService
	Jobs     library.JobReader
	Logs     LogReader
}

// Handler returns the /mcp endpoint: a streamable-HTTP MCP server behind a
// bearer-token check. Sessions are stateless — every tool call is
// self-contained, so nothing is lost across restarts and no session state can
// leak between clients.
func Handler(deps Deps, token string) http.Handler {
	server := newServer(deps)
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	return requireBearer(token, streamable)
}

// newServer builds the MCP server and registers every tool.
func newServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:        serverName,
		Title:       "sub_scribe",
		Description: "Self-hosted YouTube archiver that syncs to Plex and Jellyfin",
		Version:     serverVersion,
	}, nil)
	tools := &toolset{deps: deps}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_sources",
		Description: "List every tracked source (channels, playlists, and the one-off buckets) with what each has on disk.",
	}, tools.listSources)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_profiles",
		Description: "List the media profiles — the how and where of downloading. Profile ids are used by save_video and add_source.",
	}, tools.listProfiles)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_library",
		Description: "Search the archived videos by title, status, and source. Returns newest first.",
	}, tools.searchLibrary)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "queue_status",
		Description: "The background queue at a glance: counts by status and the jobs currently running or waiting.",
	}, tools.queueStatus)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_job",
		Description: "Everything known about one background job, including its full error and the log lines it produced (yt-dlp output included).",
	}, tools.getJob)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "save_video",
		Description: "Queue one video for immediate download from a pasted link (watch, youtu.be, or Shorts URL). Optionally pick a media profile to route it — e.g. a Movies profile files it into the Plex movie library.",
	}, tools.saveVideo)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_source",
		Description: "Start tracking a YouTube channel or playlist: it is scanned immediately and re-scanned on a schedule, and matching videos are downloaded.",
	}, tools.addSource)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "scan_now",
		Description: "Trigger an immediate out-of-schedule scan of a source. A no-op if a scan is already queued or running.",
	}, tools.scanNow)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "retry_failed",
		Description: "Requeue every failed download for a source in one go, returning how many were requeued.",
	}, tools.retryFailed)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_source",
		Description: "Stop tracking a source and forget everything known about it. By default the downloaded video files STAY on disk (reversible: re-adding the source adopts them); set delete_files to also remove the files, which cannot be undone. Confirm with the user before calling this with delete_files.",
	}, tools.deleteSource)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_media",
		Description: "Surgically delete individual videos by media id: each one's file and sidecars are removed from disk (IRREVERSIBLE) and the record is tombstoned so a rescan never re-downloads it. Use search_library to find ids first, and confirm the list with the user before calling. Items currently queued or downloading are refused.",
	}, tools.deleteMedia)

	return server
}

// toolset carries the dependencies into the tool handlers.
type toolset struct {
	deps Deps
}

// toolError wraps a failure as a tool-level error result, so the calling model
// sees what went wrong instead of a bare protocol failure.
func toolError[Out any](err error) (*mcp.CallToolResult, Out, error) {
	var zero Out
	return nil, zero, err
}

// firstProfileID resolves the default profile — the first by id, matching the
// web UI's "first profile is the default" contract.
func (t *toolset) firstProfileID(ctx context.Context) (int64, error) {
	profiles, err := t.deps.Profiles.ListProfiles(ctx)
	if err != nil {
		return 0, err
	}
	if len(profiles) == 0 {
		return 0, errNoProfiles
	}
	return profiles[0].ID, nil
}
