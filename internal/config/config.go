// Package config loads sub_scribe's application configuration from the process
// environment, applying documented defaults and validating the result. It reads
// SUBSCRIBE_-prefixed variables through an injected getenv function so callers
// (and tests) control the environment source without touching os.Getenv.
package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Environment variable keys. All application settings are namespaced under the
// SUBSCRIBE_ prefix to avoid collisions with other processes.
const (
	envDataDir       = "SUBSCRIBE_DATA_DIR"
	envMediaDir      = "SUBSCRIBE_MEDIA_DIR"
	envTempDir       = "SUBSCRIBE_TEMP_DIR"
	envDBPath        = "SUBSCRIBE_DB_PATH"
	envCookiesPath   = "SUBSCRIBE_COOKIES_PATH"
	envFeedDir       = "SUBSCRIBE_FEED_DIR"
	envYtDlpPath     = "SUBSCRIBE_YTDLP_PATH"
	envAppriseBinary = "SUBSCRIBE_APPRISE_BINARY"
	envAppriseURLs   = "SUBSCRIBE_APPRISE_URLS"
	envPOTProvider   = "SUBSCRIBE_POT_PROVIDER_URL"
	envPort          = "SUBSCRIBE_PORT"
	envWorkers       = "SUBSCRIBE_WORKERS"
	envJobRetention  = "SUBSCRIBE_JOB_RETENTION_DAYS"
)

// Default configuration values used when the corresponding environment variable
// is unset or empty.
const (
	defaultDataDir  = "/config"
	defaultMediaDir = "/media"
	// defaultTempDir lives on the container's own filesystem rather than under a
	// mounted volume: partial downloads are rewritten and renamed constantly, and
	// network/virtual mounts (a Windows bind mount, an NFS share) fail those
	// renames. Only the finished file is written to MediaDir.
	defaultTempDir       = "/var/tmp/sub_scribe"
	defaultYtDlpPath     = "yt-dlp"
	defaultAppriseBinary = "apprise"
	defaultPort          = 8080
	defaultWorkers       = 2
	// defaultJobRetentionDays is how long finished queue entries are kept before
	// being pruned. Long enough to investigate yesterday's failures, short enough
	// that indexing a large channel does not leave thousands of rows forever.
	defaultJobRetentionDays = 14
)

// hoursPerDay converts the configured retention in days into a duration.
const hoursPerDay = 24 * time.Hour

// Filenames and subdirectories derived from the resolved DataDir.
const (
	dbFileName    = "sub_scribe.db"
	cookiesName   = "cookies.txt"
	feedDirName   = "feeds"
	appriseURLSep = ","
	pathSeparator = "/"
)

// Config holds the fully resolved application configuration. Path fields are
// absolute (or defaulted) locations; AppriseURLs is nil when none are set.
type Config struct {
	DataDir  string
	MediaDir string
	// TempDir is the scratch space yt-dlp writes partial downloads to before the
	// finished file is moved into MediaDir. It deliberately defaults off the
	// mounted volumes; see defaultTempDir.
	TempDir       string
	DBPath        string
	CookiesPath   string
	FeedDir       string
	YtDlpPath     string
	AppriseBinary string
	AppriseURLs   []string
	// POTProviderURL, when set, is the base URL of a bgutil PO-token provider that
	// lets yt-dlp fetch YouTube content without cookies. Empty disables it.
	POTProviderURL string
	Port           int
	Workers        int
	// JobRetention is how long a finished queue entry is kept before it is pruned.
	// Zero keeps finished jobs forever.
	JobRetention time.Duration
}

// Load builds a Config from SUBSCRIBE_-prefixed environment variables read via
// getenv. Unset or empty variables fall back to documented defaults, and the
// DB, cookies, and feed paths are derived from the resolved DataDir. It returns
// a wrapped error if Port or Workers is not a positive integer.
func Load(getenv func(key string) string) (Config, error) {
	dataDir := valueOr(getenv, envDataDir, defaultDataDir)
	cfg := Config{
		DataDir:        dataDir,
		MediaDir:       valueOr(getenv, envMediaDir, defaultMediaDir),
		TempDir:        valueOr(getenv, envTempDir, defaultTempDir),
		DBPath:         valueOr(getenv, envDBPath, joinPath(dataDir, dbFileName)),
		CookiesPath:    valueOr(getenv, envCookiesPath, joinPath(dataDir, cookiesName)),
		FeedDir:        valueOr(getenv, envFeedDir, joinPath(dataDir, feedDirName)),
		YtDlpPath:      valueOr(getenv, envYtDlpPath, defaultYtDlpPath),
		AppriseBinary:  valueOr(getenv, envAppriseBinary, defaultAppriseBinary),
		AppriseURLs:    splitURLs(getenv(envAppriseURLs)),
		POTProviderURL: strings.TrimSpace(getenv(envPOTProvider)),
	}

	port, err := positiveInt(getenv(envPort), defaultPort, envPort)
	if err != nil {
		return Config{}, err
	}
	cfg.Port = port

	workers, err := positiveInt(getenv(envWorkers), defaultWorkers, envWorkers)
	if err != nil {
		return Config{}, err
	}
	cfg.Workers = workers

	retentionDays, err := nonNegativeInt(getenv(envJobRetention), defaultJobRetentionDays, envJobRetention)
	if err != nil {
		return Config{}, err
	}
	cfg.JobRetention = time.Duration(retentionDays) * hoursPerDay

	return cfg, nil
}

// valueOr returns the environment value for key, or fallback when it is empty.
func valueOr(getenv func(key string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

// splitURLs comma-splits a raw Apprise URL list, trimming blanks. It returns nil
// when no non-empty URLs remain so the zero value stays nil.
func splitURLs(raw string) []string {
	if raw == "" {
		return nil
	}
	var urls []string
	for _, part := range strings.Split(raw, appriseURLSep) {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			urls = append(urls, trimmed)
		}
	}
	return urls
}

// positiveInt parses raw as an integer, using fallback when empty, and requires
// the result to be greater than zero. key names the source variable for errors.
func positiveInt(raw string, fallback int, key string) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", key, raw, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %d", key, value)
	}
	return value, nil
}

// nonNegativeInt parses raw as an integer, using fallback when empty, and rejects
// negatives. Unlike positiveInt it allows zero, which callers use to mean
// "disabled". key names the source variable for errors.
func nonNegativeInt(raw string, fallback int, key string) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", key, raw, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %d", key, value)
	}
	return value, nil
}

// joinPath joins a directory and a name with a single forward slash. sub_scribe
// runs in a Linux container, so paths are POSIX-style.
func joinPath(dir, name string) string {
	return strings.TrimRight(dir, pathSeparator) + pathSeparator + name
}
