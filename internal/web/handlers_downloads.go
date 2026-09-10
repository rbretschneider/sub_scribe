package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"sub_scribe/internal/domain"
	"sub_scribe/internal/library"
)

// downloadFormView is the render model for the one-off download form. Profiles
// feed the "save as" picker, which is what routes a real movie into a Plex
// library instead of the YouTube archive.
type downloadFormView struct {
	baseView
	Error     string
	URL       string
	Profiles  []domain.MediaProfile
	ProfileID int64
}

// urlFormField carries the pasted video address; profileFormField the chosen
// media profile (empty or zero = the default profile).
const (
	urlFormField     = "url"
	profileFormField = "profile_id"
)

// handleDownloadNew renders the form for downloading a single pasted video.
func (s *Server) handleDownloadNew(w http.ResponseWriter, r *http.Request) {
	s.renderDownloadForm(w, r, downloadFormView{})
}

// handleDownloadCreate queues one pasted video under the chosen profile and
// lands on its library page, where the live status shows the download actually
// happening. A paste that is not a single video re-renders the form saying so,
// with the text and profile choice preserved.
func (s *Server) handleDownloadCreate(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.PostFormValue(urlFormField))
	profileID, _ := strconv.ParseInt(r.PostFormValue(profileFormField), 10, 64)
	if profileID < 0 {
		profileID = 0
	}

	id, err := s.deps.Sources.DownloadVideo(r.Context(), rawURL, profileID)
	if errors.Is(err, library.ErrNotAVideoURL) {
		s.renderDownloadForm(w, r, downloadFormView{
			Error:     "That doesn't look like a link to a single video. Paste a watch, youtu.be, or Shorts link — channels and playlists belong in Sources.",
			URL:       rawURL,
			ProfileID: profileID,
		})
		return
	}
	if err != nil {
		http.Error(w, "could not queue that video", http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/library/"+strconv.FormatInt(id, 10))
}

// renderDownloadForm fills the shared parts of the form view — the base layout
// and the profile options — around whatever the caller set.
func (s *Server) renderDownloadForm(w http.ResponseWriter, r *http.Request, view downloadFormView) {
	profiles, err := s.deps.Profiles.ListProfiles(r.Context())
	if err != nil {
		http.Error(w, "could not load profiles", http.StatusInternalServerError)
		return
	}
	view.baseView = s.newBaseView(r, "Save a video", navLibrary)
	view.Profiles = profiles
	s.render(w, "download_form", http.StatusOK, view)
}
