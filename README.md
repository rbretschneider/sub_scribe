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

## Installation

Prebuilt images are published to the GitHub Container Registry for `linux/amd64`
and `linux/arm64`, so the same command works on a normal server and on a
Raspberry Pi.

| Tag | What it tracks |
| --- | --- |
| `latest` | the tip of `main` |
| `1.2.3` | an exact release |
| `1.2` / `1` | the newest patch / minor of that line |

### Option 1 — Docker Compose (recommended)

Compose is the easier route because it also runs the optional PO-token provider,
which lets most videos download **without** needing your YouTube cookies.

```bash
curl -O https://raw.githubusercontent.com/rbretschneider/sub_scribe/main/docker-compose.yml
# edit the ./media line to point at your Plex/Jellyfin library, then:
docker compose up -d
```

### Option 2 — a single container

```bash
docker run -d \
  --name sub_scribe \
  -p 8080:8080 \
  -v sub_scribe_config:/config \
  -v /path/to/your/media:/media \
  --restart unless-stopped \
  ghcr.io/rbretschneider/sub_scribe:latest
```

Mind the two volumes — they are deliberately different kinds, and
[Storage](#storage) explains why:

- **`/config`** is a *named volume*. It holds the database, your cookies, and
  generated feeds.
- **`/media`** is a *bind mount* to a real folder on your disk. These are the
  video files, and this is the path you point Plex or Jellyfin at.

### First run

Open <http://localhost:8080>. A default "1080p, Plex layout" profile is created
for you, so you can add a channel immediately: paste a channel or playlist URL,
choose how far back to download, and save. The scan starts in the background and
the **Jobs** screen shows it working.

Files are named the way media servers expect, for example:

```
Computerphile/Season 2026/s2026e071601 - GPS Hidden Messages.mkv
Computerphile/Season 2026/s2026e071601 - GPS Hidden Messages.jpg
Computerphile/Season 2026/s2026e071601 - GPS Hidden Messages.nfo
```

`s2026e071601` is season-by-year, episode-by-upload-date, plus a two-digit index
so two videos posted on the same day get distinct episode numbers. That token is
what makes the layout work: a media server parses it and takes the title from the
rest of the filename.

### Pointing a media server at it

Add the media folder as a **TV Shows** library. Each channel becomes a show and
each year a season.

**Plex** needs one setting changed, or it will show invented titles like
`Episode 04-22`:

- Library → *Edit* → **Advanced**
- **Agent: Personal Media Shows**

That tells Plex to take titles from the filenames instead of trying to match your
channel against its TV database. Note that **Plex does not read `.nfo` files** —
it has no native NFO agent — so the sidecars are there for other servers and for
the third-party XBMCnfo agent if you use it.

**Jellyfin, Emby and Kodi** read the `.nfo` sidecars natively and need no extra
configuration.

### Upgrading

```bash
docker compose pull && docker compose up -d      # compose
docker pull ghcr.io/rbretschneider/sub_scribe:latest   # single container
```

Schema migrations run automatically at startup, and anything already on disk is
re-adopted, so upgrading never loses your archive.

### Building it yourself

```bash
git clone https://github.com/rbretschneider/sub_scribe.git
cd sub_scribe
docker build -t sub_scribe .
```

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

## Pacing

sub_scribe deliberately throttles itself, and this is on by default.

The reason is your account. Uploading cookies means every request YouTube sees
is signed in as you, and a signed-in client that pulls video after video with no
pause looks like exactly what it is. The penalty for that is not a failed
download, it is the account — and replacing a Google account now means handing
over a phone number, which is not something to lose casually.

**Out of the box it downloads roughly one video every ten minutes.** That is a
deliberate choice, not a limitation. Nothing here is urgent: an archiver that
quietly collects a channel over a day is doing its job, while one that empties a
channel in ten minutes is doing its job in the most conspicuous way available.

Four settings, all in the table below:

- **`SUBSCRIBE_DOWNLOAD_DELAY_MIN_SECONDS`/`_MAX_SECONDS`** are the interval
  between the start of one download and the next, picked at random from that
  range each time. Randomised on purpose: a perfectly regular gap is itself a
  signature.
- **`SUBSCRIBE_REQUEST_DELAY_SECONDS`** pauses between individual HTTP requests
  within one yt-dlp run. Most traffic is metadata lookups rather than downloads,
  so this is the one that covers the bulk of it.
- **`SUBSCRIBE_CALL_GAP_SECONDS`** is a floor on how close together two yt-dlp
  processes may start, so several workers cannot fire at the same instant.
- **`SUBSCRIBE_RATE_LIMIT`** caps download bandwidth (`4M`, `500K`). Off by
  default.

Set any of them to `0` to turn that measure off.

Waiting for a turn happens **in the queue, not in a worker**. A download that is
not due yet is put back with a later start time and says so on its job page, so
indexing, feed generation, and anything you click keep running normally while the
archive fills in slowly in the background. Nothing is blocked behind a ten-minute
pause, and the wait survives a restart.

This applies only to items actually being downloaded. Discarding a back
catalogue — hundreds of videos rejected for being outside a source's date window
— is not paced, because there is nothing being fetched to pace.

The resolved settings are logged at startup, so a run that feels slow can be
identified as deliberate restraint rather than a problem:

```
INFO provider pacing request_delay=2s download_every=8m0s–12m0s call_gap=5s rate_limit=unlimited
```

Anything you pass yourself through a media profile's extra yt-dlp arguments wins
over these, since yt-dlp takes the last occurrence of a flag.

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
| `SUBSCRIBE_REQUEST_DELAY_SECONDS` | `2` | Pause between HTTP requests (see [Pacing](#pacing)); `0` disables |
| `SUBSCRIBE_DOWNLOAD_DELAY_MIN_SECONDS` | `480` | Lower bound of the random interval between downloads |
| `SUBSCRIBE_DOWNLOAD_DELAY_MAX_SECONDS` | `720` | Upper bound of that interval |
| `SUBSCRIBE_CALL_GAP_SECONDS` | `5` | Minimum spacing between yt-dlp launches, across all workers |
| `SUBSCRIBE_RATE_LIMIT` | *(unlimited)* | Download bandwidth cap in yt-dlp notation, e.g. `4M` or `500K` |

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
