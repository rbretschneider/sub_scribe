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
	// envUsername/envPassword switch on HTTP basic auth for the whole UI when
	// both are set. Unset (the default) leaves the app open, which is fine on a
	// trusted LAN and wrong anywhere else.
	envUsername = "SUBSCRIBE_USERNAME"
	envPassword = "SUBSCRIBE_PASSWORD"
	// envOIDC* switch on single sign-on through an OpenID Connect provider when
	// all three are set. The issuer URL is the provider's base URL — endpoints
	// are found via its /.well-known/openid-configuration document, never
	// configured individually. Unset (the default) leaves SSO entirely dormant.
	envOIDCIssuerURL    = "SUBSCRIBE_OIDC_ISSUER_URL"
	envOIDCClientID     = "SUBSCRIBE_OIDC_CLIENT_ID"
	envOIDCClientSecret = "SUBSCRIBE_OIDC_CLIENT_SECRET"
	// envOIDCButtonLabel customises the sign-in button's text on the login page.
	envOIDCButtonLabel = "SUBSCRIBE_OIDC_BUTTON_LABEL"
	// envYtDlpUpdate turns off the yt-dlp self-update run at startup.
	envYtDlpUpdate = "SUBSCRIBE_YTDLP_AUTO_UPDATE"

	// Throttle settings. All are expressed in seconds except the rate limit,
	// which takes yt-dlp's own notation. Setting any of them to 0 turns that
	// particular measure off.
	envRequestDelay = "SUBSCRIBE_REQUEST_DELAY_SECONDS"
	// These two keep their original names although they now govern the interval
	// between downloads rather than a pause before each one. The names still
	// describe what they do, and renaming them would silently turn into a no-op
	// in an existing compose file — the worst possible outcome for a setting
	// whose job is to keep an account safe.
	envMinDelay  = "SUBSCRIBE_DOWNLOAD_DELAY_MIN_SECONDS"
	envMaxDelay  = "SUBSCRIBE_DOWNLOAD_DELAY_MAX_SECONDS"
	envCallGap   = "SUBSCRIBE_CALL_GAP_SECONDS"
	envRateLimit = "SUBSCRIBE_RATE_LIMIT"
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

	// defaultOIDCButtonLabel is the sign-in button text when none is configured.
	defaultOIDCButtonLabel = "Sign in with SSO"

	// Throttle defaults, chosen for the case that actually matters: archiving
	// while signed in with your own account's cookies. A signed-in client that
	// pulls video after video with no pause is what gets accounts flagged, and
	// the cost of that is the account, not the download. These are deliberately
	// on out of the box; set any of them to 0 to opt out.
	//
	// Nothing here is urgent. An archiver that quietly collects a channel over a
	// day is doing its job; one that empties a channel in ten minutes is doing
	// its job in the most conspicuous way available.
	//
	// Two seconds between HTTP requests sounds heavy, but requests are mostly
	// metadata lookups that take milliseconds — this is the pacing that covers
	// the bulk of the traffic.
	defaultRequestDelaySeconds = 2
	// Roughly one download every ten minutes, randomised across this range so
	// the interval is not itself a recognisable pattern. This is enforced by the
	// task queue, not by a sleeping worker, so nothing else is held up by it.
	defaultMinDownloadIntervalSeconds = 8 * 60
	defaultMaxDownloadIntervalSeconds = 12 * 60
	// The floor on spacing between yt-dlp launches, which is what stops two
	// workers from starting at the same instant. Short enough to simply wait out.
	defaultCallGapSeconds = 5
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

	// Username and Password, when both set, put the whole UI behind HTTP basic
	// auth. Both empty (the default) leaves it open for a trusted LAN.
	Username string
	Password string

	// OIDC, when enabled, adds single sign-on through an OpenID Connect
	// provider. It may be combined with basic auth: SSO serves the browser while
	// basic auth remains available for scripts and feed readers.
	OIDC OIDC

	// YtDlpAutoUpdate runs yt-dlp's self-update once at startup, so YouTube
	// breakage is fixed by a restart instead of waiting for an image rebuild.
	YtDlpAutoUpdate bool

	// Throttle paces every call to the provider. See the defaults above for why
	// it is on by default.
	Throttle Throttle
}

// OIDC is the resolved single-sign-on configuration. Either all three
// credentials are set (SSO on) or none are (SSO dormant); Load rejects
// anything in between. The model is trust-the-IdP: any identity the provider
// authenticates and authorizes for this app gets in — access control is the
// provider's application/group binding, not a user table here.
type OIDC struct {
	// IssuerURL is the provider's base URL; its discovery document supplies
	// every endpoint.
	IssuerURL    string
	ClientID     string
	ClientSecret string
	// ButtonLabel is the login page's sign-in button text.
	ButtonLabel string
}

// Enabled reports whether SSO is configured. Load guarantees the three
// credentials are all present or all absent, so the issuer alone decides.
func (o OIDC) Enabled() bool {
	return o.IssuerURL != ""
}

// Throttle is the resolved pacing configuration. It mirrors ytdlp.Throttle
// rather than importing it, so config stays free of dependencies on the
// packages it configures; main does the one-line conversion.
type Throttle struct {
	// RequestDelay is the pause between HTTP requests inside one yt-dlp run.
	RequestDelay time.Duration
	// MinDownloadInterval and MaxDownloadInterval bound the randomised gap
	// between the start of one download and the next. Waiting for this happens
	// in the task queue rather than in a worker, so a long interval costs
	// nothing but time.
	MinDownloadInterval time.Duration
	MaxDownloadInterval time.Duration
	// CallGap is the minimum spacing between yt-dlp launches across all workers.
	CallGap time.Duration
	// RateLimit caps download bandwidth in yt-dlp notation ("4.2M"); empty is
	// unlimited.
	RateLimit string
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

	throttle, err := loadThrottle(getenv)
	if err != nil {
		return Config{}, err
	}
	cfg.Throttle = throttle

	cfg.Username = strings.TrimSpace(getenv(envUsername))
	cfg.Password = getenv(envPassword)
	if (cfg.Username == "") != (cfg.Password == "") {
		return Config{}, fmt.Errorf("config: %s and %s must be set together or not at all",
			envUsername, envPassword)
	}

	oidc, err := loadOIDC(getenv)
	if err != nil {
		return Config{}, err
	}
	cfg.OIDC = oidc

	update, err := boolOr(getenv(envYtDlpUpdate), true, envYtDlpUpdate)
	if err != nil {
		return Config{}, err
	}
	cfg.YtDlpAutoUpdate = update

	return cfg, nil
}

// loadOIDC resolves the SSO settings, extending the basic-auth precedent to a
// triple: issuer, client id, and client secret are set together or not at all.
// A partial configuration would either silently leave SSO off or break at the
// first login, so it fails loudly at startup instead.
func loadOIDC(getenv func(key string) string) (OIDC, error) {
	oidc := OIDC{
		IssuerURL:    strings.TrimSpace(getenv(envOIDCIssuerURL)),
		ClientID:     strings.TrimSpace(getenv(envOIDCClientID)),
		ClientSecret: getenv(envOIDCClientSecret),
	}
	set := 0
	for _, value := range []string{oidc.IssuerURL, oidc.ClientID, oidc.ClientSecret} {
		if value != "" {
			set++
		}
	}
	if set != 0 && set != 3 {
		return OIDC{}, fmt.Errorf("config: %s, %s, and %s must be set together or not at all",
			envOIDCIssuerURL, envOIDCClientID, envOIDCClientSecret)
	}
	// The label only means something once SSO is on; leaving it empty otherwise
	// keeps a dormant OIDC block indistinguishable from the zero value.
	if oidc.Enabled() {
		oidc.ButtonLabel = valueOr(getenv, envOIDCButtonLabel, defaultOIDCButtonLabel)
	}
	return oidc, nil
}

// boolOr parses an on/off environment value, using fallback when it is unset.
func boolOr(raw string, fallback bool, key string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return fallback, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("config: %s must be a boolean (true/false), got %q", key, raw)
	}
}

// loadThrottle resolves the pacing settings, each of which accepts a fractional
// number of seconds and treats zero as "turn this one off".
func loadThrottle(getenv func(key string) string) (Throttle, error) {
	var throttle Throttle
	for _, setting := range []durationSetting{
		{envRequestDelay, defaultRequestDelaySeconds, &throttle.RequestDelay},
		{envMinDelay, defaultMinDownloadIntervalSeconds, &throttle.MinDownloadInterval},
		{envMaxDelay, defaultMaxDownloadIntervalSeconds, &throttle.MaxDownloadInterval},
		{envCallGap, defaultCallGapSeconds, &throttle.CallGap},
	} {
		value, err := nonNegativeSeconds(getenv(setting.key), setting.fallback, setting.key)
		if err != nil {
			return Throttle{}, err
		}
		*setting.target = value
	}
	throttle.RateLimit = strings.TrimSpace(getenv(envRateLimit))
	return throttle, nil
}

// durationSetting binds an environment variable to the Throttle field it fills,
// so the four pacing values are parsed by one loop instead of four near-copies.
type durationSetting struct {
	key      string
	fallback float64
	target   *time.Duration
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

// nonNegativeSeconds parses raw as a possibly-fractional number of seconds,
// using fallback when empty. Zero is allowed and means "disabled"; negatives are
// rejected because a negative pause has no sensible reading. key names the
// source variable for errors.
func nonNegativeSeconds(raw string, fallback float64, key string) (time.Duration, error) {
	seconds := fallback
	if raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, fmt.Errorf("parse %s %q: %w", key, raw, err)
		}
		seconds = parsed
	}
	if seconds < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %v", key, seconds)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// joinPath joins a directory and a name with a single forward slash. sub_scribe
// runs in a Linux container, so paths are POSIX-style.
func joinPath(dir, name string) string {
	return strings.TrimRight(dir, pathSeparator) + pathSeparator + name
}
