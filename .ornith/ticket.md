# Favicon / logo for sub_scribe

## Goal

Give sub_scribe a favicon and sidebar logo — an SVG that fits the app's amber
accent — and wire it into the layout so browsers and the sidebar chrome both
show it.

## Current behaviour

- The web UI has no favicon. The browser tab and bookmarks show the default
  browser icon.
- The sidebar logo is a 9×9 CSS dot (`.brand-dot` in
  `internal/web/static/app.css` line 59) — an amber circle with a soft glow —
  next to the monospace `sub_scribe` wordmark in `internal/web/templates/layout.html`
  lines 15–16.
- Static files are served from `internal/web/static/` via the `//go:embed
  static/*` directive in `internal/web/server.go` lines 33–34, routed at
  `/static/` (see `internal/web/routes.go` line 41, 90).
- The layout template (`internal/web/templates/layout.html`) has no `<link
  rel="icon">` tag.

## Desired behaviour

1. Add an SVG favicon at `internal/web/static/favicon.svg`. The mark should be
   legible at 16×16 and 32×32 (the sizes browsers actually use), work on both
   dark and light backgrounds, and echo the app's amber accent
   (`#f2a63b` / `#d9871c`) and play-button / subscription motif without
   reproducing YouTube's branding. A simple play triangle inside a rounded
   square or circle is fine — keep the file small and readable.
2. Link the SVG in `internal/web/templates/layout.html` inside the `<head>`:
   `<link rel="icon" href="/static/favicon.svg" type="image/svg+xml">`. Place it
   next to the existing stylesheet link so the favicon loads early.
3. Replace the `.brand-dot` CSS dot in the sidebar with the same SVG (inline or
   via `<img>`), so the sidebar logo matches the browser tab. The wordmark
   `sub<b>_</b>scribe` stays next to it. The replacement must keep the element
   `aria-hidden="true"` so accessibility is unchanged.

That's it. The favicon should appear in the browser tab, bookmarks, and tab bar,
and the sidebar should show the same mark next to the wordmark.

## Out of scope

- No PNG or ICO fallbacks. The request is for an SVG.
- No `manifest.json`, no PWA support, no `apple-touch-icon`, no `<meta
  theme-color>`.
- No hover states, animations, or colour changes on the logo.
- No changes to `app.js`, `app.css` beyond what is needed for the brand-dot
  replacement (and only if the replacement needs it).
- No changes to routes, handlers, or the embed directive beyond relying on the
  existing `static/*` glob to pick up the new file.
- No changes to the dashboard, library, sources, jobs, logs, profiles, or any
  other page beyond the layout template's `<head>` and sidebar brand area.
- No changes to the design concept file (`design/ui-concept.html`).
- No dependency additions.

## Acceptance

```
go test -short -race ./...
```

The existing test suite must still pass. Concretely:

- `go test -short -race ./...` succeeds with no output.
- `internal/web/static/favicon.svg` exists and is well-formed XML (an SVG
  element, `xmlns`, a `viewBox`, and no stray characters before `<svg`).
- The layout template contains a `<link>` referencing `/static/favicon.svg`
  with `rel="icon"`.
- The sidebar brand area still renders the wordmark `sub<b>_</b>scribe` and the
  logo element is `aria-hidden="true"`.
