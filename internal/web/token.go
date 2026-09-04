package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"sub_scribe/internal/cookies"
)

// cookiesFileMode keeps the uploaded cookie file owner-only; it holds live
// YouTube session secrets and must never be world-readable.
const cookiesFileMode = 0o600

// maxCookieUpload caps an uploaded cookie file. A real cookies.txt is a few KB;
// this bounds memory while comfortably clearing any legitimate export.
const maxCookieUpload = 2 << 20 // 2 MiB

// cookieFormField is the multipart field name the upload form posts under.
const cookieFormField = "cookies"

// Badge state classes pair a color with an icon and text so the health signal is
// never color-only (WCAG: do not rely on color alone).
const (
	badgeGood  = "good"
	badgeWarn  = "warn"
	badgeBad   = "bad"
	badgeMuted = "muted"
)

// tokenView is the render model for both the nav badge and the token page. It
// exposes only assessment metadata, never cookie values.
type tokenView struct {
	Present         bool   // a cookie file exists on disk
	Connected       bool   // a valid, unexpired YouTube login is present
	StatusText      string // plain-language verdict
	Icon            string // symbol shown alongside the color
	BadgeClass      string // CSS state class
	DaysUntilExpiry int
	Error           string // friendly, specific error to surface after an upload
}

// assessToken reads CookiesPath and produces the current health view. A missing
// file is a normal "not connected" state, not an error.
func (s *Server) assessToken() tokenView {
	data, err := os.ReadFile(s.deps.CookiesPath)
	if errors.Is(err, os.ErrNotExist) {
		return tokenView{StatusText: "Not connected", Icon: "○", BadgeClass: badgeMuted}
	}
	if err != nil {
		return tokenView{Present: true, StatusText: "Cookie file unreadable", Icon: "✕", BadgeClass: badgeBad}
	}
	return s.viewForBytes(data)
}

// viewForBytes assesses raw cookie bytes as of the injected clock, mapping the
// health verdict to a plain-language badge.
func (s *Server) viewForBytes(data []byte) tokenView {
	jar, err := cookies.Parse(bytes.NewReader(data))
	if err != nil {
		return tokenView{Present: true, StatusText: "Cookie file unreadable", Icon: "✕", BadgeClass: badgeBad}
	}
	assessment := jar.Assess(s.deps.Clock.Now())
	return viewForAssessment(assessment)
}

// viewForAssessment maps a cookies.Assessment to its badge presentation.
func viewForAssessment(a cookies.Assessment) tokenView {
	view := tokenView{Present: true, DaysUntilExpiry: a.DaysUntilExpiry}
	switch a.Health {
	case cookies.HealthHealthy:
		return healthyView(view, a)
	case cookies.HealthExpiringSoon:
		view.Connected, view.Icon, view.BadgeClass = true, "!", badgeWarn
		view.StatusText = fmt.Sprintf("Connected — expires in %d days", a.DaysUntilExpiry)
	case cookies.HealthExpired:
		view.Icon, view.BadgeClass = "✕", badgeBad
		view.StatusText = "Login expired — reconnect to keep downloading"
	default: // HealthNoLogin
		view.Icon, view.BadgeClass = "✕", badgeBad
		view.StatusText = "No YouTube login found"
	}
	return view
}

// healthyView fills a connected, healthy badge, distinguishing a dated login from
// a session-only login that has no expiry to report.
func healthyView(view tokenView, a cookies.Assessment) tokenView {
	view.Connected, view.Icon, view.BadgeClass = true, "✓", badgeGood
	if a.ExpiresAt == nil {
		view.StatusText = "Connected"
		return view
	}
	view.StatusText = fmt.Sprintf("Connected — expires in %d days", a.DaysUntilExpiry)
	return view
}

// handleTokenPage renders the connect-your-account instructions plus the current
// assessment and the drag-and-drop upload zone.
func (s *Server) handleTokenPage(w http.ResponseWriter, r *http.Request) {
	s.renderToken(w, r, http.StatusOK, s.assessToken())
}

// handleTokenUpload accepts a multipart cookies.txt, validating it before it can
// overwrite a working file: a parse failure or a logged-out export re-renders the
// page with a specific error and leaves any existing good file untouched.
func (s *Server) handleTokenUpload(w http.ResponseWriter, r *http.Request) {
	data, err := readUpload(r)
	if err != nil {
		s.renderTokenError(w, r, "We couldn't read that upload — please choose your cookies.txt file and try again.")
		return
	}
	jar, err := cookies.Parse(bytes.NewReader(data))
	if err != nil {
		s.renderTokenError(w, r, "That file isn't a valid cookies.txt export — re-export it with the cookie extension and try again.")
		return
	}
	if jar.Assess(s.deps.Clock.Now()).Health == cookies.HealthNoLogin {
		s.renderTokenError(w, r, "This file has no YouTube login in it — did you export while logged out? Sign in to YouTube, then export again.")
		return
	}
	s.saveToken(w, r, data)
}

// saveToken persists validated cookie bytes and re-renders the now-connected page.
func (s *Server) saveToken(w http.ResponseWriter, r *http.Request, data []byte) {
	if err := os.WriteFile(s.deps.CookiesPath, data, cookiesFileMode); err != nil {
		s.renderTokenError(w, r, "We validated your login but couldn't save it. Check the app's storage permissions and try again.")
		return
	}
	s.renderToken(w, r, http.StatusOK, s.viewForBytes(data))
}

// readUpload extracts the raw bytes of the cookie file field, bounding the read.
func readUpload(r *http.Request) ([]byte, error) {
	if err := r.ParseMultipartForm(maxCookieUpload); err != nil {
		return nil, fmt.Errorf("web: parsing upload: %w", err)
	}
	file, _, err := r.FormFile(cookieFormField)
	if err != nil {
		return nil, fmt.Errorf("web: missing %q field: %w", cookieFormField, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxCookieUpload))
	if err != nil {
		return nil, fmt.Errorf("web: reading upload: %w", err)
	}
	return data, nil
}

// tokenPageView is the render model for the token page.
type tokenPageView struct {
	baseView
	Token tokenView
}

// renderToken renders the token page with the given assessment.
func (s *Server) renderToken(w http.ResponseWriter, r *http.Request, status int, token tokenView) {
	base := s.newBaseView(r, "Connect your YouTube account", navToken)
	base.Token = token
	s.render(w, "token", status, tokenPageView{baseView: base, Token: token})
}

// renderTokenError re-renders the token page (200) with a friendly error and the
// current, unchanged assessment, so a failed upload never dead-ends.
func (s *Server) renderTokenError(w http.ResponseWriter, r *http.Request, message string) {
	token := s.assessToken()
	token.Error = message
	s.renderToken(w, r, http.StatusOK, token)
}
