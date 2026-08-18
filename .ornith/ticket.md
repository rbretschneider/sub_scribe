# Dashboard layout: full-width downloading panel

## Goal

Rearrange the dashboard so the "Downloading" panel spans the full width at the top, with "Recently archived" and "Retention queue" side by side underneath.

## Current behaviour

`internal/web/templates/dashboard.html` renders four panels inside a `<div class="grid-2">`:

1. **Downloading** (lines 35-57) - Shows active downloads with progress bars
2. **Recently archived** (lines 60-76) - Shows recently completed downloads
3. **Up next** (lines 78-94) - Conditional panel showing queued items (only when `.UpNext` is non-empty)
4. **Retention queue** (lines 96-114) - Shows media scheduled for deletion

The `grid-2` CSS class (defined in `internal/web/static/app.css`) creates a two-column grid layout, placing all panels side by side in a single row.

## Desired behaviour

- The "Downloading" panel sits full-width at the top of the dashboard (outside the `grid-2` container).
- The "Recently archived" and "Retention queue" panels sit side by side (50/50) underneath the downloading panel.
- The "Up next" panel is removed — it was conditional and redundant with the "Downloading now" stat in the overview section.

## Out of scope

- Do not change the stats row (Videos archived, Downloading now, Sources, Failed).
- Do not change the page head or eyebrow.
- Do not modify any Go code, service layer, or store queries.
- Do not add hover states, sorting, or pagination to the panels.
- Do not change the CSS file (`internal/web/static/app.css`).
- Do not touch the library page (`internal/web/templates/library.html`) or any other template.

## Acceptance

    go test -short -race ./...
