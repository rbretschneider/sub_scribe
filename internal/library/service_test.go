package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/events"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/naming"
	"sub_scribe/internal/ytdlp"
)

// fixedClock is a deterministic Clock for tests.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// fakeSourceRepo is an in-memory SourceRepo backed by a map.
type fakeSourceRepo struct {
	items  map[int64]domain.Source
	nextID int64
	marked map[int64]time.Time
}

func newFakeSourceRepo() *fakeSourceRepo {
	return &fakeSourceRepo{items: map[int64]domain.Source{}, marked: map[int64]time.Time{}}
}

func (r *fakeSourceRepo) Create(_ context.Context, source domain.Source) (int64, error) {
	r.nextID++
	source.ID = r.nextID
	r.items[source.ID] = source
	return source.ID, nil
}

func (r *fakeSourceRepo) Get(_ context.Context, id int64) (domain.Source, error) {
	s, ok := r.items[id]
	if !ok {
		return domain.Source{}, errors.New("source not found")
	}
	return s, nil
}

func (r *fakeSourceRepo) List(_ context.Context) ([]domain.Source, error) {
	out := make([]domain.Source, 0, len(r.items))
	for _, s := range r.items {
		out = append(out, s)
	}
	return out, nil
}

func (r *fakeSourceRepo) Update(_ context.Context, source domain.Source) error {
	if _, ok := r.items[source.ID]; !ok {
		return errors.New("source not found")
	}
	r.items[source.ID] = source
	return nil
}

func (r *fakeSourceRepo) Delete(_ context.Context, id int64) error {
	delete(r.items, id)
	return nil
}

func (r *fakeSourceRepo) DueForIndex(_ context.Context, _ time.Time) ([]domain.Source, error) {
	return nil, nil
}

func (r *fakeSourceRepo) MarkIndexed(_ context.Context, id int64, now time.Time) error {
	s, ok := r.items[id]
	if !ok {
		return errors.New("source not found")
	}
	s.LastIndexedAt = &now
	r.items[id] = s
	r.marked[id] = now
	return nil
}

// fakeMediaRepo is an in-memory MediaRepo backed by a map.
type fakeMediaRepo struct {
	items  map[int64]domain.Media
	nextID int64
}

func newFakeMediaRepo() *fakeMediaRepo {
	return &fakeMediaRepo{items: map[int64]domain.Media{}}
}

func (r *fakeMediaRepo) Upsert(_ context.Context, media domain.Media) (int64, error) {
	for id, existing := range r.items {
		if existing.SourceID == media.SourceID && existing.ExternalID == media.ExternalID {
			media.ID = id
			r.items[id] = media
			return id, nil
		}
	}
	r.nextID++
	media.ID = r.nextID
	r.items[media.ID] = media
	return media.ID, nil
}

func (r *fakeMediaRepo) Get(_ context.Context, id int64) (domain.Media, error) {
	m, ok := r.items[id]
	if !ok {
		return domain.Media{}, errors.New("media not found")
	}
	return m, nil
}

func (r *fakeMediaRepo) FindBySource(_ context.Context, sourceID int64, externalID string) (domain.Media, bool, error) {
	for _, m := range r.items {
		if m.SourceID == sourceID && m.ExternalID == externalID {
			return m, true, nil
		}
	}
	return domain.Media{}, false, nil
}

func (r *fakeMediaRepo) ExistsBySource(_ context.Context, sourceID int64, externalID string) (bool, error) {
	for _, m := range r.items {
		if m.SourceID == sourceID && m.ExternalID == externalID {
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeMediaRepo) ListBySource(_ context.Context, sourceID int64) ([]domain.Media, error) {
	out := make([]domain.Media, 0)
	for _, m := range r.items {
		if m.SourceID == sourceID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (r *fakeMediaRepo) ListByStatus(_ context.Context, status domain.MediaStatus, _ int) ([]domain.Media, error) {
	out := make([]domain.Media, 0)
	for _, m := range r.items {
		if m.Status == status {
			out = append(out, m)
		}
	}
	return out, nil
}

func (r *fakeMediaRepo) SetStatus(_ context.Context, id int64, status domain.MediaStatus, now time.Time) error {
	m, ok := r.items[id]
	if !ok {
		return errors.New("media not found")
	}
	m.Status = status
	m.UpdatedAt = now
	r.items[id] = m
	return nil
}

func (r *fakeMediaRepo) MarkDownloaded(_ context.Context, id int64, filePath string, size int64, now time.Time) error {
	m, ok := r.items[id]
	if !ok {
		return errors.New("media not found")
	}
	m.Status = domain.MediaDownloaded
	m.FilePath = filePath
	m.FileSize = size
	m.DownloadedAt = &now
	m.UpdatedAt = now
	r.items[id] = m
	return nil
}

func (r *fakeMediaRepo) SetError(_ context.Context, id int64, status domain.MediaStatus, cause string, now time.Time) error {
	m, ok := r.items[id]
	if !ok {
		return errors.New("media not found")
	}
	m.Status = status
	m.LastError = cause
	m.UpdatedAt = now
	r.items[id] = m
	return nil
}

func (r *fakeMediaRepo) CountsByStatus(_ context.Context) (map[domain.MediaStatus]int, error) {
	counts := make(map[domain.MediaStatus]int)
	for _, m := range r.items {
		counts[m.Status]++
	}
	return counts, nil
}

func (r *fakeMediaRepo) TotalDownloadedBytes(_ context.Context) (int64, error) {
	var total int64
	for _, m := range r.items {
		if m.Status == domain.MediaDownloaded {
			total += m.FileSize
		}
	}
	return total, nil
}

func (r *fakeMediaRepo) SameDayIndex(_ context.Context, id int64) (int, error) {
	media, ok := r.items[id]
	if !ok {
		return 1, nil
	}
	index := 0
	for otherID, other := range r.items {
		same := other.SourceID == media.SourceID &&
			other.Metadata.UploadDate.Equal(media.Metadata.UploadDate)
		if same && otherID <= id {
			index++
		}
	}
	if index < 1 {
		index = 1
	}
	return index, nil
}

func (r *fakeMediaRepo) StatsBySource(_ context.Context) (map[int64]SourceStats, error) {
	stats := make(map[int64]SourceStats)
	for _, m := range r.items {
		if m.Status != domain.MediaDownloaded {
			continue
		}
		entry := stats[m.SourceID]
		entry.Files++
		entry.Bytes += m.FileSize
		stats[m.SourceID] = entry
	}
	return stats, nil
}

func (r *fakeMediaRepo) GetWithSource(_ context.Context, id int64) (MediaListItem, error) {
	m, ok := r.items[id]
	if !ok {
		return MediaListItem{}, errors.New("media not found")
	}
	return MediaListItem{Media: m, SourceName: "Test Source"}, nil
}

func (r *fakeMediaRepo) ListWithSource(_ context.Context, status domain.MediaStatus, limit int) ([]MediaListItem, error) {
	var items []MediaListItem
	for _, m := range r.items {
		if status != "" && m.Status != status {
			continue
		}
		items = append(items, MediaListItem{Media: m, SourceName: "Test Source"})
		if limit > 0 && len(items) >= limit {
			break
		}
	}
	return items, nil
}

func TestOverviewSummarizesArchive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	src, err := h.svc.AddSource(ctx, validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	mustUpsert(t, h, domain.Media{SourceID: src.ID, ExternalID: "a", Status: domain.MediaDownloaded, FileSize: 500})
	mustUpsert(t, h, domain.Media{SourceID: src.ID, ExternalID: "b", Status: domain.MediaDownloaded, FileSize: 1500})
	mustUpsert(t, h, domain.Media{SourceID: src.ID, ExternalID: "c", Status: domain.MediaDownloading})

	overview, err := h.svc.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if overview.SourceCount != 1 {
		t.Errorf("SourceCount = %d, want 1", overview.SourceCount)
	}
	if overview.Counts[domain.MediaDownloaded] != 2 {
		t.Errorf("downloaded count = %d, want 2", overview.Counts[domain.MediaDownloaded])
	}
	if overview.StorageBytes != 2000 {
		t.Errorf("StorageBytes = %d, want 2000", overview.StorageBytes)
	}
	if len(overview.Downloading) != 1 {
		t.Errorf("Downloading len = %d, want 1", len(overview.Downloading))
	}
	if overview.TotalMedia != 3 {
		t.Errorf("TotalMedia = %d, want 3", overview.TotalMedia)
	}
}

func mustUpsert(t *testing.T, h *harness, media domain.Media) {
	t.Helper()
	if _, err := h.media.Upsert(context.Background(), media); err != nil {
		t.Fatalf("upsert media: %v", err)
	}
}

func TestEnforceRedownloadRequeuesOnlyStaleMedia(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID, err := h.profiles.Create(ctx, domain.MediaProfile{
		Name: "Refresh weekly", OutputPathTemplate: "{{ title }}", Kind: domain.MediaVideo,
		SponsorBlockMode: domain.SponsorBlockOff, MetadataFormat: domain.MetadataMovie,
		RedownloadAfter: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	src, err := h.svc.AddSource(ctx, validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	stale := h.now.Add(-30 * 24 * time.Hour)
	fresh := h.now.Add(-1 * time.Hour)
	staleID, _ := h.media.Upsert(ctx, domain.Media{SourceID: src.ID, ExternalID: "old", Status: domain.MediaDownloaded, FilePath: "/x/old.mp4", DownloadedAt: &stale})
	freshID, _ := h.media.Upsert(ctx, domain.Media{SourceID: src.ID, ExternalID: "new", Status: domain.MediaDownloaded, DownloadedAt: &fresh})

	if err := h.svc.EnforceRedownload(ctx, src.ID); err != nil {
		t.Fatalf("EnforceRedownload: %v", err)
	}

	staleMedia, _ := h.media.Get(ctx, staleID)
	if staleMedia.Status != domain.MediaPending {
		t.Errorf("stale media status = %q, want pending (requeued)", staleMedia.Status)
	}
	freshMedia, _ := h.media.Get(ctx, freshID)
	if freshMedia.Status != domain.MediaDownloaded {
		t.Errorf("fresh media status = %q, want downloaded (left alone)", freshMedia.Status)
	}
	if dl := h.tasks.tasksOfType(jobs.TaskDownloadMedia); len(dl) != 1 {
		t.Errorf("enqueued %d download tasks, want 1 (only the stale item)", len(dl))
	}
}

func TestDownloadMediaSkippedByDateCutoffMarksSkipped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	src, err := h.svc.AddSource(ctx, validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	mediaID, _ := h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "old", Status: domain.MediaPending,
		Metadata: domain.MediaMetadata{Title: "Old clip"},
	})
	// yt-dlp declined to download it (before the cutoff) — a settled skip.
	h.runner.downloadErr = ytdlp.ErrFilteredOut

	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia should not error on a date skip, got %v", err)
	}

	got, _ := h.media.Get(ctx, mediaID)
	if got.Status != domain.MediaSkipped {
		t.Errorf("status = %q, want skipped", got.Status)
	}
	if h.pub.countKind(events.KindMediaFailed) != 0 {
		t.Errorf("a date skip should not publish a failure event")
	}
}

func TestDownloadMediaAppliesCustomOptionsAndHook(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID, err := h.profiles.Create(ctx, domain.MediaProfile{
		Name: "Custom", OutputPathTemplate: "{{ title }}", Kind: domain.MediaVideo,
		SponsorBlockMode: domain.SponsorBlockOff, MetadataFormat: domain.MetadataMovie,
		ExtraYtdlpArgs:      []string{"--sleep-requests", "2"},
		PostDownloadCommand: "/opt/notify.sh",
	})
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	src, err := h.svc.AddSource(ctx, validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	mediaID, _ := h.media.Upsert(ctx, domain.Media{
		SourceID: src.ID, ExternalID: "v1", Status: domain.MediaPending,
		Metadata: domain.MediaMetadata{Title: "Vid"},
	})
	downloadedPath := filepath.Join(h.mediaDir, "Vid.mp4")
	h.runner.result = ytdlp.DownloadResult{FilePath: downloadedPath, FileSize: 10}

	if err := h.svc.DownloadMedia(ctx, mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}

	got := h.runner.lastOpts.ExtraArgs
	if len(got) != 2 || got[0] != "--sleep-requests" || got[1] != "2" {
		t.Errorf("ExtraArgs = %v, want [--sleep-requests 2]", got)
	}
	if h.hook.calls != 1 || h.hook.lastCommand != "/opt/notify.sh" || h.hook.lastPath != downloadedPath {
		t.Errorf("hook run = {calls:%d cmd:%q path:%q}, want 1 call for /opt/notify.sh with the file path",
			h.hook.calls, h.hook.lastCommand, h.hook.lastPath)
	}
}

func TestEnforceRedownloadNoopWhenAgeZero(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t) // seedProfile has no redownload age
	src, err := h.svc.AddSource(ctx, validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	old := h.now.Add(-99 * 24 * time.Hour)
	id, _ := h.media.Upsert(ctx, domain.Media{SourceID: src.ID, ExternalID: "x", Status: domain.MediaDownloaded, DownloadedAt: &old})

	if err := h.svc.EnforceRedownload(ctx, src.ID); err != nil {
		t.Fatalf("EnforceRedownload: %v", err)
	}
	got, _ := h.media.Get(ctx, id)
	if got.Status != domain.MediaDownloaded {
		t.Errorf("status = %q, want downloaded (no redownload age configured)", got.Status)
	}
}

// fakeProfileRepo is an in-memory ProfileRepo backed by a map.
type fakeProfileRepo struct {
	items  map[int64]domain.MediaProfile
	nextID int64
}

func newFakeProfileRepo() *fakeProfileRepo {
	return &fakeProfileRepo{items: map[int64]domain.MediaProfile{}}
}

func (r *fakeProfileRepo) Create(_ context.Context, profile domain.MediaProfile) (int64, error) {
	r.nextID++
	profile.ID = r.nextID
	r.items[profile.ID] = profile
	return profile.ID, nil
}

func (r *fakeProfileRepo) Get(_ context.Context, id int64) (domain.MediaProfile, error) {
	p, ok := r.items[id]
	if !ok {
		return domain.MediaProfile{}, errors.New("profile not found")
	}
	return p, nil
}

func (r *fakeProfileRepo) List(_ context.Context) ([]domain.MediaProfile, error) {
	out := make([]domain.MediaProfile, 0, len(r.items))
	for _, p := range r.items {
		out = append(out, p)
	}
	return out, nil
}

func (r *fakeProfileRepo) Update(_ context.Context, profile domain.MediaProfile) error {
	if _, ok := r.items[profile.ID]; !ok {
		return errors.New("profile not found")
	}
	r.items[profile.ID] = profile
	return nil
}

func (r *fakeProfileRepo) Delete(_ context.Context, id int64) error {
	delete(r.items, id)
	return nil
}

// fakeTaskEnqueuer records enqueued tasks.
type fakeTaskEnqueuer struct {
	tasks  []jobs.Task
	nextID int64
}

func (e *fakeTaskEnqueuer) Enqueue(_ context.Context, task jobs.Task) (int64, error) {
	e.nextID++
	task.ID = e.nextID
	e.tasks = append(e.tasks, task)
	return task.ID, nil
}

// fakeQueueMaintain is an in-memory QueueMaintain that records the repairs the
// service asked for, so reconciliation can be tested without a database.
type fakeQueueMaintain struct {
	orphansRemoved  int
	runningRequeued int
	activeMedia     map[int64]bool
	retried         []int64
	retryErr        error

	deleted        []int64
	finishedPruned int
	pruneCutoff    time.Time
}

func newFakeQueueMaintain() *fakeQueueMaintain {
	return &fakeQueueMaintain{activeMedia: map[int64]bool{}}
}

func (q *fakeQueueMaintain) Retry(_ context.Context, id int64, _ time.Time) error {
	if q.retryErr != nil {
		return q.retryErr
	}
	q.retried = append(q.retried, id)
	return nil
}

func (q *fakeQueueMaintain) Delete(_ context.Context, id int64) error {
	q.deleted = append(q.deleted, id)
	return nil
}

func (q *fakeQueueMaintain) DeleteFinished(_ context.Context) (int, error) {
	return q.finishedPruned, nil
}

func (q *fakeQueueMaintain) DeleteFinishedBefore(_ context.Context, cutoff time.Time) (int, error) {
	q.pruneCutoff = cutoff
	return q.finishedPruned, nil
}

func (q *fakeQueueMaintain) DeleteOrphans(_ context.Context) (int, error) {
	return q.orphansRemoved, nil
}

func (q *fakeQueueMaintain) RequeueRunning(_ context.Context, _ time.Time) (int, error) {
	return q.runningRequeued, nil
}

func (q *fakeQueueMaintain) ActiveMediaIDs(_ context.Context) (map[int64]bool, error) {
	return q.activeMedia, nil
}

func (e *fakeTaskEnqueuer) tasksOfType(t jobs.TaskType) []jobs.Task {
	out := make([]jobs.Task, 0)
	for _, task := range e.tasks {
		if task.Type == t {
			out = append(out, task)
		}
	}
	return out
}

// fakeRunner is an in-memory ytdlp.Runner.
type fakeRunner struct {
	entries       []ytdlp.IndexEntry
	indexErr      error
	downloadErr   error
	result        ytdlp.DownloadResult
	indexCalls    int
	lastIndexOpts ytdlp.IndexOptions
	downloadCalls int
	lastOpts      ytdlp.DownloadOptions
	lastURL       string
	progress      []float64

	// metadata is returned by Metadata, standing in for the full per-item lookup
	// the service makes when indexing left the upload date blank.
	metadata      ytdlp.IndexEntry
	metadataErr   error
	metadataCalls int
}

func (r *fakeRunner) Index(_ context.Context, _ string, opts ytdlp.IndexOptions) ([]ytdlp.IndexEntry, error) {
	r.indexCalls++
	r.lastIndexOpts = opts
	if r.indexErr != nil {
		return nil, r.indexErr
	}
	return r.entries, nil
}

func (r *fakeRunner) Metadata(_ context.Context, _, _ string) (ytdlp.IndexEntry, error) {
	r.metadataCalls++
	if r.metadataErr != nil {
		return ytdlp.IndexEntry{}, r.metadataErr
	}
	return r.metadata, nil
}

func (r *fakeRunner) Download(_ context.Context, url string, opts ytdlp.DownloadOptions, onProgress ytdlp.ProgressFunc) (ytdlp.DownloadResult, error) {
	r.downloadCalls++
	r.lastURL = url
	r.lastOpts = opts
	if onProgress != nil {
		onProgress(50)
		r.progress = append(r.progress, 50)
	}
	if r.downloadErr != nil {
		return ytdlp.DownloadResult{}, r.downloadErr
	}
	return r.result, nil
}

// capturingPublisher records published events.
type capturingPublisher struct{ events []events.Event }

func (p *capturingPublisher) Publish(e events.Event) { p.events = append(p.events, e) }

func (p *capturingPublisher) countKind(k events.Kind) int {
	n := 0
	for _, e := range p.events {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// fakeMetadataWriter records metadata writes and the format requested.
type fakeMetadataWriter struct {
	calls      int
	lastFormat domain.MetadataFormat
	err        error
}

func (w *fakeMetadataWriter) WriteFor(_ context.Context, _ string, _ domain.Media, _ string, format domain.MetadataFormat) error {
	w.calls++
	w.lastFormat = format
	return w.err
}

// fakeFeedWriter records feed writes.
type fakeFeedWriter struct {
	calls     int
	lastItems []domain.Media
	err       error
}

func (w *fakeFeedWriter) WriteFeed(_ context.Context, _ domain.Source, items []domain.Media) error {
	w.calls++
	w.lastItems = items
	return w.err
}

// fakeNotifier records notifications.
type fakeNotifier struct {
	calls int
	err   error
}

func (n *fakeNotifier) Notify(_ context.Context, _, _ string) error {
	n.calls++
	return n.err
}

// fakeHook records the post-download command and media path it was asked to run.
type fakeHook struct {
	calls       int
	lastCommand string
	lastPath    string
}

func (h *fakeHook) Run(_ context.Context, command, mediaPath string) error {
	h.calls++
	h.lastCommand = command
	h.lastPath = mediaPath
	return nil
}

// fakeSponsorBlock returns a fixed argument list.
type fakeSponsorBlock struct{ args []string }

func (b fakeSponsorBlock) Args(_ domain.SponsorBlockMode, _ []domain.SponsorBlockCategory) []string {
	return b.args
}

// harness bundles the fakes and the service under test.
type harness struct {
	sources  *fakeSourceRepo
	media    *fakeMediaRepo
	profiles *fakeProfileRepo
	tasks    *fakeTaskEnqueuer
	queue    *fakeQueueMaintain
	runner   *fakeRunner
	metadata *fakeMetadataWriter
	feed     *fakeFeedWriter
	notifier *fakeNotifier
	hook     *fakeHook
	pub      *capturingPublisher
	svc      *Service
	now      time.Time
	mediaDir string
	tempDir  string
	// retention is the job-history window the service is built with. Tests that
	// care about pruning set it and call rebuildService.
	retention time.Duration
	// pace, when set, is the download gate. Nil leaves downloads unpaced, which
	// is what most tests want.
	pace DownloadPacer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	h := &harness{
		sources:  newFakeSourceRepo(),
		media:    newFakeMediaRepo(),
		profiles: newFakeProfileRepo(),
		tasks:    &fakeTaskEnqueuer{},
		queue:    newFakeQueueMaintain(),
		runner:   &fakeRunner{},
		metadata: &fakeMetadataWriter{},
		feed:     &fakeFeedWriter{},
		notifier: &fakeNotifier{},
		hook:     &fakeHook{},
		pub:      &capturingPublisher{},
		now:      now,
		mediaDir: t.TempDir(),
		tempDir:  t.TempDir(),
	}
	h.rebuildService()
	return h
}

// rebuildService reconstructs the service from the harness's current settings,
// so a test can vary a Deps value (such as the retention window) after the
// fakes are in place without duplicating the whole wiring.
func (h *harness) rebuildService() {
	h.svc = NewService(Deps{
		Sources:      h.sources,
		Media:        h.media,
		Profiles:     h.profiles,
		Tasks:        h.tasks,
		Queue:        h.queue,
		Runner:       h.runner,
		Naming:       naming.NewRenderer(),
		Metadata:     h.metadata,
		Feed:         h.feed,
		Notifier:     h.notifier,
		SponsorBlock: fakeSponsorBlock{args: []string{"--sponsorblock-remove", "sponsor"}},
		Hook:         h.hook,
		Events:       h.pub,
		Clock:        fixedClock{t: h.now},
		DownloadPace: h.pace,
		MediaDir:     h.mediaDir,
		TempDir:      h.tempDir,
		CookiesPath:  "",
		JobRetention: h.retention,
	})
}

// seedProfile inserts a valid profile and returns its id.
func (h *harness) seedProfile(t *testing.T) int64 {
	t.Helper()
	id, err := h.profiles.Create(context.Background(), domain.MediaProfile{
		Name:               "Plex TV",
		OutputPathTemplate: "{{ source_name }}/{{ title }}",
		Kind:               domain.MediaVideo,
		QualityFormat:      "bestvideo+bestaudio",
		MetadataFormat:     domain.MetadataMovie,
		SponsorBlockMode:   domain.SponsorBlockOff,
	})
	if err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return id
}

func validInput(profileID int64) AddSourceInput {
	return AddSourceInput{
		Name:           "My Channel",
		URL:            "https://youtube.com/@chan",
		CollectionType: domain.CollectionChannel,
		MediaProfileID: profileID,
		CookieBehavior: domain.CookieDisabled,
	}
}

func TestAddSourcePersistsEnqueuesAndSkipsRunner(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)

	src, err := h.svc.AddSource(context.Background(), validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	if src.ID == 0 {
		t.Fatalf("expected assigned source id, got 0")
	}
	if !src.Enabled {
		t.Errorf("expected source enabled")
	}
	if src.IndexFrequency != defaultIndexFrequency {
		t.Errorf("expected default index frequency %v, got %v", defaultIndexFrequency, src.IndexFrequency)
	}
	if src.ShortsRule != domain.InclusionInclude || src.LivestreamsRule != domain.InclusionInclude {
		t.Errorf("expected inclusion rules defaulted to include, got %q/%q", src.ShortsRule, src.LivestreamsRule)
	}
	if _, err := h.sources.Get(context.Background(), src.ID); err != nil {
		t.Errorf("source not persisted: %v", err)
	}

	indexTasks := h.tasks.tasksOfType(jobs.TaskIndexSource)
	if len(indexTasks) != 1 {
		t.Fatalf("expected exactly 1 index task, got %d", len(indexTasks))
	}
	if indexTasks[0].SourceID == nil || *indexTasks[0].SourceID != src.ID {
		t.Errorf("index task not scoped to source %d: %+v", src.ID, indexTasks[0])
	}
	if h.runner.indexCalls != 0 || h.runner.downloadCalls != 0 {
		t.Errorf("AddSource must not invoke Runner (index=%d download=%d)", h.runner.indexCalls, h.runner.downloadCalls)
	}
}

func TestAddSourceValidation(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)

	tests := []struct {
		name    string
		mutate  func(*AddSourceInput)
		wantErr error
	}{
		{"blank url", func(in *AddSourceInput) { in.URL = "" }, errURLRequired},
		{"bad title regex", func(in *AddSourceInput) { in.TitleFilterPattern = "([" }, nil},
		{"bad collection type", func(in *AddSourceInput) { in.CollectionType = "bogus" }, nil},
		{"missing profile", func(in *AddSourceInput) { in.MediaProfileID = 999 }, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput(profileID)
			tc.mutate(&in)
			_, err := h.svc.AddSource(context.Background(), in)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected error %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestRequestScanEnqueuesHighPriorityIndex(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)
	src, err := h.svc.AddSource(ctx, validInput(profileID))
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	before := len(h.tasks.tasksOfType(jobs.TaskIndexSource))

	if err := h.svc.RequestScan(ctx, src.ID); err != nil {
		t.Fatalf("RequestScan: %v", err)
	}

	indexTasks := h.tasks.tasksOfType(jobs.TaskIndexSource)
	if len(indexTasks) != before+1 {
		t.Fatalf("index tasks = %d, want %d", len(indexTasks), before+1)
	}
	last := indexTasks[len(indexTasks)-1]
	if last.SourceID == nil || *last.SourceID != src.ID {
		t.Errorf("scan task source = %v, want %d", last.SourceID, src.ID)
	}
	if last.Priority <= 0 {
		t.Errorf("scan task priority = %d, want a raised priority", last.Priority)
	}
}

func TestAddSourceAllowsBlankName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)

	in := validInput(profileID)
	in.Name = "   "
	src, err := h.svc.AddSource(ctx, in)
	if err != nil {
		t.Fatalf("AddSource with blank name should be allowed, got %v", err)
	}
	if src.Name != "" {
		t.Errorf("blank name should persist blank until indexed, got %q", src.Name)
	}
}

func TestIndexSourceAutoNamesFromChannel(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)

	in := validInput(profileID)
	in.Name = ""
	src, err := h.svc.AddSource(ctx, in)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	h.runner.entries = []ytdlp.IndexEntry{
		{ExternalID: "a", Title: "Vid A", Uploader: "Veritasium"},
	}
	if err := h.svc.IndexSource(ctx, src.ID); err != nil {
		t.Fatalf("IndexSource: %v", err)
	}

	got, _ := h.sources.Get(ctx, src.ID)
	if got.Name != "Veritasium" {
		t.Errorf("auto-named source = %q, want the channel name Veritasium", got.Name)
	}
}

func TestIndexSourceKeepsUserName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)

	in := validInput(profileID) // validInput sets a non-blank name
	src, err := h.svc.AddSource(ctx, in)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	original := src.Name

	h.runner.entries = []ytdlp.IndexEntry{{ExternalID: "a", Uploader: "Some Channel"}}
	if err := h.svc.IndexSource(ctx, src.ID); err != nil {
		t.Fatalf("IndexSource: %v", err)
	}

	got, _ := h.sources.Get(ctx, src.ID)
	if got.Name != original {
		t.Errorf("user-set name was overwritten: got %q, want %q", got.Name, original)
	}
}

func TestIndexSourceRollingCutoffSkipsOldUploads(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	profileID := h.seedProfile(t)

	in := validInput(profileID)
	in.CutoffWindow = 30 * 24 * time.Hour // "the last 30 days"
	src, err := h.svc.AddSource(ctx, in)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	// Harness clock is 2026-07-21, so the effective cutoff is ~2026-06-21.
	h.runner.entries = []ytdlp.IndexEntry{
		{ExternalID: "old", Title: "Old", Uploader: "C", UploadDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ExternalID: "new", Title: "New", Uploader: "C", UploadDate: time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)},
	}
	if err := h.svc.IndexSource(ctx, src.ID); err != nil {
		t.Fatalf("IndexSource: %v", err)
	}

	dl := h.tasks.tasksOfType(jobs.TaskDownloadMedia)
	if len(dl) != 1 {
		t.Errorf("enqueued %d download tasks, want 1 (the old upload should be skipped by the rolling window)", len(dl))
	}
}

func TestIndexSourceFiltersDedupesAndEnqueues(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)

	src, err := h.svc.AddSource(context.Background(), AddSourceInput{
		Name:           "Chan",
		URL:            "https://youtube.com/@chan",
		CollectionType: domain.CollectionChannel,
		MediaProfileID: profileID,
		CookieBehavior: domain.CookieDisabled,
		ShortsRule:     domain.InclusionExclude,
	})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	// Pre-existing media that should be skipped as a duplicate.
	if _, err := h.media.Upsert(context.Background(), domain.Media{
		SourceID:   src.ID,
		ExternalID: "existing1",
		Status:     domain.MediaDownloaded,
	}); err != nil {
		t.Fatalf("seed media: %v", err)
	}

	h.runner.entries = []ytdlp.IndexEntry{
		{ExternalID: "vid1", Title: "Regular video"},
		{ExternalID: "short1", Title: "A short", IsShort: true}, // excluded by ShortsRule
		{ExternalID: "existing1", Title: "Already have"},        // duplicate
		{ExternalID: "vid2", Title: "Another video"},
	}

	if err := h.svc.IndexSource(context.Background(), src.ID); err != nil {
		t.Fatalf("IndexSource: %v", err)
	}

	// Only vid1 and vid2 should have produced pending media + download tasks.
	pending, _ := h.media.ListByStatus(context.Background(), domain.MediaPending, 0)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending media, got %d", len(pending))
	}
	dlTasks := h.tasks.tasksOfType(jobs.TaskDownloadMedia)
	if len(dlTasks) != 2 {
		t.Fatalf("expected 2 download tasks, got %d", len(dlTasks))
	}

	// The short must not have been recorded at all.
	if exists, _ := h.media.ExistsBySource(context.Background(), src.ID, "short1"); exists {
		t.Errorf("short entry should have been filtered out")
	}

	if _, ok := h.sources.marked[src.ID]; !ok {
		t.Errorf("expected source to be marked indexed")
	}
	if h.pub.countKind(events.KindSourceIndexed) != 1 {
		t.Errorf("expected 1 source_indexed event, got %d", h.pub.countKind(events.KindSourceIndexed))
	}
}

func TestIndexSourcePropagatesRunnerError(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	h.runner.indexErr = errors.New("boom")

	if err := h.svc.IndexSource(context.Background(), src.ID); err == nil {
		t.Fatalf("expected error from IndexSource when runner fails")
	}
	if _, ok := h.sources.marked[src.ID]; ok {
		t.Errorf("source should not be marked indexed on failure")
	}
}

func TestDownloadMediaSuccess(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))

	mediaID, _ := h.media.Upsert(context.Background(), domain.Media{
		SourceID:   src.ID,
		ExternalID: "abc123",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "Cool Video"},
	})

	downloadedPath := filepath.Join(h.mediaDir, "My Channel", "Cool Video.mp4")
	h.runner.result = ytdlp.DownloadResult{FilePath: downloadedPath, FileSize: 4242}

	if err := h.svc.DownloadMedia(context.Background(), mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}

	if h.runner.downloadCalls != 1 {
		t.Fatalf("expected 1 download call, got %d", h.runner.downloadCalls)
	}
	// URL built from external id.
	wantURL := youtubeWatchBase + "abc123"
	if h.runner.lastURL != wantURL {
		t.Errorf("expected download url %q, got %q", wantURL, h.runner.lastURL)
	}
	// The destination is expressed as the media root plus a relative template, so
	// yt-dlp honours the separate scratch directory (it ignores --paths when the
	// output template is absolute).
	wantOut := filepath.Join("My Channel", "Cool Video")
	if h.runner.lastOpts.OutputPath != wantOut {
		t.Errorf("expected output path %q, got %q", wantOut, h.runner.lastOpts.OutputPath)
	}
	if h.runner.lastOpts.HomeDir != h.mediaDir {
		t.Errorf("expected home dir %q, got %q", h.mediaDir, h.runner.lastOpts.HomeDir)
	}
	// SponsorBlock args threaded through.
	if len(h.runner.lastOpts.SponsorBlockArgs) != 2 {
		t.Errorf("expected sponsorblock args passed through, got %v", h.runner.lastOpts.SponsorBlockArgs)
	}

	got, _ := h.media.Get(context.Background(), mediaID)
	if got.Status != domain.MediaDownloaded {
		t.Errorf("expected status downloaded, got %q", got.Status)
	}
	if got.FilePath != downloadedPath || got.FileSize != 4242 {
		t.Errorf("download result not persisted: %+v", got)
	}

	if h.metadata.calls != 1 {
		t.Errorf("expected metadata written once, got %d", h.metadata.calls)
	}
	if h.metadata.lastFormat != domain.MetadataMovie {
		t.Errorf("expected metadata format %q passed to writer, got %q", domain.MetadataMovie, h.metadata.lastFormat)
	}
	if h.feed.calls != 1 {
		t.Errorf("expected feed regenerated once, got %d", h.feed.calls)
	}
	if h.notifier.calls != 1 {
		t.Errorf("expected notification once, got %d", h.notifier.calls)
	}
	if h.pub.countKind(events.KindMediaCompleted) != 1 {
		t.Errorf("expected 1 media_completed event, got %d", h.pub.countKind(events.KindMediaCompleted))
	}
}

func TestDownloadMediaFailure(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))

	mediaID, _ := h.media.Upsert(context.Background(), domain.Media{
		SourceID:   src.ID,
		ExternalID: "abc123",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "Cool Video"},
	})
	h.runner.downloadErr = errors.New("network down")

	err := h.svc.DownloadMedia(context.Background(), mediaID)
	if err == nil {
		t.Fatalf("expected error from failed download")
	}

	got, _ := h.media.Get(context.Background(), mediaID)
	if got.Status != domain.MediaFailed {
		t.Errorf("expected status failed, got %q", got.Status)
	}
	if got.LastError == "" {
		t.Errorf("expected LastError recorded")
	}
	if h.pub.countKind(events.KindMediaFailed) != 1 {
		t.Errorf("expected 1 media_failed event, got %d", h.pub.countKind(events.KindMediaFailed))
	}
	if h.pub.countKind(events.KindMediaCompleted) != 0 {
		t.Errorf("expected no completed event on failure")
	}
	if h.feed.calls != 0 {
		t.Errorf("feed should not regenerate on failure")
	}
}

func TestEnforceRetention(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)

	src, _ := h.svc.AddSource(context.Background(), AddSourceInput{
		Name:           "Chan",
		URL:            "https://youtube.com/@chan",
		CollectionType: domain.CollectionChannel,
		MediaProfileID: profileID,
		CookieBehavior: domain.CookieDisabled,
		RetentionAfter: 30 * 24 * time.Hour,
	})

	// One old file (should be deleted) and one recent (kept).
	oldFile := filepath.Join(h.mediaDir, "old.mp4")
	newFile := filepath.Join(h.mediaDir, "new.mp4")
	if err := os.WriteFile(oldFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	oldTime := h.now.Add(-40 * 24 * time.Hour)
	newTime := h.now.Add(-5 * 24 * time.Hour)
	oldID, _ := h.media.Upsert(context.Background(), domain.Media{
		SourceID: src.ID, ExternalID: "old", Status: domain.MediaDownloaded,
		FilePath: oldFile, DownloadedAt: &oldTime,
	})
	newID, _ := h.media.Upsert(context.Background(), domain.Media{
		SourceID: src.ID, ExternalID: "new", Status: domain.MediaDownloaded,
		FilePath: newFile, DownloadedAt: &newTime,
	})

	if err := h.svc.EnforceRetention(context.Background(), src.ID); err != nil {
		t.Fatalf("EnforceRetention: %v", err)
	}

	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("expected old file removed")
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Errorf("expected new file kept: %v", err)
	}

	oldMedia, _ := h.media.Get(context.Background(), oldID)
	if oldMedia.Status != domain.MediaSkipped {
		t.Errorf("expected old media skipped, got %q", oldMedia.Status)
	}
	newMedia, _ := h.media.Get(context.Background(), newID)
	if newMedia.Status != domain.MediaDownloaded {
		t.Errorf("expected new media retained, got %q", newMedia.Status)
	}
}

func TestEnforceRetentionDisabled(t *testing.T) {
	h := newHarness(t)
	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID)) // RetentionAfter == 0

	past := h.now.Add(-365 * 24 * time.Hour)
	id, _ := h.media.Upsert(context.Background(), domain.Media{
		SourceID: src.ID, ExternalID: "x", Status: domain.MediaDownloaded,
		FilePath: filepath.Join(h.mediaDir, "keep.mp4"), DownloadedAt: &past,
	})

	if err := h.svc.EnforceRetention(context.Background(), src.ID); err != nil {
		t.Fatalf("EnforceRetention: %v", err)
	}
	got, _ := h.media.Get(context.Background(), id)
	if got.Status != domain.MediaDownloaded {
		t.Errorf("retention disabled: media should be untouched, got %q", got.Status)
	}
}

func TestCreateProfileValidatesTemplate(t *testing.T) {
	h := newHarness(t)

	_, err := h.svc.CreateProfile(context.Background(), domain.MediaProfile{
		Name:               "Bad",
		OutputPathTemplate: "{{ unknown_var }}",
		Kind:               domain.MediaVideo,
		SponsorBlockMode:   domain.SponsorBlockOff,
	})
	if err == nil {
		t.Fatalf("expected error for unknown template variable")
	}

	got, err := h.svc.CreateProfile(context.Background(), domain.MediaProfile{
		Name:               "Good",
		OutputPathTemplate: "{{ source_name }}/{{ title }}",
		Kind:               domain.MediaVideo,
		SponsorBlockMode:   domain.SponsorBlockOff,
	})
	if err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if got.ID == 0 {
		t.Errorf("expected profile id assigned")
	}
}

// stubPacer is a DownloadPacer with a scripted answer, so pacing behaviour is
// tested without any real waiting.
type stubPacer struct {
	allow bool
	slot  time.Time
	calls int
}

func (p *stubPacer) TryClaim() (time.Time, bool) {
	p.calls++
	return p.slot, p.allow
}

// TestDownloadMediaDefersWhenItIsNotItsTurn covers the pacing gate: the item
// must go back to pending and the task must be deferred rather than run, so a
// long interval costs a queue row instead of a worker.
func TestDownloadMediaDefersWhenItIsNotItsTurn(t *testing.T) {
	slot := time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC)
	h := newHarness(t)
	h.pace = &stubPacer{allow: false, slot: slot}
	h.rebuildService()

	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	mediaID, _ := h.media.Upsert(context.Background(), domain.Media{
		SourceID:   src.ID,
		ExternalID: "abc123",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "Cool Video", UploadDate: h.now},
	})

	err := h.svc.DownloadMedia(context.Background(), mediaID)

	var deferral *jobs.Deferral
	if !errors.As(err, &deferral) {
		t.Fatalf("DownloadMedia() = %v, want a jobs.Deferral", err)
	}
	if !deferral.RunAfter.Equal(slot) {
		t.Errorf("deferred until %v, want the pacer's slot %v", deferral.RunAfter, slot)
	}
	if h.runner.downloadCalls != 0 {
		t.Errorf("download ran anyway (%d calls); the gate did nothing", h.runner.downloadCalls)
	}
	// Left marked "downloading" it would show on the dashboard as an active
	// download that is not happening, for as long as the interval lasts.
	got, _ := h.media.Get(context.Background(), mediaID)
	if got.Status != domain.MediaPending {
		t.Errorf("status = %q while waiting for its turn, want %q", got.Status, domain.MediaPending)
	}
}

func TestDownloadMediaProceedsWhenItIsItsTurn(t *testing.T) {
	h := newHarness(t)
	h.pace = &stubPacer{allow: true}
	h.rebuildService()

	profileID := h.seedProfile(t)
	src, _ := h.svc.AddSource(context.Background(), validInput(profileID))
	mediaID, _ := h.media.Upsert(context.Background(), domain.Media{
		SourceID:   src.ID,
		ExternalID: "abc123",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "Cool Video", UploadDate: h.now},
	})
	h.runner.result = ytdlp.DownloadResult{
		FilePath: filepath.Join(h.mediaDir, "My Channel", "Cool Video.mp4"), FileSize: 1,
	}

	if err := h.svc.DownloadMedia(context.Background(), mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	if h.runner.downloadCalls != 1 {
		t.Errorf("download calls = %d, want 1", h.runner.downloadCalls)
	}
}

// TestPacingDoesNotApplyToItemsBeingDiscarded is the reason the gate sits after
// the window check rather than before it. A first scan of an old channel rejects
// hundreds of items without downloading anything; pacing those rejections at one
// every ten minutes would take days to get through a back catalogue nobody
// wanted.
func TestPacingDoesNotApplyToItemsBeingDiscarded(t *testing.T) {
	pacer := &stubPacer{allow: false, slot: time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC)}
	h := newHarness(t)
	h.pace = pacer
	h.rebuildService()

	profileID := h.seedProfile(t)
	cutoff := h.now.AddDate(0, 0, -7)
	input := validInput(profileID)
	input.DownloadCutoff = &cutoff
	src, _ := h.svc.AddSource(context.Background(), input)

	mediaID, _ := h.media.Upsert(context.Background(), domain.Media{
		SourceID:   src.ID,
		ExternalID: "old1",
		Status:     domain.MediaPending,
		Metadata:   domain.MediaMetadata{Title: "Ancient", UploadDate: h.now.AddDate(-1, 0, 0)},
	})

	if err := h.svc.DownloadMedia(context.Background(), mediaID); err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	got, _ := h.media.Get(context.Background(), mediaID)
	if got.Status != domain.MediaSkipped {
		t.Errorf("status = %q, want %q", got.Status, domain.MediaSkipped)
	}
	if pacer.calls != 0 {
		t.Errorf("the pacer was consulted %d times for an item that was never going to download", pacer.calls)
	}
}
