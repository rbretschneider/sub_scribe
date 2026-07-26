# sub_scribe — Handoff / Pickup Notes

_Last updated: 2026-07-22. Status: **v1 feature-complete**, 114 tests passing, `go vet` clean, binary builds, `docker compose config` validates._

This file is the single place to resume work after a break. It records **why**
this project exists, **what's built**, **how it fits together**, **how to run and
test it**, and **what's left to do**.

---

## 1. The goal

`sub_scribe` is a ground-up redesign of [pinchflat](https://github.com/kieraneglin/pinchflat)
— a self-hosted YouTube archiver that downloads channels/playlists with `yt-dlp`
and syncs the files to Plex/Jellyfin/Kodi.

The owner ran pinchflat for 1–2 years. It worked but had four pain points, which
are the north star for this rewrite:

1. **Laggy at 5+ channels** — the UI stalled when adding sources. Root cause:
   pinchflat did slow `yt-dlp` work synchronously on the add path.
2. **Painful YouTube "age token" (cookies)** — required a terminal, `nano`, and a
   hidden config file, and silently expired with no warning.
3. **Outdated UI.**
4. **Lack of configuration.**

**Design mandate:** clean code, ultra-lightweight frameworks, absolute minimum
resource usage, while staying responsive and full-featured.

### Decisions locked in (do not relitigate without reason)

- **Language/stack: Go.** Single static binary (~19 MB, pure-Go SQLite via
  `modernc.org/sqlite`, so no CGo). `net/http` + `html/template` + a tiny bit of
  vanilla JS (SSE) — **no heavy web framework, no SPA, no CDN assets** (all
  embedded).
- **Scope: full feature parity with pinchflat** (not a lean MVP).
- **Deploy: single Docker container** (with an optional second container for the
  PO-token provider — see §7).
- **Tests alongside every feature** (owner's explicit instruction). Table-driven
  Go tests, dependency injection, mock the subprocess boundary.
- **Two core fixes by architecture:** (a) **instant add** — adding a source
  writes one row + enqueues a background task and returns; a worker pool does all
  slow work. (b) **token intelligence** — drag-drop cookie upload that parses,
  validates, and reports expiry with pre-expiry warnings.

### How it was built (context for the code shape)

The foundation (domain, naming, cookies, store+queue, jobs) was written by hand,
bottom-up. Then the remaining 11 modules were built by a **multi-agent workflow**
in parallel, each against **frozen interfaces** authored first
(`ytdlp.Runner`, `events.Publisher`, `library` ports + service API). That
contract-first approach is why the parallel work integrated with zero seam
breaks. If you extend a cross-package boundary, **freeze the interface first**.

---

## 2. Current status — what works

Everything for v1 is built, tested, and was **verified on the running binary**:

- **Instant add**: `POST /sources` returns 303 in ~110 ms; the source appears
  immediately; no blocking on `yt-dlp`. (This is the #1 lag fix, proven.)
- **Async backbone**: tasks enqueue → worker pool claims → runs → retries with
  backoff on failure (verified: failed on missing `yt-dlp`, re-enqueued).
- **Token UX**: drag-drop cookie upload. Logged-out file → friendly, accessible
  error, file **not** written. Valid login → "Connected — expires in N days",
  file written (0600). Dashboard badge warns before expiry.
- **Sources**: add / list / detail / **edit** / delete, with per-source rules
  (date cutoff, title regex, Shorts/livestream inclusion, retention, cookie
  behavior).
- **Profiles**: full CRUD UI (list/new/create/edit/update/delete) — naming
  template, format, embeds, subtitles, SponsorBlock, redownload age.
- **Scheduler**: periodically enqueues index + cleanup tasks for due sources.
- **Downloading**: `yt-dlp` wrapper builds args (format/audio, embeds,
  SponsorBlock, cookies, PO-token), streams progress, captures final path.
- **Media-server output**: cross-platform-safe naming templates, `.nfo` metadata
  sidecars (Kodi/Jellyfin), RSS/podcast feeds per source.
- **Extras**: Apprise notifications (opt-in), SponsorBlock, retention auto-delete,
  live SSE UI updates, optional PO-token provider (see §7).

**Metrics:** 17 packages, ~5,700 lines of source, ~4,080 lines of tests, 114 test
functions, all green.

---

## 3. Architecture (package map)

Clean, layered, dependency-inverted. The domain and application core import **no**
infrastructure. Every side-effecting collaborator (DB, `yt-dlp`, clock,
notifications, events) is behind an interface and injected.

```
cmd/subscribe/main.go   Composition root: loads config, wires everything, runs
                        the HTTP server + worker pool + scheduler; seeds a default
                        media profile on first run.

internal/domain         Entities, enums, and pure rules. No imports of infra.
                        - Source, Media, MediaProfile, MediaMetadata
                        - Source.IsDueForIndex, MediaMetadata.PassesFilters
internal/naming         Path template engine + cross-platform sanitize.
                        - Renderer.Render / Validate; vars: source_name, title,
                          id, upload_date, upload_year, season, episode, uploader
internal/cookies        Netscape cookies.txt parse + login health/expiry
                        assessment (powers the token UX). Never exposes values.
internal/store          SQLite persistence + the durable task queue.
                        - Open() (WAL, FK, single writer), migrations in schema.go
                        - Repos: TaskRepo (queue), SourceRepo, MediaRepo,
                          ProfileRepo. Accessors: db.Tasks/Sources/Media/Profiles
internal/jobs           Async backbone: Task, Queue interface, worker Pool
                        (dispatch, retry/backoff, panic recovery), Registry,
                        Clock (SystemClock).
internal/scheduler      Enqueues index/cleanup for due sources on a ticker.
internal/ytdlp          yt-dlp subprocess boundary. Runner interface (FROZEN) +
                        ExecRunner + FakeRunner. Pure arg-builders + parsers.
internal/library        APPLICATION CORE. Service implements SourceService,
                        ProfileService, Indexer, Downloader, Retainer.
                        - ports.go / service_api.go = FROZEN interfaces
                        - service.go = the orchestration
internal/tasks          Bridges jobs.Task -> library calls. Register(registry,Deps)
internal/metadata       Kodi/Jellyfin .nfo writer (BuildEpisodeNFO + Writer).
internal/feed           RSS/podcast feed generation (BuildRSS + Writer).
internal/sponsorblock   Builds yt-dlp SponsorBlock args from profile settings.
internal/notify         Apprise notifier + NopNotifier.
internal/events         events.Publisher (FROZEN) + SSE Hub (Publish + ServeHTTP).
internal/config         Env-based config (SUBSCRIBE_* vars) with defaults.
internal/web            Server-rendered UI. embed.FS templates + static assets.
                        Depends only on library interfaces + cookies + events path.
```

### Data flow (the important paths)

- **Add source**: `web POST /sources` → `library.AddSource` (validate, write row,
  enqueue `TaskIndexSource`) → returns immediately.
- **Index**: scheduler/worker → `tasks.IndexHandler` → `library.IndexSource`
  (`ytdlp.Index` → filter via `domain.PassesFilters` → upsert pending media →
  enqueue a `TaskDownloadMedia` per new item → mark indexed → publish event).
- **Download**: worker → `tasks.DownloadHandler` → `library.DownloadMedia`
  (render path via `naming` → `ytdlp.Download` with progress events → mark
  downloaded → write `.nfo` → regenerate feed → notify → publish completed).

---

## 4. Build / run / test

Requires **Go 1.26**. `yt-dlp` + `ffmpeg` are runtime-only (they live in the
container); tests mock the subprocess boundary, so the full suite runs without
them.

```bash
# Test everything
go test ./...

# Vet
go vet ./...

# Build the binary
go build -o subscribe ./cmd/subscribe    # add .exe on Windows

# Run locally (Linux defaults use /config and /media — override on Windows/dev):
SUBSCRIBE_DATA_DIR=./.run/config SUBSCRIBE_MEDIA_DIR=./.run/media \
  SUBSCRIBE_PORT=8080 ./subscribe
# then open http://localhost:8080

# Docker (single container, cookies-only)
docker build -t sub_scribe .
docker run -d -p 8080:8080 -v ./config:/config -v ./media:/media sub_scribe

# Docker Compose (adds the opt-in PO-token provider — skips cookies for most videos)
docker compose up -d
```

### Config (env vars, defaults shown)

| Var | Default | Purpose |
|---|---|---|
| `SUBSCRIBE_DATA_DIR` | `/config` | DB, cookies, feeds |
| `SUBSCRIBE_MEDIA_DIR` | `/media` | Downloads |
| `SUBSCRIBE_DB_PATH` | `<data>/sub_scribe.db` | SQLite file |
| `SUBSCRIBE_COOKIES_PATH` | `<data>/cookies.txt` | Cookie file |
| `SUBSCRIBE_FEED_DIR` | `<data>/feeds` | RSS feeds |
| `SUBSCRIBE_PORT` | `8080` | HTTP port |
| `SUBSCRIBE_WORKERS` | `2` | Background workers |
| `SUBSCRIBE_YTDLP_PATH` | `yt-dlp` | yt-dlp binary |
| `SUBSCRIBE_APPRISE_BINARY` | `apprise` | Apprise binary |
| `SUBSCRIBE_APPRISE_URLS` | *(none)* | Comma-separated Apprise URLs |
| `SUBSCRIBE_POT_PROVIDER_URL` | *(none)* | PO-token provider base URL (§7) |

---

## 5. Key implementation notes & gotchas (read before editing)

- **Frozen interfaces**: `internal/ytdlp/runner.go`, `internal/events/event.go`,
  and `internal/library/ports.go` + `service_api.go` are the seams the rest of the
  code was built against. Changing a signature ripples; prefer adding.
- **Store timestamps**: `SourceRepo`/`ProfileRepo` `Create`/`Update` persist the
  **caller-supplied** `CreatedAt`/`UpdatedAt` (they do NOT auto-stamp). The
  `library.Service` sets them via the injected `Clock`. If you write a new caller,
  set timestamps yourself.
- **Media upsert dedup**: `MediaRepo.Upsert` dedupes on `(source_id, external_id)`
  and refreshes metadata columns only — it never clobbers `status`/`file_path`/
  `downloaded_at`. Re-indexing is safe.
- **SQLite single writer**: `store.Open` sets `MaxOpenConns(1)` + WAL +
  `busy_timeout`. Intentional: writes are tiny; avoids "database is locked". Don't
  raise it without load-testing.
- **Scheduler dedup**: `Tick` marks a source indexed at *enqueue* time
  (optimistic) so duplicate index tasks don't stack between ticks; a failed index
  is retried by the queue's backoff, not re-enqueued by the scheduler.
- **Provider is YouTube-only**: `library` builds video URLs as
  `https://www.youtube.com/watch?v=<external_id>` (see `mediaURLFor` in
  `service.go`). Generalizing this is the main open task (§6).
- **PO-token**: process-global, carried as an `ExecRunner` field (not per-request),
  so the frozen `Runner` interface and `library` were untouched. Args:
  `--extractor-args youtubepot-bgutilhttp:base_url=<url>` on both index+download.
- **Web templates**: each page composes with `layout.html`; register new pages in
  `pageNames` in `server.go`. `html/template` escapes apostrophes to `&#39;` —
  don't assert on `"couldn't"` in tests (bit me once).
- **No new Go deps** beyond `modernc.org/sqlite`. Keep it that way unless there's
  a strong reason.

---

## 6. TODO / open work (pick up here)

Ordered roughly by value:

1. **Multi-provider support** (biggest gap). Everything is YouTube-only via
   `library.mediaURLFor`. To support other `yt-dlp` sites, generalize URL
   construction (store the canonical media URL on `Media` at index time instead of
   rebuilding it from the id), and revisit Shorts/livestream detection which is
   YouTube-shaped.
2. **CI workflow**: add `.github/workflows/ci.yml` running `go test ./...` +
   `go vet` + `docker build`. Nothing exists yet.
3. **Publish image**: the README/compose reference `ghcr.io/you/sub_scribe` — wire
   up a real image build+push and update the tag.
4. **Media list / status UI**: the source detail page shows config but not the
   per-video download status/progress list. The data + SSE events exist
   (`events.KindMediaProgress/Completed/Failed`); `app.js` updates progress
   elements — but there's no per-source media table yet. Good next UI feature.
5. **Redownload enforcement**: `MediaProfile.RedownloadAfter` is stored and
   surfaced in the UI, but nothing schedules a redownload yet. Wire a scheduler/
   task path if desired.
6. **`TaskGenerateFeed`**: the task type exists but isn't registered (feeds are
   regenerated inline after each download). Register a handler only if you want
   standalone feed rebuilds.
7. **Verify real yt-dlp path end-to-end**: all runtime tests mock the subprocess.
   Worth one manual run inside the container against a real channel to confirm the
   `--print after_move:filepath` capture and progress parsing against current
   `yt-dlp` output.
8. **Docs polish**: `README.md` is user-facing and current; this file is the dev
   handoff.

### Nice-to-haves / not started
- Pause/resume a source (there's an `Enabled` flag but no UI toggle).
- Bulk actions, search/filter on the dashboard.
- Prometheus metrics endpoint (pinchflat had one; not ported).
- Auth (currently no login — fine for a trusted LAN, note before exposing).

---

## 7. PO-token provider (optional sidecar) — how it's wired

YouTube increasingly wants a "proof of origin" token even for public videos.
Rather than bloat the image with the Node-based provider (~150 MB+), the provider
runs as its **own opt-in container**; sub_scribe just points `yt-dlp` at it.

- Enable with `docker compose up -d` — `docker-compose.yml` runs
  `brainicism/bgutil-ytdlp-pot-provider` and sets
  `SUBSCRIBE_POT_PROVIDER_URL=http://pot-provider:4416` on an internal network
  (provider not exposed to the host).
- The **client half** (a small Python `yt-dlp` plugin,
  `bgutil-ytdlp-pot-provider`) is pip-installed in the main `Dockerfile`.
- Run sub_scribe alone → the var is unset → it falls back to cookies. Cookies
  remain the path for age-restricted/private content either way.

---

## 8. Memory pointers

Durable notes for this project live in the assistant memory index
(`MEMORY.md`): `project-sub-scribe` (this summary in brief) and
`feedback-tests-as-you-go` (write tests alongside every feature). Keep those in
sync if the project's direction changes.
