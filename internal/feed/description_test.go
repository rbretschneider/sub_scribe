package feed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sub_scribe/internal/domain"
)

func describedItem(desc string) domain.Media {
	m := mediaAt("v1", "One", "/d/v1.mp4", 10, domain.MediaDownloaded, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	m.Metadata.Description = desc
	return m
}

// offGolden is the feed a source without cleanup has always produced: no item
// description element, whatever the stored description holds.
const offGolden = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
  <channel>
    <title>Chan</title>
    <item>
      <title>One</title>
      <guid isPermaLink="false">v1</guid>
      <pubDate>Fri, 02 Jan 2026 03:04:05 +0000</pubDate>
      <enclosure url="media/v1" length="10" type="video/mp4"></enclosure>
    </item>
  </channel>
</rss>
`

func TestBuildRSS_AC1_OffFeedIsByteIdenticalToBefore(t *testing.T) {
	for _, mode := range []domain.DescriptionCleanup{"", domain.DescriptionCleanupOff} {
		got, err := BuildRSS(domain.Source{Name: "Chan", DescriptionCleanup: mode}, []domain.Media{describedItem("Some text")})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != offGolden {
			t.Errorf("mode %q: feed differs from the golden output:\n%s", mode, got)
		}
	}
}

func TestBuildRSS_OnFeedCarriesEscapedDescription(t *testing.T) {
	src := domain.Source{Name: "Chan", DescriptionCleanup: domain.DescriptionCleanupOn}
	got, err := BuildRSS(src, []domain.Media{describedItem(`Fish & <chips> "ok"`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "<description>Fish &amp; &lt;chips&gt; &#34;ok&#34;</description>") {
		t.Errorf("description not escaped as expected:\n%s", got)
	}
}

func TestBuildRSS_OnFeedOmitsEmptyDescription(t *testing.T) {
	src := domain.Source{Name: "Chan", DescriptionCleanup: domain.DescriptionCleanupOn}
	got, _ := BuildRSS(src, []domain.Media{describedItem("")})
	if strings.Contains(string(got), "<description") {
		t.Errorf("empty description must not emit an element:\n%s", got)
	}
}

func TestWriteFeed_UnchangedFeedIsNotRewrittenAndNoTempFileRemains(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir)
	src := domain.Source{ID: 7, Name: "Chan"}
	items := []domain.Media{describedItem("x")}
	ctx := context.Background()
	if err := w.WriteFeed(ctx, src, items); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "7.xml")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFeed(ctx, src, items); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if !info.ModTime().Equal(old) {
		t.Errorf("unchanged feed was rewritten: mtime %v", info.ModTime())
	}

	src.DescriptionCleanup = domain.DescriptionCleanupOn
	if err := w.WriteFeed(ctx, src, items); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "<description>x</description>") {
		t.Error("a changed feed must be rewritten")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the feed", len(entries))
	}
}
