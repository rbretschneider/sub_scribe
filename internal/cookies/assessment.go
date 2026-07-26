package cookies

import (
	"strings"
	"time"
)

// Health is a plain-language verdict on a cookie file, driving the dashboard
// badge so a user is never guessing whether downloads will keep working.
type Health string

const (
	// HealthNoLogin means the file parsed but lacks YouTube authentication
	// cookies (e.g. a logged-out export).
	HealthNoLogin Health = "no_login"
	// HealthExpired means the login cookies have passed their expiry.
	HealthExpired Health = "expired"
	// HealthExpiringSoon means the login is valid but within the warning window.
	HealthExpiringSoon Health = "expiring_soon"
	// HealthHealthy means the login is valid and not expiring soon.
	HealthHealthy Health = "healthy"
)

// expiringSoonWindow is how far ahead of expiry the app starts warning, giving a
// user comfortable time to refresh cookies before downloads break.
const expiringSoonWindow = 7 * 24 * time.Hour

// youTubeAuthDomain is the cookie domain that carries a YouTube login session.
const youTubeAuthDomain = ".youtube.com"

// authCookieNames are cookies that, when present for the YouTube domain, indicate
// an authenticated session. Any one of them is sufficient to consider the file a
// login; yt-dlp needs the full set, but for a health signal presence is enough.
var authCookieNames = map[string]struct{}{
	"SID":            {},
	"HSID":           {},
	"SSID":           {},
	"SAPISID":        {},
	"APISID":         {},
	"__Secure-1PSID": {},
	"__Secure-3PSID": {},
	"LOGIN_INFO":     {},
}

// Assessment is the metadata-only verdict returned for a cookie file. It
// deliberately omits cookie values so it is safe to render, log, and store.
type Assessment struct {
	Health       Health
	IsLogin      bool
	CookieCount  int
	ExpiresAt    *time.Time // earliest expiry among login cookies, if any
	DaysUntilExpiry int      // whole days from now until ExpiresAt; 0 if expired/none
}

// Assess evaluates the jar as of now and produces a UI-ready verdict. The clock
// is passed in rather than read from the environment so the behavior is
// deterministic and testable.
func (j Jar) Assess(now time.Time) Assessment {
	assessment := Assessment{CookieCount: len(j)}

	earliest, hasExpiry, found := j.earliestAuthExpiry()
	if !found {
		assessment.Health = HealthNoLogin
		return assessment
	}

	assessment.IsLogin = true
	if !hasExpiry {
		// Login present but all auth cookies are session cookies: valid now, with
		// no persistent expiry to report or warn on.
		assessment.Health = HealthHealthy
		return assessment
	}

	assessment.ExpiresAt = &earliest
	switch {
	case !earliest.After(now):
		assessment.Health = HealthExpired
	case earliest.Sub(now) <= expiringSoonWindow:
		assessment.Health = HealthExpiringSoon
		assessment.DaysUntilExpiry = daysBetween(now, earliest)
	default:
		assessment.Health = HealthHealthy
		assessment.DaysUntilExpiry = daysBetween(now, earliest)
	}
	return assessment
}

// earliestAuthExpiry scans the jar's YouTube authentication cookies and reports:
// the soonest persistent expiry, whether any persistent expiry exists, and
// whether any authentication cookie was found at all. Separating "found" from
// "hasExpiry" lets the caller treat a session-only login as valid rather than
// expired.
func (j Jar) earliestAuthExpiry() (earliest time.Time, hasExpiry bool, foundAuth bool) {
	for _, cookie := range j {
		if !isYouTubeAuthCookie(cookie) {
			continue
		}
		foundAuth = true
		if cookie.IsSession() {
			continue
		}
		if !hasExpiry || cookie.Expires.Before(earliest) {
			earliest = cookie.Expires
			hasExpiry = true
		}
	}
	return earliest, hasExpiry, foundAuth
}

// isYouTubeAuthCookie reports whether a cookie is a recognized YouTube login
// cookie, matching the domain permissively (youtube.com or a subdomain).
func isYouTubeAuthCookie(c Cookie) bool {
	domain := strings.ToLower(c.Domain)
	if !strings.HasSuffix(domain, youTubeAuthDomain) && domain != "youtube.com" {
		return false
	}
	_, isAuth := authCookieNames[c.Name]
	return isAuth
}

// daysBetween returns the whole number of days from start to end, floored at 0.
func daysBetween(start, end time.Time) int {
	if !end.After(start) {
		return 0
	}
	return int(end.Sub(start).Hours() / 24)
}
