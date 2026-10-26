package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

func validFormValues() sourceFormValues {
	return sourceFormValues{
		Name: "C", URL: "https://youtube.com/@c", CollectionType: "channel",
		MediaProfileID: "1", CookieBehavior: "when_needed", FrequencyHours: "6",
		ShortsRule: "include", LivestreamsRule: "include",
	}
}

func TestSourceForm_DescriptionCleanupDefaultsOff(t *testing.T) {
	for _, mode := range []string{"", "off"} {
		v := validFormValues()
		v.DescriptionCleanup = mode
		in, err := v.toInput()
		if err != nil || in.DescriptionCleanup != domain.DescriptionCleanupOff {
			t.Errorf("mode %q: got %q, %v; want off", mode, in.DescriptionCleanup, err)
		}
	}
	v := validFormValues()
	v.DescriptionCleanup = "on"
	if in, _ := v.toInput(); in.DescriptionCleanup != domain.DescriptionCleanupOn {
		t.Error("on was not carried through")
	}
	v.DescriptionCleanup = "sometimes"
	if _, err := v.toInput(); err == nil || !strings.Contains(err.Error(), "clean descriptions") {
		t.Errorf("err = %v, want a message about the cleanup choice", err)
	}
}

func TestSourceForm_StripPatternsOnePerLine(t *testing.T) {
	v := validFormValues()
	v.StripPatterns = "merch\r\n\r\n  follow me  \nmerch\n"
	in, err := v.toInput()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(in.DescriptionStripPatterns, "|"); got != "merch|follow me" {
		t.Errorf("patterns = %q, want merch|follow me", got)
	}
}

func TestSourceForm_RejectsBadPatternsWithAMessage(t *testing.T) {
	many := strings.Repeat("a\n", 3) // placeholder replaced below
	var sb strings.Builder
	for i := 0; i < 21; i++ {
		sb.WriteString("word" + strings.Repeat("x", i) + "\n")
	}
	many = sb.String()
	tests := []struct {
		name, in, want string
	}{
		{"syntax", "(unclosed", "isn't valid"},
		{"hostile repeat", `\pL{1000}`, "more than 100 times or is too complex"},
		{"nested", `(\pL{50}){50}`, "more than 100 times or is too complex"},
		{"too many", many, "at most 20"},
		{"too long", strings.Repeat("a", 501), "longer than 500"},
		{"combined", "\\pL{15}a\n\\pL{15}b\n\\pL{15}c\n\\pL{15}d", "too complex together"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := validFormValues()
			v.StripPatterns = tc.in
			_, err := v.toInput()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
			if err != nil && len(err.Error()) > 200 {
				t.Errorf("message is %d characters; it must not echo the pattern", len(err.Error()))
			}
		})
	}
}

func TestSourceForm_FromSourceRoundTripsCleanupFields(t *testing.T) {
	got := fromSource(domain.Source{
		DescriptionCleanup:       domain.DescriptionCleanupOn,
		DescriptionStripPatterns: []string{"a", "b"},
	})
	if got.DescriptionCleanup != "on" || got.StripPatterns != "a\nb" {
		t.Errorf("fromSource = %q, %q", got.DescriptionCleanup, got.StripPatterns)
	}
	if fromSource(domain.Source{}).DescriptionCleanup != "off" {
		t.Error("a source without the field must render as off")
	}
}

func postSource(server *Server, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/sources", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func TestCreateSource_BadPatternStoresNothingAndKeepsInput(t *testing.T) {
	sources := &fakeSources{}
	server := newTestServer(t, sources, &fakeProfiles{}, "")
	form := url.Values{
		"url": {"https://youtube.com/@c"}, "collection_type": {"channel"}, "media_profile_id": {"1"},
		"cookie_behavior": {"when_needed"}, "frequency_hours": {"6"}, "shorts_rule": {"include"},
		"livestreams_rule": {"include"}, "description_cleanup": {"on"},
		"description_strip_patterns": {"keepme\n(unclosed"},
	}
	rec := postSource(server, form)
	if sources.addCalls != 0 {
		t.Errorf("AddSource called %d times, want 0 for an invalid pattern", sources.addCalls)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want the form re-rendered", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "isn&#39;t valid") || !strings.Contains(body, "keepme") {
		t.Errorf("body should show the message and keep the typed patterns:\n%s", body)
	}
}

func TestCreateSource_PassesCleanupSettingsToTheService(t *testing.T) {
	sources := &fakeSources{}
	server := newTestServer(t, sources, &fakeProfiles{}, "")
	form := url.Values{
		"url": {"https://youtube.com/@c"}, "collection_type": {"channel"}, "media_profile_id": {"1"},
		"cookie_behavior": {"when_needed"}, "frequency_hours": {"6"}, "shorts_rule": {"include"},
		"livestreams_rule": {"include"}, "description_cleanup": {"on"},
		"description_strip_patterns": {"merch"},
	}
	postSource(server, form)
	if sources.addCalls != 1 || sources.added.DescriptionCleanup != domain.DescriptionCleanupOn ||
		len(sources.added.DescriptionStripPatterns) != 1 {
		t.Errorf("added = %+v after %d calls", sources.added, sources.addCalls)
	}
}

func TestSourceForm_OffersCleanupControls(t *testing.T) {
	server := newTestServer(t, &fakeSources{}, &fakeProfiles{}, "")
	body := get(server, "/sources/new").Body.String()
	for _, want := range []string{`name="description_cleanup"`, `name="description_strip_patterns"`, ">Clean up descriptions<"} {
		if !strings.Contains(body, want) {
			t.Errorf("source form missing %q", want)
		}
	}
}

type fakePreview struct {
	preview library.DescriptionPreview
	err     error
}

func (f fakePreview) PreviewDescription(context.Context, int64) (library.DescriptionPreview, error) {
	return f.preview, f.err
}

func previewServer(t *testing.T, p library.DescriptionPreviewer) *Server {
	t.Helper()
	lib := &fakeLibrary{item: library.MediaListItem{
		Media: domain.Media{ID: 99, SourceID: 7, ExternalID: "abc", Status: domain.MediaDownloaded,
			Metadata: domain.MediaMetadata{Title: "T", Description: "ORIGINAL TEXT"}},
	}}
	jobsFake := &fakeJobs{}
	server, err := NewServer(ServerDeps{
		Sources: &fakeSources{}, Profiles: &fakeProfiles{}, Library: lib, Media: &fakeMediaService{},
		Jobs: jobsFake, Queue: jobsFake, Logs: applog.NewBuffer(0), Preview: p,
		CookiesPath: "", Clock: fixedClock{now: testNow}, EventsPath: "/events",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestMediaDetail_AC10_ShowsCleanedTextAndEachReason(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		p := library.DescriptionPreview{
			Available: true, Enabled: enabled, Original: "ORIGINAL TEXT", Cleaned: "CLEANED <b>TEXT</b>", SourceID: 7,
			Removed: []library.RemovedParagraph{
				{Text: "<script>alert(1)</script>", Reason: "sponsor", Label: "Sponsor rule: sponsored by plus a link"},
				{Text: "second", Reason: "repeated", Label: "Repeated in channel: in 7 of 9 videos from this channel"},
			},
		}
		body := get(previewServer(t, fakePreview{preview: p}), "/library/99").Body.String()
		for _, want := range []string{
			"ORIGINAL TEXT", "CLEANED &lt;b&gt;TEXT&lt;/b&gt;", "Sponsor rule: sponsored by plus a link",
			"Repeated in channel: in 7 of 9 videos", "&lt;script&gt;alert(1)&lt;/script&gt;", `href="/sources/7/edit"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("enabled=%v: page missing %q", enabled, want)
			}
		}
		if strings.Contains(body, "<script>alert(1)") {
			t.Errorf("enabled=%v: removed text was not escaped", enabled)
		}
		wantStatus := "Cleanup is off for this source"
		if enabled {
			wantStatus = "Cleanup is on for this source"
		}
		if !strings.Contains(body, wantStatus) {
			t.Errorf("enabled=%v: missing status line %q", enabled, wantStatus)
		}
	}
}

func TestMediaDetail_PreviewStates(t *testing.T) {
	tests := []struct {
		name    string
		preview fakePreview
		want    []string
		not     []string
	}{
		{"unavailable still returns the page",
			fakePreview{preview: library.DescriptionPreview{Available: true, Unavailable: true, UnavailableReason: "the description could not be analysed in time", SourceID: 7}},
			[]string{"Preview unavailable", "ORIGINAL TEXT"}, nil},
		{"fell back is explained",
			fakePreview{preview: library.DescriptionPreview{Available: true, FellBack: true, Cleaned: "ORIGINAL TEXT", SourceID: 7}},
			[]string{"Every paragraph matched"}, nil},
		{"nothing removed",
			fakePreview{preview: library.DescriptionPreview{Available: true, Cleaned: "ORIGINAL TEXT", SourceID: 7}},
			[]string{"Nothing would be removed"}, nil},
		{"error hides the panel but not the page",
			fakePreview{err: errors.New("boom")}, []string{"ORIGINAL TEXT"}, []string{"Cleaned description"}},
		{"not available hides the panel",
			fakePreview{}, []string{"ORIGINAL TEXT"}, []string{"Cleaned description"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(previewServer(t, tc.preview), "/library/99")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(body, n) {
					t.Errorf("unexpected %q", n)
				}
			}
		})
	}
}

func TestMediaDetail_WithoutPreviewerRendersAsBefore(t *testing.T) {
	body := get(previewServer(t, nil), "/library/99").Body.String()
	if !strings.Contains(body, "ORIGINAL TEXT") || strings.Contains(body, "Cleaned description") {
		t.Error("a nil previewer must hide the panel only")
	}
}

func TestSourceDetail_ShowsDescriptionCleanupSummary(t *testing.T) {
	tests := []struct {
		name   string
		source domain.Source
		want   string
	}{
		{"off", domain.Source{ID: 7, Name: "C"}, "<dd>Off</dd>"},
		{"on with patterns", domain.Source{ID: 7, Name: "C", DescriptionCleanup: domain.DescriptionCleanupOn,
			DescriptionStripPatterns: []string{"a", "b"}}, "On (2 custom patterns)"},
		{"one pattern", domain.Source{ID: 7, Name: "C", DescriptionStripPatterns: []string{"a"}}, "Off (1 custom pattern)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t, &fakeSources{getResult: tc.source}, &fakeProfiles{}, "")
			body := get(server, "/sources/7").Body.String()
			if !strings.Contains(body, tc.want) {
				t.Errorf("source page missing %q", tc.want)
			}
		})
	}
}
