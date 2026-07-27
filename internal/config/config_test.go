package config

import (
	"reflect"
	"testing"
	"time"
)

// fakeEnv builds a getenv function backed by a map, so tests inject an exact
// environment without touching the real process environment.
func fakeEnv(vars map[string]string) func(string) string {
	return func(key string) string {
		return vars[key]
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(fakeEnv(nil))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := Config{
		DataDir:       "/config",
		MediaDir:      "/media",
		TempDir:       "/var/tmp/sub_scribe",
		DBPath:        "/config/sub_scribe.db",
		CookiesPath:   "/config/cookies.txt",
		FeedDir:       "/config/feeds",
		YtDlpPath:     "yt-dlp",
		AppriseBinary: "apprise",
		AppriseURLs:   nil,
		Port:          8080,
		Workers:       2,
		JobRetention:  14 * 24 * time.Hour,
		// Pacing is on by default; see the note on the defaults in config.go.
		Throttle: Throttle{
			RequestDelay:        2 * time.Second,
			MinDownloadInterval: 8 * time.Minute,
			MaxDownloadInterval: 12 * time.Minute,
			CallGap:             5 * time.Second,
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("defaults mismatch:\n got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadPOTProviderURL(t *testing.T) {
	t.Run("defaults empty", func(t *testing.T) {
		cfg, err := Load(fakeEnv(nil))
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		if cfg.POTProviderURL != "" {
			t.Errorf("POTProviderURL = %q, want empty by default", cfg.POTProviderURL)
		}
	})
	t.Run("reads and trims env", func(t *testing.T) {
		cfg, err := Load(fakeEnv(map[string]string{envPOTProvider: "  http://pot:4416  "}))
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		if cfg.POTProviderURL != "http://pot:4416" {
			t.Errorf("POTProviderURL = %q, want trimmed http://pot:4416", cfg.POTProviderURL)
		}
	})
}

func TestLoadDataDirMovesDerivedPaths(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{envDataDir: "/data/app"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"DBPath", cfg.DBPath, "/data/app/sub_scribe.db"},
		{"CookiesPath", cfg.CookiesPath, "/data/app/cookies.txt"},
		{"FeedDir", cfg.FeedDir, "/data/app/feeds"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoadTrailingSlashDataDir(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{envDataDir: "/data/"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DBPath != "/data/sub_scribe.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/data/sub_scribe.db")
	}
}

func TestLoadExplicitDerivedPathsOverrideDefaults(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{
		envDataDir:     "/data",
		envDBPath:      "/custom/db.sqlite",
		envCookiesPath: "/custom/c.txt",
		envFeedDir:     "/custom/feeds",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DBPath != "/custom/db.sqlite" {
		t.Errorf("DBPath = %q, want override", cfg.DBPath)
	}
	if cfg.CookiesPath != "/custom/c.txt" {
		t.Errorf("CookiesPath = %q, want override", cfg.CookiesPath)
	}
	if cfg.FeedDir != "/custom/feeds" {
		t.Errorf("FeedDir = %q, want override", cfg.FeedDir)
	}
}

func TestLoadPortAndWorkersOverride(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{
		envPort:    "9090",
		envWorkers: "8",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d, want 9090", cfg.Port)
	}
	if cfg.Workers != 8 {
		t.Errorf("Workers = %d, want 8", cfg.Workers)
	}
}

func TestLoadPathAndBinaryOverrides(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{
		envMediaDir:      "/mnt/media",
		envTempDir:       "/scratch",
		envYtDlpPath:     "/usr/bin/yt-dlp",
		envAppriseBinary: "/usr/bin/apprise",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MediaDir != "/mnt/media" {
		t.Errorf("MediaDir = %q, want /mnt/media", cfg.MediaDir)
	}
	if cfg.TempDir != "/scratch" {
		t.Errorf("TempDir = %q, want /scratch", cfg.TempDir)
	}
	if cfg.YtDlpPath != "/usr/bin/yt-dlp" {
		t.Errorf("YtDlpPath = %q, want /usr/bin/yt-dlp", cfg.YtDlpPath)
	}
	if cfg.AppriseBinary != "/usr/bin/apprise" {
		t.Errorf("AppriseBinary = %q, want /usr/bin/apprise", cfg.AppriseBinary)
	}
}

func TestLoadJobRetention(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"defaults to two weeks", "", 14 * 24 * time.Hour},
		{"explicit days", "3", 3 * 24 * time.Hour},
		{"zero keeps history forever", "0", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := Load(fakeEnv(map[string]string{envJobRetention: test.raw}))
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if cfg.JobRetention != test.want {
				t.Errorf("JobRetention = %v, want %v", cfg.JobRetention, test.want)
			}
		})
	}
}

func TestLoadRejectsNegativeJobRetention(t *testing.T) {
	if _, err := Load(fakeEnv(map[string]string{envJobRetention: "-1"})); err == nil {
		t.Fatal("Load() error = nil, want an error for a negative retention")
	}
}

func TestLoadInvalidPortAndWorkers(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
	}{
		{"non-numeric port", map[string]string{envPort: "abc"}},
		{"zero port", map[string]string{envPort: "0"}},
		{"negative port", map[string]string{envPort: "-1"}},
		{"non-numeric workers", map[string]string{envWorkers: "two"}},
		{"zero workers", map[string]string{envWorkers: "0"}},
		{"negative workers", map[string]string{envWorkers: "-3"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(fakeEnv(tc.vars)); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestLoadAppriseURLsSplitting(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty is nil", "", nil},
		{"single url", "mailto://a", []string{"mailto://a"}},
		{"comma split", "mailto://a,discord://b", []string{"mailto://a", "discord://b"}},
		{"trims spaces", " mailto://a , discord://b ", []string{"mailto://a", "discord://b"}},
		{"drops empties", "mailto://a,,discord://b,", []string{"mailto://a", "discord://b"}},
		{"only separators is nil", ",, ,", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(fakeEnv(map[string]string{envAppriseURLs: tc.raw}))
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if !reflect.DeepEqual(cfg.AppriseURLs, tc.want) {
				t.Errorf("AppriseURLs = %#v, want %#v", cfg.AppriseURLs, tc.want)
			}
		})
	}
}

func TestLoadThrottleOverrides(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{
		"SUBSCRIBE_REQUEST_DELAY_SECONDS":      "0.5",
		"SUBSCRIBE_DOWNLOAD_DELAY_MIN_SECONDS": "10",
		"SUBSCRIBE_DOWNLOAD_DELAY_MAX_SECONDS": "45",
		"SUBSCRIBE_CALL_GAP_SECONDS":           "7",
		"SUBSCRIBE_RATE_LIMIT":                 " 2.5M ",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := Throttle{
		RequestDelay:        500 * time.Millisecond,
		MinDownloadInterval: 10 * time.Second,
		MaxDownloadInterval: 45 * time.Second,
		CallGap:             7 * time.Second,
		RateLimit:           "2.5M",
	}
	if cfg.Throttle != want {
		t.Errorf("Throttle = %+v, want %+v", cfg.Throttle, want)
	}
}

// TestLoadThrottleCanBeTurnedOff checks the opt-out: zero has to mean disabled
// rather than falling back to the default, or there would be no way to run
// unthrottled.
func TestLoadThrottleCanBeTurnedOff(t *testing.T) {
	cfg, err := Load(fakeEnv(map[string]string{
		"SUBSCRIBE_REQUEST_DELAY_SECONDS":      "0",
		"SUBSCRIBE_DOWNLOAD_DELAY_MIN_SECONDS": "0",
		"SUBSCRIBE_DOWNLOAD_DELAY_MAX_SECONDS": "0",
		"SUBSCRIBE_CALL_GAP_SECONDS":           "0",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if (cfg.Throttle != Throttle{}) {
		t.Errorf("Throttle = %+v, want the zero value (disabled)", cfg.Throttle)
	}
}

func TestLoadThrottleRejectsBadValues(t *testing.T) {
	tests := map[string]map[string]string{
		"negative delay":  {"SUBSCRIBE_REQUEST_DELAY_SECONDS": "-1"},
		"not a number":    {"SUBSCRIBE_CALL_GAP_SECONDS": "soon"},
		"negative gap":    {"SUBSCRIBE_CALL_GAP_SECONDS": "-0.5"},
		"negative window": {"SUBSCRIBE_DOWNLOAD_DELAY_MAX_SECONDS": "-30"},
	}
	for name, env := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(fakeEnv(env)); err == nil {
				t.Errorf("Load(%v) returned nil error, want a rejection", env)
			}
		})
	}
}
