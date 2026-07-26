# sub_scribe

A lightweight, self-hosted YouTube archiver that syncs to Plex, Jellyfin, and
Kodi. It tracks channels and playlists, downloads new uploads automatically with
`yt-dlp`, names files exactly how your media server wants them, and generates
podcast RSS feeds — all from a single ~20 MB static binary that idles around
30 MB of RAM.

It is a ground-up redesign of the ideas in
[pinchflat](https://github.com/kieraneglin/pinchflat), rebuilt in Go around two
principles:

1. **Adding a source is instant.** The UI never waits on `yt-dlp`. Adding a
   channel writes one row and returns; every slow scan and download runs in a
   background worker pool. Performance stays flat whether you track 1 channel or
   50.
2. **Connecting your YouTube account is trivial.** Drag one file onto the page;
   the app validates it, tells you who you're signed in as and when it expires,
   and warns you *before* it lapses. No terminal, no config files, ever.

## Quick start (Docker)

```bash
docker run -d \
  --name sub_scribe \
  -p 8080:8080 \
  -v sub_scribe_config:/config \
  -v /path/to/media:/media \
  ghcr.io/you/sub_scribe:latest   # or build locally: docker build -t sub_scribe .
```

Or `docker compose up -d`, which additionally runs the optional PO-token
provider described below.

Open <http://localhost:8080>. A default "1080p, Plex layout" profile is created
on first run, so you can add a channel right away.

Point Plex/Jellyfin at the same `/media` folder and it will pick up the files —
they're named in a media-server-friendly layout by default.

## Storage

**`/config` must be a Docker named volume, not a host bind mount.** Docker
Desktop shares host folders over 9p/virtiofs, and those filesystems do not
honour `fsync` ordering. SQLite's durability guarantees rest entirely on that
contract. When it is broken, an abrupt stop can leave a database that passes
`PRAGMA integrity_check` while committed rows have silently vanished — in
practice, a few hundred queued videos disappear and the queue fills with tasks
pointing at media that no longer exists. sub_scribe logs a prominent warning at
startup if it detects the database on such a filesystem.

`/media` is a bind mount on purpose — those are the files you want on your own
disk. In-progress downloads are written to `SUBSCRIBE_TEMP_DIR` on the
container's own filesystem and only the finished file is moved to `/media`, so
the partial-file renames that these mounts handle badly never touch it.

To back up the config volume:

```bash
docker run --rm -v sub_scribe_config:/c -v "$PWD:/out" alpine \
  tar czf /out/sub_scribe-config.tgz -C /c .
```

## Connecting your YouTube account (the cookies / "token")

You only need this for **age-restricted** or **private/members** videos. Most
content downloads without it.

YouTube blocks downloaders from those videos unless you prove you're a real,
logged-in person. That proof is a small file — `cookies.txt` — exported from a
browser where you're signed in. Here's the whole process:

1. In **sub_scribe → Account**, click through to install the recommended browser
   cookie-export extension (a one-time install).
2. Go to YouTube (signed in), click the extension, and **Export** — a
   `cookies.txt` file lands in your Downloads.
3. **Drag that file onto the Account page.**

That's it. The app immediately tells you `✅ Connected — expires in N days`, or,
if you exported while logged out, exactly what went wrong. A badge on the
dashboard warns you a week before the login expires, so downloads never silently
stop.

> Cookies are stored at `/config/cookies.txt` with `0600` permissions and are
> never logged or shown. Per source, you can set cookie use to **Disabled**,
> **When needed** (recommended — only retries with cookies on an
> age-restriction error), or **All operations**.

### Skip cookies for most videos (optional PO-token provider)

YouTube increasingly wants a "proof of origin" (PO) token even for public videos.
sub_scribe can talk to a small **PO-token provider** so yt-dlp fetches most
content **without any cookies at all** — cookies then only matter for
age-restricted/private videos.

The provider runs as its own tiny container so the sub_scribe image stays small.
The easiest way to enable it is Docker Compose:

```bash
docker compose up -d   # starts sub_scribe + the pot-provider container
```

That's it — `docker-compose.yml` wires `SUBSCRIBE_POT_PROVIDER_URL` to the
provider automatically. If you run sub_scribe as a lone container instead, it
simply falls back to cookies. To point at a provider you host yourself, set
`SUBSCRIBE_POT_PROVIDER_URL=http://your-provider:4416`.

## Configuration

All configuration is via environment variables (sensible defaults shown):

| Variable | Default | Purpose |
|---|---|---|
| `SUBSCRIBE_DATA_DIR` | `/config` | Database, cookies, and feeds |
| `SUBSCRIBE_MEDIA_DIR` | `/media` | Where finished downloads are written |
| `SUBSCRIBE_TEMP_DIR` | `/var/tmp/sub_scribe` | Scratch space for in-progress downloads (see [Storage](#storage)) |
| `SUBSCRIBE_DB_PATH` | `<data>/sub_scribe.db` | SQLite database file |
| `SUBSCRIBE_COOKIES_PATH` | `<data>/cookies.txt` | YouTube cookie file |
| `SUBSCRIBE_FEED_DIR` | `<data>/feeds` | Generated RSS feeds |
| `SUBSCRIBE_PORT` | `8080` | HTTP port |
| `SUBSCRIBE_WORKERS` | `2` | Concurrent background workers |
| `SUBSCRIBE_JOB_RETENTION_DAYS` | `14` | How long finished jobs stay on the Jobs screen; `0` keeps them forever |
| `SUBSCRIBE_YTDLP_PATH` | `yt-dlp` | Path to the yt-dlp binary |
| `SUBSCRIBE_APPRISE_BINARY` | `apprise` | Path to Apprise (notifications) |
| `SUBSCRIBE_APPRISE_URLS` | *(none)* | Comma-separated Apprise URLs |
| `SUBSCRIBE_POT_PROVIDER_URL` | *(none)* | Base URL of a PO-token provider (skip cookies for most videos) |

## Naming templates

A media profile's output template maps each video's metadata to a path. The
default is:

```
{{ source_name }}/Season {{ upload_year }}/{{ source_name }} - {{ upload_date }} - {{ title }} [{{ id }}]
```

Available variables: `source_name`, `uploader`, `title`, `id`, `upload_date`,
`upload_year`, `season`, `episode`. Every value is sanitized for cross-platform
filesystem safety (Windows/SMB-safe), so titles like `AC/DC` never create stray
folders and templates can never escape the media root.

## Features

- Automatic tracking of channels and playlists, re-scanned on a schedule
- Instant add — background indexing and downloading via a durable task queue
- Video or audio-only downloads
- Per-source rules: upload-date cutoff, title regex filter, Shorts and
  livestream inclusion/exclusion
- Plex/Jellyfin/Kodi `.nfo` metadata sidecars
- RSS/podcast feed generation per source
- SponsorBlock (remove or mark segments)
- Retention: auto-delete media older than a configured age
- Apprise notifications
- Live UI updates over Server-Sent Events (no page reloads)
- Drag-and-drop cookie management with health/expiry warnings
- **Jobs screen**: every scan and download as a queue entry you can open —
  status, attempts, schedule, the linked channel and video, the full error, and
  the log lines that job produced (including yt-dlp's own output), refreshed
  live. Anything finished can be run again from the page.
- **Per-video detail pages** with the complete failure reason and a retry action
- Pause/resume any source without deleting it or losing its settings
- Finished jobs can be deleted individually, cleared in bulk, and are pruned
  automatically after `SUBSCRIBE_JOB_RETENTION_DAYS`
- Self-healing startup: orphaned queue entries are cleared, tasks stranded
  mid-flight are requeued, and pending videos that lost their task are queued
  again, so the app recovers instead of quietly doing nothing
- Videos the provider refuses (members-only, private, removed) are recorded as
  **unavailable** rather than retried into a generic failure

## Development

Requires Go 1.26. `yt-dlp` and `ffmpeg` are only needed at runtime (they run in
the container); the tests mock the subprocess boundary, so the full suite runs
without them.

```bash
go test ./...          # run all tests
go build ./cmd/subscribe
```

## Architecture

Clean, layered, and dependency-inverted — the domain and application core import
no infrastructure:

```
cmd/subscribe        composition root: loads config and wires everything
internal/domain      entities, enums, and rules (sources, media, profiles)
internal/library     application core: add/index/download/retain orchestration
internal/store       SQLite persistence + the durable task queue
internal/jobs        background worker pool, dispatch, retry/backoff
internal/scheduler   enqueues index/cleanup work for due sources
internal/ytdlp       yt-dlp subprocess boundary (Runner interface)
internal/naming      path template engine + cross-platform sanitization
internal/cookies     cookie parsing + login health/expiry assessment
internal/metadata    Kodi/Jellyfin .nfo writer
internal/feed        RSS/podcast feed generation
internal/sponsorblock, internal/notify, internal/events, internal/config, internal/web
```

Every collaborator with side effects (the database, yt-dlp, the clock,
notifications) sits behind an interface and is injected, so each package is
tested in isolation.
