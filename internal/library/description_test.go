package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sub_scribe/internal/descclean"
	"sub_scribe/internal/domain"
	"sub_scribe/internal/jobs"
)

const (
	sponsorPara = "This video is sponsored by Acme. Use code SAVE at https://acme.example/deal"
	boilerPara  = "Follow me on Twitter @example"
	contentPara = "Today we build a bridge."
)

// contentWriter is a MetadataWriter fake that remembers the description it was
// last given for each path, and reports a change only when it differs, like the
// real writer's byte comparison.
type contentWriter struct {
	mu      sync.Mutex
	written map[string]string
	writes  int
	err     error
}

func newContentWriter() *contentWriter { return &contentWriter{written: map[string]string{}} }

func (w *contentWriter) WriteFor(_ context.Context, path string, m domain.Media, _ string, _ domain.MetadataFormat) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if w.err != nil {
		return false, w.err
	}
	prev, seen := w.written[path]
	w.written[path] = m.Metadata.Description
	return !seen || prev != m.Metadata.Description, nil
}

func (w *contentWriter) WriteShow(context.Context, string, string, string) (bool, error) {
	return false, nil
}

// fakeHistory serves descriptions per uploader and records the queries.
type fakeHistory struct {
	byUploader map[string][]string
	queried    []string
	err        error
	panicWith  any
}

func (f *fakeHistory) DescriptionsByUploader(_ context.Context, u string) ([]string, error) {
	f.queried = append(f.queried, u)
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.byUploader[u], f.err
}

func (f *fakeHistory) CountDescriptionsByUploader(_ context.Context, u string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return len(f.byUploader[u]), nil
}

// recordingMedia wraps the fake media repo and remembers every description any
// write was asked to store, so a test can prove a cleaned copy never reached it.
type recordingMedia struct {
	*fakeMediaRepo
	stored []string
}

func (r *recordingMedia) Upsert(ctx context.Context, m domain.Media) (int64, error) {
	r.stored = append(r.stored, m.Metadata.Description)
	return r.fakeMediaRepo.Upsert(ctx, m)
}

func (r *recordingMedia) UpsertBatch(ctx context.Context, ms []domain.Media) ([]int64, error) {
	for _, m := range ms {
		r.stored = append(r.stored, m.Metadata.Description)
	}
	return r.fakeMediaRepo.UpsertBatch(ctx, ms)
}

type descHarness struct {
	*harness
	writer  *contentWriter
	history *fakeHistory
	rec     *recordingMedia
	src     domain.Source
	profile int64
}

// newDescHarness builds a service with cleanup wired in and one source whose
// cleanup is set to mode.
func newDescHarness(t *testing.T, mode domain.DescriptionCleanup, patterns ...string) *descHarness {
	t.Helper()
	h := newHarness(t)
	d := &descHarness{harness: h, writer: newContentWriter(), history: &fakeHistory{byUploader: map[string][]string{}}}
	d.rec = &recordingMedia{fakeMediaRepo: h.media}
	h.svc.deps.Metadata = d.writer
	h.svc.deps.Media = d.rec
	h.svc.deps.Cleaner = descclean.NewCleaner()
	h.svc.deps.History = d.history
	d.profile = seedEpisodeProfile(t, h)

	in := validInput(d.profile)
	in.DescriptionCleanup = mode
	in.DescriptionStripPatterns = patterns
	src, err := h.svc.AddSource(context.Background(), in)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	d.src = src
	return d
}

// addDownloaded stores a downloaded item with the given description and returns it.
func (d *descHarness) addDownloaded(t *testing.T, ext, uploader, description string) domain.Media {
	t.Helper()
	path := filepath.Join(d.mediaDir, "My Channel", "Season 2026", ext+".mkv")
	writeFile(t, path)
	m := domain.Media{
		SourceID: d.src.ID, ExternalID: ext, Status: domain.MediaDownloaded, FilePath: path,
		Metadata: domain.MediaMetadata{Title: ext, Description: description, Uploader: uploader, UploadDate: d.now},
	}
	id, err := d.media.Upsert(context.Background(), m) // seed through the plain fake, not the recorder
	if err != nil {
		t.Fatal(err)
	}
	m.ID = id
	return m
}

func (d *descHarness) setSource(t *testing.T, mutate func(*domain.Source)) {
	t.Helper()
	s, _ := d.sources.Get(context.Background(), d.src.ID)
	mutate(&s)
	d.sources.items[s.ID] = s
	d.src = s
}

func (d *descHarness) writtenFor(m domain.Media) string { return d.writer.written[m.FilePath] }

func joinParas(p ...string) string { return strings.Join(p, "\n\n") }

func TestDescription_AC1_OffWritesStoredMediaUntouched(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOff)
	m := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, sponsorPara))

	got := d.svc.forOutput(context.Background(), d.src, m, nil)
	if got.Metadata.Description != m.Metadata.Description {
		t.Errorf("Off changed the description: %q", got.Metadata.Description)
	}

	d.svc.finalizeDownload(context.Background(), d.src, m, m.FilePath, domain.MediaProfile{MetadataFormat: domain.MetadataEpisode})
	if d.writtenFor(m) != m.Metadata.Description {
		t.Errorf("sidecar got %q, want the stored description", d.writtenFor(m))
	}
	if d.history.queried != nil {
		t.Errorf("Off must not read channel history, got %v", d.history.queried)
	}
	items := d.feed.lastItems
	if len(items) != 1 || items[0].Metadata.Description != m.Metadata.Description {
		t.Errorf("feed items = %+v, want the stored description", items)
	}
}

func TestDescription_ZeroValueSourceBehavesAsOff(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOff)
	src := d.src
	src.DescriptionCleanup = ""
	m := domain.Media{Metadata: domain.MediaMetadata{Description: joinParas(contentPara, sponsorPara)}}
	if got := d.svc.forOutput(context.Background(), src, m, nil); got.Metadata.Description != m.Metadata.Description {
		t.Error("an unset mode must not clean")
	}
}

func TestDescription_AC2_RepetitionUsesStoredChannelHistory(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	for i := 0; i < 5; i++ {
		desc := fmt.Sprintf("Episode %d", i)
		if i < 3 {
			desc += "\n\n" + boilerPara
		}
		d.history.byUploader["Chan"] = append(d.history.byUploader["Chan"], desc)
	}
	m := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, boilerPara))

	got := d.svc.forOutput(context.Background(), d.src, m, nil)
	if got.Metadata.Description != contentPara {
		t.Errorf("description = %q, want the repeated paragraph removed", got.Metadata.Description)
	}
}

func TestDescription_AC3_HistoryIsKeyedByUploaderOnly(t *testing.T) {
	// A playlist source and the singles bucket both judge a video against its own
	// channel's history; the port takes no source argument at all.
	for _, ct := range []domain.CollectionType{domain.CollectionChannel, domain.CollectionPlaylist, domain.CollectionSingles} {
		t.Run(string(ct), func(t *testing.T) {
			d := newDescHarness(t, domain.DescriptionCleanupOn)
			for i := 0; i < 5; i++ {
				d.history.byUploader["Other Chan"] = append(d.history.byUploader["Other Chan"],
					fmt.Sprintf("video %d\n\n%s", i, boilerPara))
			}
			src := d.src
			src.CollectionType = ct
			m := domain.Media{Metadata: domain.MediaMetadata{Uploader: " Other Chan ", Description: joinParas(contentPara, boilerPara)}}
			got := d.svc.forOutput(context.Background(), src, m, nil)
			if got.Metadata.Description != contentPara {
				t.Errorf("description = %q, want the channel's repeated paragraph removed", got.Metadata.Description)
			}
			if len(d.history.queried) != 1 || d.history.queried[0] != "Other Chan" {
				t.Errorf("history queries = %q, want one for the trimmed uploader", d.history.queried)
			}
		})
	}
}

func TestDescription_AC3_EmptyUploaderSkipsRepetition(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	m := domain.Media{Metadata: domain.MediaMetadata{Uploader: "  ", Description: joinParas(contentPara, "tail")}}
	got := d.svc.forOutput(context.Background(), d.src, m, nil)
	if got.Metadata.Description != m.Metadata.Description || d.history.queried != nil {
		t.Errorf("empty uploader: description %q, history queried %v", got.Metadata.Description, d.history.queried)
	}
}

func TestDescription_AC4AndAC5_SponsorAndUserPatternsApplyToSidecarsAndFeed(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn, "merch store")
	m := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, sponsorPara, "Visit the Merch Store today"))

	changed, err := d.svc.RefreshSourceSidecars(context.Background(), d.src.ID)
	if err != nil || changed != 1 {
		t.Fatalf("RefreshSourceSidecars = %d, %v; want 1, nil", changed, err)
	}
	if d.writtenFor(m) != contentPara {
		t.Errorf("sidecar description = %q, want %q", d.writtenFor(m), contentPara)
	}
	if len(d.feed.lastItems) != 1 || d.feed.lastItems[0].Metadata.Description != contentPara {
		t.Errorf("feed items = %+v, want the cleaned description", d.feed.lastItems)
	}
}

func TestDescription_AC7_AllRemovedKeepsOriginal(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn, "bridge")
	m := d.addDownloaded(t, "a", "Chan", contentPara)
	if got := d.svc.forOutput(context.Background(), d.src, m, nil); got.Metadata.Description != contentPara {
		t.Errorf("description = %q, want the original", got.Metadata.Description)
	}
}

func TestDescription_AC8_TurningOffRestoresOriginalEverywhere(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	original := joinParas(contentPara, sponsorPara)
	m := d.addDownloaded(t, "a", "Chan", original)
	ctx := context.Background()

	d.svc.RefreshSourceSidecars(ctx, d.src.ID)
	if d.writtenFor(m) != contentPara {
		t.Fatalf("setup: sidecar = %q, want cleaned", d.writtenFor(m))
	}
	d.setSource(t, func(s *domain.Source) { s.DescriptionCleanup = domain.DescriptionCleanupOff })
	changed, _ := d.svc.RefreshSourceSidecars(ctx, d.src.ID)
	if changed != 1 || d.writtenFor(m) != original {
		t.Errorf("after Off: changed = %d, sidecar = %q; want the original restored", changed, d.writtenFor(m))
	}
	if got := d.feed.lastItems[0].Metadata.Description; got != original {
		t.Errorf("feed description = %q, want the original", got)
	}
	if d.src.CleansDescriptions() {
		t.Error("source should be off")
	}
}

func TestDescription_AC11_SecondPassWritesNothingAndChangeRewritesOnlyAffected(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	a := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, sponsorPara))
	d.addDownloaded(t, "b", "Chan", "Plain description with no boilerplate")
	ctx := context.Background()

	first, _ := d.svc.RefreshSourceSidecars(ctx, d.src.ID)
	second, _ := d.svc.RefreshSourceSidecars(ctx, d.src.ID)
	if first != 2 || second != 0 {
		t.Errorf("passes changed %d then %d, want 2 then 0", first, second)
	}

	d.setSource(t, func(s *domain.Source) { s.DescriptionStripPatterns = []string{"bridge"} })
	third, _ := d.svc.RefreshSourceSidecars(ctx, d.src.ID)
	// "a" now has its only remaining paragraph matching a pattern, so the original
	// returns; "b" is untouched by the new pattern.
	if third != 1 || d.writtenFor(a) != a.Metadata.Description {
		t.Errorf("after pattern change: changed %d, sidecar %q", third, d.writtenFor(a))
	}
}

func TestDescription_AC12_SameInputGivesSameOutput(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn, "bridge")
	m := d.addDownloaded(t, "a", "Chan", joinParas("intro", contentPara, sponsorPara, "outro"))
	want := d.svc.forOutput(context.Background(), d.src, m, nil).Metadata.Description
	for i := 0; i < 50; i++ {
		if got := d.svc.forOutput(context.Background(), d.src, m, nil).Metadata.Description; got != want {
			t.Fatalf("run %d: %q, want %q", i, got, want)
		}
	}
	if want != "intro\n\noutro" {
		t.Errorf("cleaned = %q", want)
	}
}

func TestDescription_CleanedCopyIsNeverStored(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	original := joinParas(contentPara, sponsorPara)
	m := d.addDownloaded(t, "a", "Chan", original)
	ctx := context.Background()

	d.svc.finalizeDownload(ctx, d.src, m, m.FilePath, domain.MediaProfile{MetadataFormat: domain.MetadataEpisode})
	d.svc.RefreshSourceSidecars(ctx, d.src.ID)
	d.svc.refreshEpisodeMetadata(ctx, d.src.ID, d.svc.newPathResolver(), m, m.FilePath)

	if len(d.rec.stored) != 0 {
		t.Errorf("repository writes during output = %q, want none", d.rec.stored)
	}
	stored, _ := d.media.Get(ctx, m.ID)
	if stored.Metadata.Description != original {
		t.Errorf("stored description = %q, want it unchanged", stored.Metadata.Description)
	}
}

func TestDescription_RenamePassKeepsCleanup(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	m := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, sponsorPara))
	d.svc.refreshEpisodeMetadata(context.Background(), d.src.ID, d.svc.newPathResolver(), m, m.FilePath)
	if d.writtenFor(m) != contentPara {
		t.Errorf("rename-time sidecar = %q, want the cleaned description", d.writtenFor(m))
	}
}

func TestDescription_AC13_FailuresFallBackToTheOriginal(t *testing.T) {
	original := joinParas(contentPara, sponsorPara)
	tests := []struct {
		name  string
		setup func(d *descHarness)
		desc  string
		want  string
	}{
		{"history error keeps the other layers", func(d *descHarness) { d.history.err = errors.New("db down") }, original, contentPara},
		{"history panic", func(d *descHarness) { d.history.panicWith = "boom" }, original, original},
		{"oversized description", func(d *descHarness) {}, strings.Repeat("a", descclean.MaxDescriptionBytes+1), strings.Repeat("a", descclean.MaxDescriptionBytes+1)},
		{"exhausted budget", func(d *descHarness) { d.svc.deps.BatchBudget = time.Nanosecond }, original, original},
		{"stored hostile patterns", func(d *descHarness) {
			d.setSource(t, func(s *domain.Source) { s.DescriptionStripPatterns = []string{`\pL{1000}`} })
		}, original, contentPara},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDescHarness(t, domain.DescriptionCleanupOn)
			tc.setup(d)
			m := d.addDownloaded(t, "a", "Chan", tc.desc)
			// A history row is needed so the history port is consulted.
			d.history.byUploader["Chan"] = []string{"x"}
			if tc.name == "exhausted budget" {
				time.Sleep(time.Millisecond)
			}
			got := d.svc.forOutput(context.Background(), d.src, m, nil)
			if got.Metadata.Description != tc.want {
				t.Errorf("description = %q, want %q", got.Metadata.Description, tc.want)
			}
			if _, err := d.svc.RefreshSourceSidecars(context.Background(), d.src.ID); err != nil {
				t.Errorf("a cleanup failure must not fail the refresh: %v", err)
			}
		})
	}
}

// captureLogs routes slog output to a buffer for the duration of a test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestDescription_LogsCarryNoDescriptionTextAndWarnOncePerBatch(t *testing.T) {
	buf := captureLogs(t)
	canary := "CANARY-DESCRIPTION-TEXT-9f3a"
	longPattern := strings.Repeat("PATTERNCANARY", 6) + `\pL{1000}`
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	d.setSource(t, func(s *domain.Source) { s.DescriptionStripPatterns = []string{longPattern} })
	d.history.err = errors.New("db down: " + canary)
	for i := 0; i < 10; i++ {
		d.addDownloaded(t, fmt.Sprintf("v%d", i), "Chan", joinParas(canary, sponsorPara))
	}
	if _, err := d.svc.RefreshSourceSidecars(context.Background(), d.src.ID); err != nil {
		t.Fatal(err)
	}

	logs := buf.String()
	if strings.Contains(logs, canary) {
		t.Errorf("logs contain description text:\n%s", logs)
	}
	if strings.Contains(logs, "PATTERNCANARYPATTERNCANARY") {
		t.Errorf("logs contain more than the short pattern excerpt:\n%s", logs)
	}
	for _, class := range []string{"history_unavailable", "pattern_rejected"} {
		if n := strings.Count(logs, "class="+class); n != 1 {
			t.Errorf("class %s logged %d times for a 10-item batch, want 1", class, n)
		}
	}
}

func TestDescription_BatchBudgetCoversWholeFeed(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	var items []domain.Media
	for i := 0; i < 20; i++ {
		items = append(items, domain.Media{Status: domain.MediaDownloaded,
			Metadata: domain.MediaMetadata{Description: joinParas(fmt.Sprintf("item %d", i), sponsorPara)}})
	}
	items = append(items, domain.Media{Status: domain.MediaPending,
		Metadata: domain.MediaMetadata{Description: joinParas("pending", sponsorPara)}})
	buf := captureLogs(t)

	d.svc.deps.BatchBudget = time.Nanosecond
	time.Sleep(time.Millisecond)
	out := d.svc.forOutputBatch(context.Background(), d.src, items, nil)
	for i, m := range out {
		if m.Metadata.Description != items[i].Metadata.Description {
			t.Fatalf("item %d changed under an exhausted budget", i)
		}
	}
	if n := strings.Count(buf.String(), "class=timeout"); n != 1 {
		t.Errorf("budget warning logged %d times, want once per batch", n)
	}

	d.svc.deps.BatchBudget = time.Minute
	out = d.svc.forOutputBatch(context.Background(), d.src, items, nil)
	if out[0].Metadata.Description != "item 0" {
		t.Errorf("downloaded item = %q, want cleaned", out[0].Metadata.Description)
	}
	if out[20].Metadata.Description != items[20].Metadata.Description {
		t.Error("items that are not downloaded are not shown in a feed and must be left alone")
	}
}

func TestDescription_OffBatchReturnsInputSlice(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOff)
	items := []domain.Media{{Status: domain.MediaDownloaded}}
	out := d.svc.forOutputBatch(context.Background(), d.src, items, nil)
	if &out[0] != &items[0] {
		t.Error("Off must pass the items through untouched")
	}
}

func TestDescription_NilCleanerMeansNoCleanup(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	d.svc.deps.Cleaner = nil
	m := domain.Media{Metadata: domain.MediaMetadata{Description: joinParas(contentPara, sponsorPara)}}
	if got := d.svc.forOutput(context.Background(), d.src, m, nil); got.Metadata.Description != m.Metadata.Description {
		t.Error("without a cleaner nothing may be cleaned")
	}
}

func TestDescription_AC5_InvalidPatternsRejectedBeforeAnyWrite(t *testing.T) {
	h := newHarness(t)
	profile := h.seedProfile(t)
	ctx := context.Background()
	many := make([]string, 21)
	for i := range many {
		many[i] = fmt.Sprintf("p%d", i)
	}
	tests := []struct {
		name string
		in   []string
		want error
	}{
		{"syntax", []string{"(unclosed"}, descclean.ErrSyntax},
		{"too many", many, descclean.ErrTooMany},
		{"too long", []string{strings.Repeat("a", 501)}, descclean.ErrTooLong},
		{"hostile repeat", []string{`\pL{1000}`}, descclean.ErrTooComplex},
		{"nested repeat", []string{`(\pL{50}){50}`}, descclean.ErrTooComplex},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput(profile)
			in.DescriptionCleanup = domain.DescriptionCleanupOn
			in.DescriptionStripPatterns = tc.in
			_, err := h.svc.AddSource(ctx, in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("AddSource error = %v, want %v", err, tc.want)
			}
			if len(h.sources.items) != 0 {
				t.Error("an invalid pattern must never be stored")
			}
		})
	}
}

func TestDescription_InvalidCleanupModeRejected(t *testing.T) {
	h := newHarness(t)
	in := validInput(h.seedProfile(t))
	in.DescriptionCleanup = "maybe"
	if _, err := h.svc.AddSource(context.Background(), in); err == nil {
		t.Error("an unknown mode must be rejected")
	}
}

func TestDescription_AddSourceDefaultsOffAndNormalisesPatterns(t *testing.T) {
	h := newHarness(t)
	in := validInput(h.seedProfile(t))
	in.DescriptionStripPatterns = []string{" a ", "", "a", "b"}
	src, err := h.svc.AddSource(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if src.DescriptionCleanup != domain.DescriptionCleanupOff {
		t.Errorf("mode = %q, want off by default", src.DescriptionCleanup)
	}
	if got := strings.Join(src.DescriptionStripPatterns, ","); got != "a,b" {
		t.Errorf("patterns = %q, want a,b", got)
	}
}

func refreshTasks(h *harness) []jobs.Task {
	var out []jobs.Task
	for _, task := range h.tasks.tasks {
		if task.Type == jobs.TaskRefreshSidecars {
			out = append(out, task)
		}
	}
	return out
}

func TestDescription_AC11_UpdateSourceQueuesRefreshOnlyWhenSettingsChange(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := validInput(h.seedProfile(t))
	src, _ := h.svc.AddSource(ctx, in)

	in.Name = "Renamed"
	if _, err := h.svc.UpdateSource(ctx, src.ID, in); err != nil {
		t.Fatal(err)
	}
	if n := len(refreshTasks(h)); n != 0 {
		t.Errorf("unrelated edit queued %d refresh tasks, want 0", n)
	}

	in.DescriptionCleanup = domain.DescriptionCleanupOn
	if _, err := h.svc.UpdateSource(ctx, src.ID, in); err != nil {
		t.Fatal(err)
	}
	in.DescriptionStripPatterns = []string{"merch"}
	if _, err := h.svc.UpdateSource(ctx, src.ID, in); err != nil {
		t.Fatal(err)
	}
	tasks := refreshTasks(h)
	if len(tasks) != 2 {
		t.Fatalf("queued %d refresh tasks, want 2 (mode change, pattern change)", len(tasks))
	}
	if tasks[0].SourceID == nil || *tasks[0].SourceID != src.ID {
		t.Errorf("task source = %v, want %d", tasks[0].SourceID, src.ID)
	}

	if _, err := h.svc.UpdateSource(ctx, src.ID, in); err != nil {
		t.Fatal(err)
	}
	if n := len(refreshTasks(h)); n != 2 {
		t.Errorf("resaving identical settings queued a task: %d total", n)
	}
}

type failingEnqueuer struct{ fakeTaskEnqueuer }

func (e *failingEnqueuer) Enqueue(context.Context, jobs.Task) (int64, error) {
	return 0, errors.New("queue down")
}

func TestDescription_UpdateSourceSucceedsWhenTheRefreshCannotBeQueued(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := validInput(h.seedProfile(t))
	src, _ := h.svc.AddSource(ctx, in)
	h.svc.deps.Tasks = &failingEnqueuer{}
	in.DescriptionCleanup = domain.DescriptionCleanupOn
	if _, err := h.svc.UpdateSource(ctx, src.ID, in); err != nil {
		t.Errorf("save must succeed even if the refresh cannot be queued: %v", err)
	}
}

func TestDescription_Preview(t *testing.T) {
	ctx := context.Background()
	t.Run("AC10 off source still shows what would be removed", func(t *testing.T) {
		d := newDescHarness(t, domain.DescriptionCleanupOff, "merch")
		m := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, sponsorPara, "Merch store open"))
		p, err := d.svc.PreviewDescription(ctx, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !p.Available || p.Enabled || p.Unavailable {
			t.Fatalf("preview flags = %+v", p)
		}
		if p.Cleaned != contentPara || p.Original != m.Metadata.Description {
			t.Errorf("cleaned %q original %q", p.Cleaned, p.Original)
		}
		if len(p.Removed) != 2 || p.Removed[0].Reason != "sponsor" || p.Removed[1].Reason != "user_pattern" {
			t.Errorf("removed = %+v", p.Removed)
		}
		if !strings.HasPrefix(p.Removed[1].Label, "Your pattern") {
			t.Errorf("label = %q", p.Removed[1].Label)
		}
	})
	t.Run("on source reports enabled", func(t *testing.T) {
		d := newDescHarness(t, domain.DescriptionCleanupOn)
		m := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, sponsorPara))
		p, _ := d.svc.PreviewDescription(ctx, m.ID)
		if !p.Enabled || p.Cleaned != contentPara {
			t.Errorf("preview = %+v", p)
		}
	})
	t.Run("fallback is flagged", func(t *testing.T) {
		d := newDescHarness(t, domain.DescriptionCleanupOn)
		m := d.addDownloaded(t, "a", "Chan", sponsorPara)
		p, _ := d.svc.PreviewDescription(ctx, m.ID)
		if !p.FellBack || p.Cleaned != sponsorPara || len(p.Removed) != 1 {
			t.Errorf("preview = %+v", p)
		}
	})
	t.Run("deadline gives unavailable without error", func(t *testing.T) {
		d := newDescHarness(t, domain.DescriptionCleanupOn)
		d.svc.deps.PreviewBudget = time.Nanosecond
		m := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, sponsorPara))
		time.Sleep(time.Millisecond)
		p, err := d.svc.PreviewDescription(ctx, m.ID)
		if err != nil || !p.Unavailable || p.UnavailableReason == "" {
			t.Errorf("preview = %+v, err = %v; want unavailable with a reason", p, err)
		}
		if p.Original != m.Metadata.Description {
			t.Error("the original must still be provided")
		}
	})
	t.Run("hostile stored patterns are ignored and flagged", func(t *testing.T) {
		d := newDescHarness(t, domain.DescriptionCleanupOn)
		d.setSource(t, func(s *domain.Source) { s.DescriptionStripPatterns = []string{`\pL{1000}`} })
		m := d.addDownloaded(t, "a", "Chan", joinParas(contentPara, sponsorPara))
		p, _ := d.svc.PreviewDescription(ctx, m.ID)
		if !p.PatternsIgnored || p.Cleaned != contentPara {
			t.Errorf("preview = %+v", p)
		}
	})
	t.Run("no description or no cleaner is not available", func(t *testing.T) {
		d := newDescHarness(t, domain.DescriptionCleanupOn)
		empty := d.addDownloaded(t, "e", "Chan", "")
		if p, _ := d.svc.PreviewDescription(ctx, empty.ID); p.Available {
			t.Error("an empty description has nothing to preview")
		}
		full := d.addDownloaded(t, "f", "Chan", joinParas(contentPara, sponsorPara))
		d.svc.deps.Cleaner = nil
		if p, _ := d.svc.PreviewDescription(ctx, full.ID); p.Available {
			t.Error("without a cleaner there is no preview")
		}
	})
	t.Run("unknown media is an error", func(t *testing.T) {
		d := newDescHarness(t, domain.DescriptionCleanupOn)
		if _, err := d.svc.PreviewDescription(ctx, 999); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestDescription_ChannelStatsAreInvalidatedWhenDescriptionsChange(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	cleaner := d.svc.deps.Cleaner
	d.history.byUploader["Chan"] = []string{"a", "b", "c", "d", "e"}
	ctx := context.Background()
	if _, err := cleaner.Stats(ctx, "Chan", d.history); err != nil {
		t.Fatal(err)
	}
	loads := len(d.history.queried)

	d.svc.invalidateChannel("Chan")
	if _, err := cleaner.Stats(ctx, "Chan", d.history); err != nil {
		t.Fatal(err)
	}
	if len(d.history.queried) != loads+1 {
		t.Error("invalidateChannel must force the next use to rebuild")
	}
	d.svc.deps.Cleaner = nil
	d.svc.invalidateChannel("Chan") // must not panic without a cleaner
}

func TestDescription_ScaleOneChannelLoadsHistoryOnce(t *testing.T) {
	d := newDescHarness(t, domain.DescriptionCleanupOn)
	for i := 0; i < 30; i++ {
		d.history.byUploader["Chan"] = append(d.history.byUploader["Chan"], fmt.Sprintf("v%d\n\n%s", i, boilerPara))
	}
	var items []domain.Media
	for i := 0; i < 3000; i++ {
		items = append(items, domain.Media{Status: domain.MediaDownloaded,
			Metadata: domain.MediaMetadata{Uploader: "Chan", Description: fmt.Sprintf("video %d\n\n%s", i, boilerPara)}})
	}
	d.svc.deps.BatchBudget = time.Minute
	start := time.Now()
	out := d.svc.forOutputBatch(context.Background(), d.src, items, nil)
	if time.Since(start) > 10*time.Second {
		t.Errorf("cleaning 3000 descriptions took %v", time.Since(start))
	}
	if out[2999].Metadata.Description != "video 2999" {
		t.Errorf("last item = %q", out[2999].Metadata.Description)
	}
	if len(d.history.queried) != 1 {
		t.Errorf("history loaded %d times, want once", len(d.history.queried))
	}
}
