package web

import (
	"fmt"
	"net/http"

	"sub_scribe/internal/domain"
)

// profilesView is the render model for the profile list page.
type profilesView struct {
	baseView
	Profiles []domain.MediaProfile
}

// profileFormView is the render model for the profile form, serving both create
// and edit via its Heading/Action/SubmitLabel fields.
type profileFormView struct {
	baseView
	Error       string
	Values      profileFormValues
	Heading     string
	Action      string
	SubmitLabel string
}

// profileFormOptions configures a profile-form render as a single parameter
// object, keeping renderProfileForm's signature small.
type profileFormOptions struct {
	Heading     string
	Action      string
	SubmitLabel string
	Status      int
	Values      profileFormValues
	Message     string
}

// handleProfiles lists the media profiles.
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.deps.Profiles.ListProfiles(r.Context())
	if err != nil {
		http.Error(w, "could not load profiles", http.StatusInternalServerError)
		return
	}
	view := profilesView{baseView: s.newBaseView("Media profiles", navProfiles), Profiles: profiles}
	s.render(w, "profiles", http.StatusOK, view)
}

// handleProfileNew renders the new-profile form with sensible defaults.
func (s *Server) handleProfileNew(w http.ResponseWriter, r *http.Request) {
	s.renderProfileForm(w, profileFormOptions{
		Heading:     "New profile",
		Action:      "/profiles",
		SubmitLabel: "Create profile",
		Status:      http.StatusOK,
		Values:      defaultProfileFormValues(),
	})
}

// handleProfileCreate validates and creates a profile, surfacing the service's
// template-validation error to the user so a bad template is actionable.
func (s *Server) handleProfileCreate(w http.ResponseWriter, r *http.Request) {
	values := readProfileFormValues(r)
	opts := profileFormOptions{Heading: "New profile", Action: "/profiles", SubmitLabel: "Create profile", Status: http.StatusOK, Values: values}
	profile, err := values.toProfile()
	if err != nil {
		opts.Message = err.Error()
		s.renderProfileForm(w, opts)
		return
	}
	if _, err := s.deps.Profiles.CreateProfile(r.Context(), profile); err != nil {
		opts.Message = friendlyProfileError(err)
		s.renderProfileForm(w, opts)
		return
	}
	redirect(w, r, "/profiles")
}

// handleProfileEdit renders the form pre-filled with an existing profile.
func (s *Server) handleProfileEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	profile, err := s.deps.Profiles.GetProfile(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.renderProfileForm(w, profileFormOptions{
		Heading:     "Edit profile",
		Action:      fmt.Sprintf("/profiles/%d", id),
		SubmitLabel: "Save changes",
		Status:      http.StatusOK,
		Values:      fromProfile(profile),
	})
}

// handleProfileUpdate validates and saves changes to a profile, preserving its
// identity and creation time.
func (s *Server) handleProfileUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	existing, err := s.deps.Profiles.GetProfile(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	values := readProfileFormValues(r)
	opts := profileFormOptions{Heading: "Edit profile", Action: fmt.Sprintf("/profiles/%d", id), SubmitLabel: "Save changes", Status: http.StatusOK, Values: values}
	profile, err := values.toProfile()
	if err != nil {
		opts.Message = err.Error()
		s.renderProfileForm(w, opts)
		return
	}
	profile.ID = existing.ID
	profile.CreatedAt = existing.CreatedAt
	if err := s.deps.Profiles.UpdateProfile(r.Context(), profile); err != nil {
		opts.Message = friendlyProfileError(err)
		s.renderProfileForm(w, opts)
		return
	}
	redirect(w, r, "/profiles")
}

// handleProfileDelete removes a profile and returns to the profile list.
func (s *Server) handleProfileDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.deps.Profiles.DeleteProfile(r.Context(), id); err != nil {
		http.Error(w, "could not delete profile", http.StatusInternalServerError)
		return
	}
	redirect(w, r, "/profiles")
}

// renderProfileForm renders the profile form for the configured mode.
func (s *Server) renderProfileForm(w http.ResponseWriter, opts profileFormOptions) {
	view := profileFormView{
		baseView:    s.newBaseView(opts.Heading, navProfiles),
		Error:       opts.Message,
		Values:      opts.Values,
		Heading:     opts.Heading,
		Action:      opts.Action,
		SubmitLabel: opts.SubmitLabel,
	}
	s.render(w, "profile_form", opts.Status, view)
}

// friendlyProfileError turns a service error into a user-facing message. Template
// problems are actionable, so the underlying detail is included.
func friendlyProfileError(err error) string {
	return fmt.Sprintf("We couldn't save this profile: %s", err.Error())
}
