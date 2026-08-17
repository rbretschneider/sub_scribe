# Refine: "Recently archived" media link should cover the entire row

The link to media in "Recently archived" should be the entire row entry, not only the title.

## Goal

Tapping anywhere on a "Recently archived" dashboard row — thumb, title, source, file size, or "Done" status — should navigate to the media detail page, not just the title text.

## Current behaviour

In `internal/web/templates/dashboard.html`, the "Recently archived" section renders each item as a `<div class="dl">` row with an inner `<a>` anchor that wraps only the title:

```html
<div class="dl">
  <div class="thumb" style="--tp:{{thumb .Media.ExternalID}}">…</div>
  <div>
    <div class="dl-title"><a href="/library/{{.Media.ID}}">{{.Media.Metadata.Title}}</a></div>
    <div class="dl-meta">{{.SourceName}} · {{bytes .Media.FileSize}}</div>
  </div>
  <span class="status done"><span class="d"></span>Done</span>
</div>
```

Only the title text is clickable. The thumbnail, source name, file size, and "Done" status are not linked.

## Desired behaviour

The entire `.dl` row in the "Recently archived" section should be a clickable link to `/library/{{.Media.ID}}`. The user taps anywhere on the row — thumb, title, source, size, status — and is taken to the media detail page.

Concrete implementation in `internal/web/templates/dashboard.html`:

- Change the outer `<div class="dl">` to `<a class="dl" href="/library/{{.Media.ID}}">`.
- Remove the inner `<a>` that wrapped the title — the title becomes plain text inside the row.

CSS in `internal/web/static/app.css` already has `.dl` styling and the `.dl-title a:hover` rule (accent color, no underline) becomes unused but harmless. The existing `.dl` grid layout and status pills render unchanged.

## Out of scope

- Do not change the "Downloading" or "Up next" rows in the dashboard.
- Do not add hover states, animations, or focus indicators beyond what the existing `.dl` styles provide.
- Do not change the route (`/library/{{.Media.ID}}`) — same destination as before.
- Do not modify CSS beyond what is required by the structural change.
- Do not touch other templates (`library.html`, `source_detail.html`, etc.).
- Do not add tests — the change is a single template structural edit with no new logic.

## Acceptance

```
go test -short -race ./...
```
