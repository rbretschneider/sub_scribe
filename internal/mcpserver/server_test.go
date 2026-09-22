package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
)

// fakeSources records the calls the tools make.
type fakeSources struct {
	sources        []domain.Source
	added          library.AddSourceInput
	scanned        []int64
	deleted        []int64
	deletedFiles   []bool
	downloadedURLs []string
	downloadedPID  int64
}

func (f *fakeSources) AddSource(_ context.Context, input library.AddSourceInput) (domain.Source, error) {
	f.added = input
	return domain.Source{ID: 9, Name: input.Name}, nil
}
func (f *fakeSources) GetSource(_ context.Context, id int64) (domain.Source, error) {
	for _, s := range f.sources {
		if s.ID == id {
			return s, nil
		}
	}
	return domain.Source{ID: id, Name: "Some Source"}, nil
}
func (f *fakeSources) ListSources(context.Context) ([]domain.Source, error) { return f.sources, nil }
func (f *fakeSources) UpdateSource(_ context.Context, id int64, input library.AddSourceInput) (domain.Source, error) {
	return domain.Source{ID: id}, nil
}
func (f *fakeSources) DeleteSource(_ context.Context, id int64, opts library.DeleteSourceOptions) error {
	f.deleted = append(f.deleted, id)
	f.deletedFiles = append(f.deletedFiles, opts.DeleteFiles)
	return nil
}
func (f *fakeSources) SetSourceEnabled(context.Context, int64, bool) error { return nil }
func (f *fakeSources) RequestScan(_ context.Context, id int64) error {
	f.scanned = append(f.scanned, id)
	return nil
}
func (f *fakeSources) RequestRename(context.Context, int64) error { return nil }
func (f *fakeSources) DownloadVideo(_ context.Context, rawURL string, profileID int64) (int64, error) {
	f.downloadedURLs = append(f.downloadedURLs, rawURL)
	f.downloadedPID = profileID
	return 42, nil
}

// fakeProfiles serves a fixed profile list.
type fakeProfiles struct{ profiles []domain.MediaProfile }

func (f *fakeProfiles) CreateProfile(_ context.Context, p domain.MediaProfile) (domain.MediaProfile, error) {
	return p, nil
}
func (f *fakeProfiles) GetProfile(_ context.Context, id int64) (domain.MediaProfile, error) {
	return domain.MediaProfile{ID: id}, nil
}
func (f *fakeProfiles) ListProfiles(context.Context) ([]domain.MediaProfile, error) {
	return f.profiles, nil
}
func (f *fakeProfiles) UpdateProfile(context.Context, domain.MediaProfile) error { return nil }
func (f *fakeProfiles) DeleteProfile(context.Context, int64) error               { return nil }

// fakeLibrary serves canned read views and records queries.
type fakeLibrary struct {
	media      []library.MediaListItem
	stats      map[int64]library.SourceStats
	lastQuery  library.MediaQuery
	retryCount int
}

func (f *fakeLibrary) Overview(context.Context) (library.Overview, error) {
	return library.Overview{}, nil
}
func (f *fakeLibrary) ListMedia(_ context.Context, q library.MediaQuery) ([]library.MediaListItem, error) {
	f.lastQuery = q
	return f.media, nil
}
func (f *fakeLibrary) GetMedia(context.Context, int64) (library.MediaListItem, error) {
	return library.MediaListItem{}, nil
}
func (f *fakeLibrary) SourceStats(context.Context) (map[int64]library.SourceStats, error) {
	return f.stats, nil
}
func (f *fakeLibrary) RetryAllFailed(context.Context, int64) (int, error) { return f.retryCount, nil }

// fakeJobs serves queue reads.
type fakeJobs struct {
	items      []library.JobListItem
	counts     map[jobs.TaskStatus]int
	unfinished map[jobs.TaskType]bool
}

func (f *fakeJobs) ListJobs(context.Context, library.JobFilter) ([]library.JobListItem, error) {
	return f.items, nil
}
func (f *fakeJobs) GetJob(_ context.Context, id int64) (library.JobListItem, error) {
	for _, item := range f.items {
		if item.Task.ID == id {
			return item, nil
		}
	}
	return library.JobListItem{Task: jobs.Task{ID: id}}, nil
}
func (f *fakeJobs) CountsByStatus(context.Context) (map[jobs.TaskStatus]int, error) {
	return f.counts, nil
}
func (f *fakeJobs) UnfinishedTypesForSource(context.Context, int64) (map[jobs.TaskType]bool, error) {
	return f.unfinished, nil
}

// harness bundles the fakes with a connected MCP client session.
type harness struct {
	sources  *fakeSources
	profiles *fakeProfiles
	library  *fakeLibrary
	jobs     *fakeJobs
	session  *mcp.ClientSession
}

// newHarness connects a real MCP client to the server over in-memory
// transports, so tests exercise the full tool-call path: schema validation,
// dispatch, and structured output.
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		sources:  &fakeSources{},
		profiles: &fakeProfiles{profiles: []domain.MediaProfile{{ID: 1, Name: "Default"}}},
		library:  &fakeLibrary{stats: map[int64]library.SourceStats{}},
		jobs:     &fakeJobs{counts: map[jobs.TaskStatus]int{}},
	}
	server := newServer(Deps{
		Sources:  h.sources,
		Profiles: h.profiles,
		Library:  h.library,
		Jobs:     h.jobs,
		Logs:     applog.NewBuffer(0),
	})

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	h.session = session
	return h
}

// call invokes a tool and returns its structured output re-marshalled as JSON.
func (h *harness) call(t *testing.T, tool string, args map[string]any) string {
	t.Helper()
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: tool, Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("CallTool(%s) returned a tool error: %+v", tool, res.Content)
	}
	out, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	return string(out)
}

func TestListSourcesReportsWhatEachHasOnDisk(t *testing.T) {
	h := newHarness(t)
	h.sources.sources = []domain.Source{{
		ID: 4, Name: "Channel 5 with Andrew Callaghan",
		URL:            "https://www.youtube.com/@Channel5YouTube",
		CollectionType: domain.CollectionChannel, Enabled: true,
	}}
	h.library.stats = map[int64]library.SourceStats{4: {Files: 14, Bytes: 6_100_000_000}}

	got := h.call(t, "list_sources", map[string]any{})

	for _, want := range []string{"Channel 5 with Andrew Callaghan", `"videos_on_disk":14`, `"size_bytes":6100000000`} {
		if !strings.Contains(got, want) {
			t.Errorf("list_sources output missing %s:\n%s", want, got)
		}
	}
}

func TestSearchLibraryCarriesEveryNarrowing(t *testing.T) {
	h := newHarness(t)

	h.call(t, "search_library", map[string]any{
		"query": "gps", "status": "failed", "source_id": 7, "limit": 5,
	})

	q := h.library.lastQuery
	if q.Search != "gps" || q.Status != domain.MediaFailed || q.SourceID != 7 || q.Limit != 5 {
		t.Errorf("query = %+v, want gps/failed/7/5", q)
	}
}

func TestSearchLibraryRejectsAnUnknownStatus(t *testing.T) {
	h := newHarness(t)

	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_library", Arguments: map[string]any{"status": "sideways"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unknown status should be a tool error naming the options")
	}
}

func TestSaveVideoRoutesTheProfile(t *testing.T) {
	h := newHarness(t)

	got := h.call(t, "save_video", map[string]any{
		"url": "https://youtu.be/gCZOjDar1tU", "profile_id": 3,
	})

	if h.sources.downloadedURLs[0] != "https://youtu.be/gCZOjDar1tU" || h.sources.downloadedPID != 3 {
		t.Errorf("DownloadVideo called with %v profile %d", h.sources.downloadedURLs, h.sources.downloadedPID)
	}
	if !strings.Contains(got, `"media_id":42`) {
		t.Errorf("output missing the media id:\n%s", got)
	}
}

func TestAddSourceDefaultsTheProfileAndRollsTheCutoff(t *testing.T) {
	h := newHarness(t)

	h.call(t, "add_source", map[string]any{
		"url": "https://www.youtube.com/@BattleBots", "type": "playlist", "cutoff_days": 90,
	})

	in := h.sources.added
	if in.CollectionType != domain.CollectionPlaylist {
		t.Errorf("collection = %q, want playlist", in.CollectionType)
	}
	if in.MediaProfileID != 1 {
		t.Errorf("profile = %d, want the default 1", in.MediaProfileID)
	}
	if in.CutoffWindow != 90*24*time.Hour {
		t.Errorf("cutoff = %v, want 90 days", in.CutoffWindow)
	}
}

func TestScanNowIsANoOpWhileAScanIsInFlight(t *testing.T) {
	h := newHarness(t)
	h.jobs.unfinished = map[jobs.TaskType]bool{jobs.TaskIndexSource: true}

	got := h.call(t, "scan_now", map[string]any{"source_id": 4})

	if len(h.sources.scanned) != 0 {
		t.Errorf("scanned = %v, want none while a scan is in flight", h.sources.scanned)
	}
	if !strings.Contains(got, "already") {
		t.Errorf("output should say a scan is already underway:\n%s", got)
	}
}

func TestDeleteSourceKeepsFilesUnlessAskedNotTo(t *testing.T) {
	h := newHarness(t)
	h.sources.sources = []domain.Source{{ID: 8, Name: "The Guild", CollectionType: domain.CollectionPlaylist}}

	got := h.call(t, "delete_source", map[string]any{"source_id": 8})

	if len(h.sources.deleted) != 1 || h.sources.deleted[0] != 8 {
		t.Fatalf("deleted = %v, want [8]", h.sources.deleted)
	}
	if h.sources.deletedFiles[0] {
		t.Error("files were deleted without delete_files being set")
	}
	if !strings.Contains(got, "kept on disk") {
		t.Errorf("output should say the files were kept:\n%s", got)
	}

	h.call(t, "delete_source", map[string]any{"source_id": 8, "delete_files": true})
	if !h.sources.deletedFiles[1] {
		t.Error("delete_files was not honoured")
	}
}

func TestQueueStatusListsOnlyLiveWork(t *testing.T) {
	h := newHarness(t)
	h.jobs.counts = map[jobs.TaskStatus]int{jobs.StatusRunning: 1, jobs.StatusPending: 2, jobs.StatusFailed: 3}
	h.jobs.items = []library.JobListItem{
		{Task: jobs.Task{ID: 1, Type: jobs.TaskDownloadMedia, Status: jobs.StatusRunning}, MediaTitle: "Live One"},
		{Task: jobs.Task{ID: 2, Type: jobs.TaskDownloadMedia, Status: jobs.StatusSucceeded}, MediaTitle: "Old One"},
	}

	got := h.call(t, "queue_status", map[string]any{})

	if !strings.Contains(got, "Live One") || strings.Contains(got, "Old One") {
		t.Errorf("active jobs should list live work only:\n%s", got)
	}
	if !strings.Contains(got, `"failed":3`) {
		t.Errorf("counts missing:\n%s", got)
	}
}

func TestBearerGateProtectsTheEndpoint(t *testing.T) {
	handler := requireBearer("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	deny := httptest.NewRecorder()
	handler.ServeHTTP(deny, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if deny.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", deny.Code)
	}

	wrong := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer nope")
	handler.ServeHTTP(wrong, req)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", wrong.Code)
	}

	allow := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(allow, req)
	if allow.Code != http.StatusNoContent {
		t.Fatalf("right token: status = %d, want 204", allow.Code)
	}
}
