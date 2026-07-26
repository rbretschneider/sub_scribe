package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
	"sub_scribe/internal/store"
)

// youtubeWatchBase is where a video's external id resolves to a watch page, so
// the UI can offer "open the original" next to a failure.
const youtubeWatchBase = "https://www.youtube.com/watch?v="

// mediaDetailView is the render model for a single archived item.
type mediaDetailView struct {
	baseView
	Item     library.MediaListItem
	WatchURL string
	// CanRetry is false while the item is already queued or in flight, so the
	// button is not offered when it would do nothing.
	CanRetry bool
}

// handleMediaDetail renders everything known about one item, including the full
// error text that the library grid can only hint at.
func (s *Server) handleMediaDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}

	item, err := s.deps.Library.GetMedia(r.Context(), id)
	if err != nil {
		if store.IsNotFound(err) {
			http.Error(w, "no such video", http.StatusNotFound)
			return
		}
		http.Error(w, "could not load that video", http.StatusInternalServerError)
		return
	}

	view := mediaDetailView{
		baseView: s.newBaseView(item.Media.Metadata.Title, navLibrary),
		Item:     item,
		WatchURL: watchURLFor(item.Media.ExternalID),
		CanRetry: canRetryMedia(item.Media.Status),
	}
	s.render(w, "media_detail", http.StatusOK, view)
}

// handleMediaRetry requeues one item and returns to its detail page.
func (s *Server) handleMediaRetry(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}

	if err := s.deps.Media.RetryMedia(r.Context(), id); err != nil {
		http.Error(w, "could not queue that video for another attempt", http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/library/"+strconv.FormatInt(id, 10))
}

// thumbnailExtensions are the sidecar image formats to look for, in preference
// order. jpg is what the downloader is told to produce; the others cover files
// fetched before that setting existed, or put there by hand.
var thumbnailExtensions = []string{".jpg", ".jpeg", ".png", ".webp"}

// thumbnailContentTypes maps a sidecar extension to what it should be served as.
var thumbnailContentTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".webp": "image/webp",
}

// handleMediaThumbnail serves the artwork saved beside a downloaded video.
//
// The path is never taken from the request: it is derived from the file path
// recorded for that media id, so a crafted URL cannot reach anything the
// downloader did not write. A missing thumbnail is a plain 404, which lets the
// library grid fall back to its placeholder without a broken image.
func (s *Server) handleMediaThumbnail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseIDParam(w, r)
	if !ok {
		return
	}

	item, err := s.deps.Library.GetMedia(r.Context(), id)
	if err != nil || item.Media.FilePath == "" {
		http.NotFound(w, r)
		return
	}

	path, contentType, ok := findThumbnail(item.Media.FilePath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	// Artwork for a given video never changes, and the URL is per media id.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, path)
}

// findThumbnail locates the sidecar image sharing a media file's base name.
func findThumbnail(mediaPath string) (string, string, bool) {
	base := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath))
	for _, ext := range thumbnailExtensions {
		candidate := base + ext
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, thumbnailContentTypes[ext], true
		}
	}
	return "", "", false
}

// canRetryMedia reports whether asking for the item again would do anything —
// it would not while a download is already queued or running.
func canRetryMedia(status domain.MediaStatus) bool {
	return status != domain.MediaPending && status != domain.MediaDownloading
}

// watchURLFor builds the provider watch URL for an external id, or empty when
// there is no id to link to.
func watchURLFor(externalID string) string {
	if externalID == "" {
		return ""
	}
	return youtubeWatchBase + externalID
}
