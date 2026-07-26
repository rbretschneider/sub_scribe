package naming

import (
	"testing"
	"time"

	"sub_scribe/internal/domain"
)

// sampleContext builds a representative context for render tests.
func sampleContext() Context {
	return Context{
		SourceName: "Tech Channel",
		ExternalID: "dQw4w9WgXcQ",
		Media: domain.MediaMetadata{
			Title:      "Intro to Go",
			Uploader:   "Tech Channel",
			UploadDate: time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC),
		},
	}
}

func TestRenderPlexStyleTemplate(t *testing.T) {
	r := NewRenderer()
	template := "{{ source_name }}/Season {{ upload_year }}/{{ title }} [{{ id }}]"

	got, err := r.Render(template, sampleContext())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := "Tech Channel/Season 2026/Intro to Go [dQw4w9WgXcQ]"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestRenderSanitizesIllegalCharactersInValues(t *testing.T) {
	r := NewRenderer()
	ctx := sampleContext()
	// A title with a slash must not create a directory boundary, and colons must
	// be stripped for Windows/SMB safety.
	ctx.Media.Title = "AC/DC: Live?"

	got, err := r.Render("{{ source_name }}/{{ title }}", ctx)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := "Tech Channel/AC DC Live"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestRenderRejectsUnknownVariable(t *testing.T) {
	r := NewRenderer()
	_, err := r.Render("{{ source_name }}/{{ bogus }}", sampleContext())
	if err == nil {
		t.Fatal("expected error for unknown variable, got nil")
	}
}

func TestRenderRejectsParentTraversal(t *testing.T) {
	r := NewRenderer()
	ctx := sampleContext()
	ctx.Media.Title = ".."
	// ".." sanitizes away trailing dots, so craft it via the template literally.
	_, err := r.Render("{{ source_name }}/../escape", ctx)
	if err == nil {
		t.Fatal("expected error for parent-directory traversal, got nil")
	}
}

func TestRenderCollapsesEmptySegments(t *testing.T) {
	r := NewRenderer()
	ctx := sampleContext()
	ctx.SourceName = "" // empty source name becomes the fallback, not an empty dir

	got, err := r.Render("{{ source_name }}/{{ title }}", ctx)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	want := "_/Intro to Go"
	if got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
}

func TestValidateRejectsEmptyTemplate(t *testing.T) {
	r := NewRenderer()
	if err := r.Validate("   "); err == nil {
		t.Fatal("expected error for empty template, got nil")
	}
}

func TestValidateAcceptsAllKnownVariables(t *testing.T) {
	r := NewRenderer()
	template := "{{ source_name }}/{{ uploader }}/{{ upload_date }}/{{ season }}/" +
		"{{ episode }}/{{ upload_year }}/{{ title }}/{{ id }}"
	if err := r.Validate(template); err != nil {
		t.Errorf("Validate() unexpected error = %v", err)
	}
}
