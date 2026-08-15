# Architecture Brief: Favicon / sidebar logo for sub_scribe

## Goal

Serve an SVG favicon via `/static/favicon.svg`, link it from the layout `<head>`, and replace the 9 px amber `.brand-dot` CSS dot in the sidebar with the same SVG so the browser tab and sidebar share one mark.

## Files to change

| File | Why |
|------|-----|
| `internal/web/static/favicon.svg` *(new)* | Single source of truth for the brand mark. Served by the existing `//go:embed static/*` directive in `server.go`. |
| `internal/web/templates/layout.html` | Add `<link rel="icon" href="/static/favicon.svg" type="image/svg+xml">` next to the stylesheet link (line 8). Replace `<span class="brand-dot" aria-hidden="true">` (line 15) with an inline `<svg>` so the sidebar logo matches the tab. |
| `internal/web/static/app.css` | Remove the `.brand-dot` rules (line 59) — they no longer apply to an inline `<svg>`. Keep `.brand` and `.brand-name` untouched. |

## Approach

1. **Design the SVG** — play triangle inside a rounded square (24×24 viewBox), filled with `#f2a63b`, stroked with `#d9871c` for legibility at 16 px. Under ~300 bytes. No text, no gradients. Readable on both dark and light backgrounds using amber fill only.
2. **Favicon link** — insert the `<link>` immediately after the stylesheet `<link>` on line 8 of `layout.html`.
3. **Sidebar replacement** — swap the `<span class="brand-dot" aria-hidden="true">` (line 15) for `<svg class="brand-dot" aria-hidden="true" viewBox="0 0 24 24" …>` containing the same paths as `favicon.svg`. Size with `width:18px;height:18px;flex:none`. Keep `aria-hidden="true"`.
4. **CSS cleanup** — delete `.brand-dot { … }` from `app.css` (line 59). The inline SVG carries its own fill.

## Data impact

**NO** — no schema, migration, SQL, or persisted data changes.

## Acceptance criteria

- `go test -short -race ./...` passes with no output.
- `internal/web/static/favicon.svg` is well-formed XML: starts with `<svg`, has `xmlns`, `viewBox`, no stray characters before the opening tag.
- `layout.html` contains `<link rel="icon" href="/static/favicon.svg" …>`.
- The sidebar brand area still renders `sub<b>_</b>scribe` and the logo element carries `aria-hidden="true"`.

## Risks

- **Layout shift in the sidebar.** The original `.brand-dot` is 9×9 px with a soft glow; the new SVG is larger. Set explicit `width:18px;height:18px` on the inline SVG and verify the `.brand` flex row still aligns with the wordmark (`gap:10px` on `.brand` is fine).
- **Orphan CSS.** Leaving `.brand-dot` in `app.css` is harmless but misleading — delete it.
- **Embed glob.** The existing `//go:embed static/*` in `server.go` picks up new files automatically — no embed changes needed.
