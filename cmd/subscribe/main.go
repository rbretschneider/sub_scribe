// Command subscribe is sub_scribe's entry point: it loads configuration, wires
// the persistence, task, and web layers together, and runs the HTTP server, the
// background worker pool, and the periodic scheduler until interrupted. This file
// is the composition root — the one place concrete implementations are chosen and
// injected; every package below it depends only on interfaces.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"sub_scribe/internal/applog"
	"sub_scribe/internal/artwork"
	"sub_scribe/internal/config"
	"sub_scribe/internal/domain"
	"sub_scribe/internal/events"
	"sub_scribe/internal/feed"
	"sub_scribe/internal/hooks"
	"sub_scribe/internal/jobs"
	"sub_scribe/internal/library"
	"sub_scribe/internal/metadata"
	"sub_scribe/internal/naming"
	"sub_scribe/internal/notify"
	"sub_scribe/internal/pacing"
	"sub_scribe/internal/scheduler"
	"sub_scribe/internal/sponsorblock"
	"sub_scribe/internal/store"
	"sub_scribe/internal/tasks"
	"sub_scribe/internal/web"
	"sub_scribe/internal/ytdlp"
)

// eventsPath is the route where the SSE hub streams live updates to the browser.
const eventsPath = "/events"

// shutdownTimeout bounds how long graceful HTTP shutdown waits for in-flight
// requests before forcing close.
const shutdownTimeout = 10 * time.Second

// defaultProfileTemplate is the naming layout seeded on first run.
//
// The season/episode token is what makes media servers read these files
// correctly. Plex parses "sYYYYeMMDDNN - Title" and takes the title from the
// filename; given a plain date instead it tries to match the channel against its
// TV database, fails, and invents titles like "Episode 04-22". The layout
// deliberately matches what pinchflat writes for media centres, because that is
// known to work.
const defaultProfileTemplate = "{{ source_name }}/Season {{ upload_year }}/{{ season_episode }} - {{ title }}"

// defaultQualityFormat is the seeded profile's yt-dlp format selector.
const defaultQualityFormat = "bestvideo[height<=1080]+bestaudio/best"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// run performs the full startup sequence and blocks until shutdown, returning any
// fatal error so main can set the exit code. Keeping this separate from main lets
// deferred cleanup (like closing the database) run before the process exits.
func run() error {
	logBuffer := applog.NewBuffer(0)
	stdout := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(applog.NewHandler(stdout, logBuffer))
	slog.SetDefault(logger)
	// Capture standard log.Printf output (used in a few services) into the buffer
	// as well, so the in-app log viewer shows everything.
	log.SetFlags(0)
	log.SetOutput(applog.NewWriter(logBuffer, os.Stdout))

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := ensureDirs(cfg); err != nil {
		return err
	}

	warnUnsafeStorage(cfg.DBPath)

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	clock := jobs.SystemClock{}
	hub := events.NewHub()
	svc := buildService(cfg, db, hub, clock)

	if err := seedDefaultProfile(context.Background(), db, svc, clock); err != nil {
		return err
	}
	// Repair anything a crash or a lost write left inconsistent before the workers
	// start claiming, so the queue the user sees is the queue that will actually run.
	if _, err := svc.Reconcile(context.Background()); err != nil {
		return fmt.Errorf("reconcile queue state: %w", err)
	}

	sessionSecret, err := loadSessionSecret(context.Background(), cfg, db)
	if err != nil {
		return err
	}

	handler, err := buildHTTPHandler(cfg, webDeps{
		svc: svc, tasks: db.Tasks(), logs: logBuffer, clock: clock, hub: hub,
		sessionSecret: sessionSecret,
	})
	if err != nil {
		return err
	}

	return serve(cfg, db, svc, clock, handler, logger)
}

// warnUnsafeStorage checks where the database lives and says so loudly if that
// filesystem cannot be trusted to keep committed writes. This is not a
// hypothetical: on a Docker Desktop bind mount the database silently loses rows
// on an abrupt stop, which looks to the user like "it indexed 500 videos and
// then downloaded nothing". Startup continues either way — it is the user's
// deployment to decide about — but it is now on the record, in the log viewer.
func warnUnsafeStorage(dbPath string) {
	warning := store.CheckDurableStorage(dbPath)
	if warning == nil {
		return
	}
	slog.Warn("database is on storage that can lose committed writes",
		"path", warning.Path,
		"filesystem", warning.Type,
		"detail", warning.Description,
		"fix", "put the data directory on a Docker named volume instead of a bind mount")
}

// ensureDirs creates the data, media, and feed directories so first-run writes
// never fail on a missing parent.
func ensureDirs(cfg config.Config) error {
	for _, dir := range []string{cfg.DataDir, cfg.MediaDir, cfg.FeedDir, cfg.TempDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}
	return nil
}

// buildService assembles the application core with its concrete collaborators.
func buildService(cfg config.Config, db *store.DB, hub *events.Hub, clock jobs.Clock) *library.Service {
	return library.NewService(library.Deps{
		Sources:      db.Sources(),
		Media:        db.Media(),
		Profiles:     db.Profiles(),
		Tasks:        db.Tasks(),
		Queue:        db.Tasks(),
		Runner:       ytdlp.NewExecRunner(cfg.YtDlpPath, cfg.POTProviderURL, throttleFor(cfg)),
		DownloadPace: downloadPacerFor(cfg),
		Naming:       naming.NewRenderer(),
		Metadata:     metadata.NewWriter(),
		Artwork:      artwork.NewWriter(nil),
		Feed:         feed.NewWriter(cfg.FeedDir),
		Notifier:     buildNotifier(cfg),
		SponsorBlock: sponsorblock.NewBuilder(),
		Hook:         hooks.NewRunner(),
		Events:       hub,
		Clock:        clock,
		MediaDir:     cfg.MediaDir,
		TempDir:      cfg.TempDir,
		CookiesPath:  cfg.CookiesPath,
		JobRetention: cfg.JobRetention,
	})
}

// throttleFor converts the resolved pacing configuration into the downloader's
// own type. The two are kept separate so the config package does not depend on
// the packages it configures.
func throttleFor(cfg config.Config) ytdlp.Throttle {
	return ytdlp.Throttle{
		RequestDelay: cfg.Throttle.RequestDelay,
		RateLimit:    cfg.Throttle.RateLimit,
		CallGap:      cfg.Throttle.CallGap,
	}
}

// downloadPacerFor builds the gate that spaces downloads apart. It is separate
// from the yt-dlp throttle because the wait is long: it is served by deferring
// the queued task rather than by holding a worker.
func downloadPacerFor(cfg config.Config) *pacing.Pacer {
	return pacing.New(pacing.Jittered(cfg.Throttle.MinDownloadInterval, cfg.Throttle.MaxDownloadInterval))
}

// refreshSidecars brings the metadata files in the archive up to date with what
// this version writes, in the background.
//
// It runs on every start rather than on request because the failure it fixes is
// invisible: an archive downloaded by an older version has sidecars missing
// fields the media server needs, and nothing about it looks wrong — the files
// play, they are named correctly, the server simply shows the wrong titles.
// Expecting the user to notice that and find a button is not a plan.
//
// In the background, because it reads every sidecar to find out whether anything
// changed, and holding the UI closed for that on a large archive would be a
// worse bug than the one being fixed.
func refreshSidecars(ctx context.Context, svc *library.Service, logger *slog.Logger) {
	changed, err := svc.RefreshSidecars(ctx)
	if err != nil {
		logger.Error("refreshing metadata sidecars failed", "error", err)
		return
	}
	if changed == 0 {
		return // the normal case: nothing to say
	}
	logger.Info("brought metadata sidecars up to date",
		"files", changed,
		"note", "media servers need a metadata refresh to pick these up")
}

// ytdlpUpdateTimeout bounds the startup self-update. The download is a few
// megabytes; anything past this is a network problem, not an update in progress.
const ytdlpUpdateTimeout = 5 * time.Minute

// updateYtDlp brings yt-dlp up to date in the background at startup, so a
// restart fixes YouTube breakage without waiting for a new image. Failure is
// logged and life goes on — the current yt-dlp keeps working exactly as well
// as it did yesterday.
func updateYtDlp(ctx context.Context, binaryPath string, logger *slog.Logger) {
	updateCtx, cancel := context.WithTimeout(ctx, ytdlpUpdateTimeout)
	defer cancel()

	version, err := ytdlp.SelfUpdate(updateCtx, binaryPath)
	if err != nil {
		logger.Warn("yt-dlp self-update failed; continuing with the installed version",
			"error", err, "disable_with", "SUBSCRIBE_YTDLP_AUTO_UPDATE=false")
		return
	}
	logger.Info("yt-dlp is up to date", "version", version)
}

// rateLimitLabel renders an unset bandwidth cap as a word rather than an empty
// string, so the startup line reads as a statement either way.
func rateLimitLabel(limit string) string {
	if limit == "" {
		return "unlimited"
	}
	return limit
}

// buildNotifier selects the Apprise notifier when notification URLs are
// configured, and a no-op notifier otherwise.
func buildNotifier(cfg config.Config) library.Notifier {
	if len(cfg.AppriseURLs) > 0 {
		return notify.NewAppriseNotifier(cfg.AppriseBinary, cfg.AppriseURLs)
	}
	return notify.NewNopNotifier()
}

// webDeps groups what the UI needs, keeping the handler builder to a short
// signature as the number of screens grows.
type webDeps struct {
	svc           *library.Service
	tasks         *store.TaskRepo
	logs          *applog.Buffer
	clock         jobs.Clock
	hub           *events.Hub
	sessionSecret []byte
}

// loadSessionSecret fetches (or first creates) the persistent cookie-signing
// secret when SSO is configured. Without SSO there are no signed cookies, so
// nothing is generated and the database stays untouched.
func loadSessionSecret(ctx context.Context, cfg config.Config, db *store.DB) ([]byte, error) {
	if !cfg.OIDC.Enabled() {
		return nil, nil
	}
	secret, err := db.Settings().SessionSecret(ctx)
	if err != nil {
		return nil, fmt.Errorf("load session secret: %w", err)
	}
	return secret, nil
}

// buildHTTPHandler mounts the SSE hub and the web UI on one mux. The more
// specific events route is matched ahead of the web catch-all by Go's router.
func buildHTTPHandler(cfg config.Config, deps webDeps) (http.Handler, error) {
	webServer, err := web.NewServer(web.ServerDeps{
		Sources:     deps.svc,
		Profiles:    deps.svc,
		Library:     deps.svc,
		Media:       deps.svc,
		Jobs:        deps.tasks,
		Queue:       deps.tasks,
		Logs:        deps.logs,
		CookiesPath: cfg.CookiesPath,
		FeedDir:     cfg.FeedDir,
		Clock:       deps.clock,
		EventsPath:  eventsPath,
		Username:    cfg.Username,
		Password:    cfg.Password,
		OIDC: web.OIDCOptions{
			IssuerURL:    cfg.OIDC.IssuerURL,
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			ButtonLabel:  cfg.OIDC.ButtonLabel,
		},
		SessionSecret: deps.sessionSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("build web server: %w", err)
	}

	mux := http.NewServeMux()
	// The hub is mounted beside the web server, so it needs the same auth gate
	// or a locked UI would still leak activity through the event stream.
	mux.Handle(eventsPath, webServer.Protect(deps.hub))
	mux.Handle("/", webServer)
	return mux, nil
}

// serve starts the worker pool, scheduler, and HTTP server, then blocks until an
// interrupt signal triggers graceful shutdown.
func serve(cfg config.Config, db *store.DB, svc *library.Service, clock jobs.Clock, handler http.Handler, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry := jobs.NewRegistry()
	tasks.Register(registry, tasks.Deps{
		Indexer:      svc,
		Downloader:   svc,
		Retainer:     svc,
		Redownloader: svc,
		JobPruner:    svc,
		Renamer:      svc,
	})
	pool := jobs.NewPool(db.Tasks(), registry, clock, jobs.PoolConfig{
		Workers:    cfg.Workers,
		Logger:     logger,
		TagContext: applog.ContextWithTask,
	})
	sched := scheduler.New(scheduler.Config{Sources: db.Sources(), Tasks: db.Tasks(), Clock: clock, Logger: logger})

	go pool.Run(ctx)
	go sched.Run(ctx)
	go refreshSidecars(ctx, svc, logger)
	if cfg.YtDlpAutoUpdate {
		go updateYtDlp(ctx, cfg.YtDlpPath, logger)
	}

	server := &http.Server{Addr: ":" + strconv.Itoa(cfg.Port), Handler: handler}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("http shutdown failed", "error", err)
		}
	}()

	logger.Info("sub_scribe listening", "addr", server.Addr, "workers", cfg.Workers, "media_dir", cfg.MediaDir)
	// Pacing is logged because it is invisible otherwise: a run that looks slow
	// should be identifiable as deliberate restraint rather than a problem.
	logger.Info("provider pacing",
		"request_delay", cfg.Throttle.RequestDelay,
		"download_every", fmt.Sprintf("%v–%v", cfg.Throttle.MinDownloadInterval, cfg.Throttle.MaxDownloadInterval),
		"call_gap", cfg.Throttle.CallGap,
		"rate_limit", rateLimitLabel(cfg.Throttle.RateLimit))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// seedDefaultProfile creates a single Plex-friendly media profile the first time
// the app starts, so the add-source screen always has a profile to choose.
func seedDefaultProfile(ctx context.Context, db *store.DB, svc *library.Service, clock jobs.Clock) error {
	existing, err := db.Profiles().List(ctx)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}

	now := clock.Now()
	profile := domain.MediaProfile{
		Name:               "Default (1080p, Plex layout)",
		OutputPathTemplate: defaultProfileTemplate,
		Kind:               domain.MediaVideo,
		QualityFormat:      defaultQualityFormat,
		MetadataFormat:     domain.MetadataEpisode,
		EmbedMetadata:      true,
		EmbedThumbnail:     true,
		WriteThumbnail:     true,
		SponsorBlockMode:   domain.SponsorBlockRemove,
		// Sponsors only, and stated rather than implied. The adjacent categories
		// are far more subjective — "self-promotion" covers a creator talking
		// about their own work — and a removal cannot be undone without
		// downloading the video again.
		SponsorBlockCategories: []domain.SponsorBlockCategory{domain.SponsorBlockSponsor},
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if _, err := svc.CreateProfile(ctx, profile); err != nil {
		return fmt.Errorf("seed default profile: %w", err)
	}
	slog.Info("seeded default media profile", "template", filepath.ToSlash(defaultProfileTemplate))
	return nil
}
