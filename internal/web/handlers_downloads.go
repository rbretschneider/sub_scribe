package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"sub_scribe/internal/library"
)

// downloadFormView is the render model for the one-off download form.
type downloadFormView struct {
	baseView
	Error string
	URL   string
}

// urlFormField carries the pasted video address.
const urlFormField = "url"

// handleDownloadNew renders the form for downloading a single pasted video.
func (s *Server) handleDownloadNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, "download_form", http.StatusOK, downloadFormView{
		baseView: s.newBaseView("Save a video", navLibrary),
	})
}

// handleDownloadCreate queues one pasted video and lands on its library page,
// where the live status shows the download actually happening. A paste that is
// not a single video re-renders the form saying so, with the text preserved.
func (s *Server) handleDownloadCreate(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.PostFormValue(urlFormField))

	id, err := s.deps.Sources.DownloadVideo(r.Context(), rawURL)
	if errors.Is(err, library.ErrNotAVideoURL) {
		s.render(w, "download_form", http.StatusOK, downloadFormView{
			baseView: s.newBaseView("Save a video", navLibrary),
			Error:    "That doesn't look like a link to a single video. Paste a watch, youtu.be, or Shorts link — channels and playlists belong in Sources.",
			URL:      rawURL,
		})
		return
	}
	if err != nil {
		http.Error(w, "could not queue that video", http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/library/"+strconv.FormatInt(id, 10))
}
