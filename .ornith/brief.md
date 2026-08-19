# Architecture Brief: "Retry all failed" user feedback

## Goal

After clicking "Retry all failed" on `/sources/{id}`, the user sees a one-time `.alert.alert-info` notice on the source detail page stating how many failed downloads were requeued; the notice disappears on the next page load.

## Files to change

| File | Why |
|---|---|
| `internal/web/handlers.go` | Pass `n` into the view struct (instead of `_ = n`). |
| `internal/web/templates/source_detail.html` | Render the alert when `Retried > 0`. |
| `internal/web/server_test.go` | Make `fakeLibrary.RetryAllFailed` return a configurable count. |
| `internal/web/server_sources_test.go` | Add 3 tests (handler + 2 template). |

## Approach

1. **Handler** (`handleSourceRetryFailed`): keep the redirect, append `?retried=N`:
   ```go
   n, err := s.deps.Library.RetryAllFailed(r.Context(), id)
   // … keep the existing 500 guard …
   redirect(w, r, fmt.Sprintf("/sources/%d?retried=%d", id, n))
   ```

2. **Template** — after the paused-source alert block, add:
   ```go
   {{if gt .Retried 0}}
   <div class="alert alert-info">
     <p>{{.Retried}} failed download{{if ne .Retried 1}}s{{end}} have been requeued.</p>
   </div>
   {{end}}
   ```
   Follows the existing `.alert.alert-info` pattern and the `ne .Retried 1` pluralisation idiom already used in this template (`ne .Stats.Files 1`).

3. **Tests** (in `server_sources_test.go`):
   - `TestSourceRetryFailedRedirectsWithCount` — stub `fakeLibrary.RetryAllFailed` to return `(3, nil)`, POST to `/sources/7/retry-failed`, assert 303 redirect to `/sources/7?retried=3`.
   - `TestSourceRetryFailedAlertShownWhenNonZero` — GET `/sources/7?retried=3`, assert body contains `3 failed downloads have been requeued.`.
   - `TestSourceRetryFailedAlertHiddenWhenAbsent` — GET `/sources/7` (no query param), assert body does NOT contain `have been requeued`.

   Make `fakeLibrary` carry a `retryCount int` field (default 0) so tests can pick a value per case.

## Data impact

**NO.** No schema, migration, SQL, or persisted data shape changes.

## Acceptance criteria

- `go test -short -race ./...` passes.
- POST to `/sources/{id}/retry-failed` with non-zero count shows the alert.
- POST with zero count does not show the alert.
- Plain GET to `/sources/{id}` does not show the alert.
- The alert uses `.alert.alert-info` and disappears on the next load (no cookies/session).

## Risks

- **Template render-time failure**: if `Retried` is referenced in the template but missing from the struct, `go build` still passes and only a handler test that actually renders the page catches it. The three template tests above cover this.
- **Pluralisation edge**: `ne .Retried 1` handles singular correctly; verify the test for `n=1` shows "1 failed download has been requeued" (without trailing 's').
- **Scope creep**: do NOT touch the button's click behavior (no spinner), do NOT add a session/flash system, do NOT modify the delete confirmation dialog.
