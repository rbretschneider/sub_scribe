package cookies

import (
	"strings"
	"testing"
	"time"
)

// now is a fixed reference time so expiry assessments are deterministic.
var now = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)

func TestParseSkipsCommentsAndBlankLines(t *testing.T) {
	content := "# Netscape HTTP Cookie File\n\n" +
		".youtube.com\tTRUE\t/\tTRUE\t9999999999\tPREF\tabc\n"
	jar, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(jar) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(jar))
	}
	if jar[0].Name != "PREF" {
		t.Errorf("cookie name = %q, want PREF", jar[0].Name)
	}
}

func TestParseHandlesHttpOnlyPrefix(t *testing.T) {
	content := "#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t9999999999\tSID\tsecret\n"
	jar, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(jar) != 1 || jar[0].Name != "SID" || jar[0].Domain != ".youtube.com" {
		t.Fatalf("HttpOnly cookie parsed incorrectly: %+v", jar)
	}
}

func TestParseRejectsMalformedRow(t *testing.T) {
	content := ".youtube.com\tTRUE\t/\tonly-four-fields\n"
	if _, err := Parse(strings.NewReader(content)); err == nil {
		t.Fatal("expected error for malformed row, got nil")
	}
}

func TestParseSessionCookieHasZeroExpiry(t *testing.T) {
	content := ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tsecret\n"
	jar, err := Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !jar[0].IsSession() {
		t.Error("expected session cookie for expiry 0")
	}
}

func TestAssessHealthyLogin(t *testing.T) {
	jar := Jar{
		{Domain: ".youtube.com", Name: "SID", Expires: now.Add(60 * 24 * time.Hour)},
		{Domain: ".youtube.com", Name: "LOGIN_INFO", Expires: now.Add(90 * 24 * time.Hour)},
	}
	got := jar.Assess(now)
	if got.Health != HealthHealthy {
		t.Errorf("Health = %q, want healthy", got.Health)
	}
	if !got.IsLogin {
		t.Error("IsLogin = false, want true")
	}
	if got.DaysUntilExpiry != 60 {
		t.Errorf("DaysUntilExpiry = %d, want 60", got.DaysUntilExpiry)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(now.Add(60*24*time.Hour)) {
		t.Errorf("ExpiresAt = %v, want earliest auth expiry", got.ExpiresAt)
	}
}

func TestAssessExpiringSoon(t *testing.T) {
	jar := Jar{{Domain: ".youtube.com", Name: "SID", Expires: now.Add(3 * 24 * time.Hour)}}
	got := jar.Assess(now)
	if got.Health != HealthExpiringSoon {
		t.Errorf("Health = %q, want expiring_soon", got.Health)
	}
	if got.DaysUntilExpiry != 3 {
		t.Errorf("DaysUntilExpiry = %d, want 3", got.DaysUntilExpiry)
	}
}

func TestAssessExpired(t *testing.T) {
	jar := Jar{{Domain: ".youtube.com", Name: "SID", Expires: now.Add(-24 * time.Hour)}}
	got := jar.Assess(now)
	if got.Health != HealthExpired {
		t.Errorf("Health = %q, want expired", got.Health)
	}
}

func TestAssessNoLoginWhenOnlyNonAuthCookies(t *testing.T) {
	jar := Jar{{Domain: ".youtube.com", Name: "PREF", Expires: now.Add(365 * 24 * time.Hour)}}
	got := jar.Assess(now)
	if got.Health != HealthNoLogin {
		t.Errorf("Health = %q, want no_login", got.Health)
	}
	if got.IsLogin {
		t.Error("IsLogin = true, want false")
	}
}

func TestAssessSessionOnlyLoginIsHealthy(t *testing.T) {
	// Auth cookie present but session-scoped (no persistent expiry).
	jar := Jar{{Domain: ".youtube.com", Name: "SID"}}
	got := jar.Assess(now)
	if got.Health != HealthHealthy {
		t.Errorf("Health = %q, want healthy", got.Health)
	}
	if got.ExpiresAt != nil {
		t.Errorf("ExpiresAt = %v, want nil for session-only login", got.ExpiresAt)
	}
}
