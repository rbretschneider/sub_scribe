# Goal

Show the user feedback when they click "Retry all failed" on a source detail page, so they know the action worked and how many downloads are being retried.

# Current behaviour

- The "Retry all failed" button on `/sources/{id}` (in `internal/web/templates/source_detail.html`) submits a form to `POST /sources/{id}/retry-failed`.
- The handler `handleSourceRetryFailed` in `internal/web/handlers.go` calls `s.deps.Library.RetryAllFailed()`, which requeues failed tasks to pending status, then redirects back to the source detail page.
- The count of retried tasks (`n`) is discarded with `_ = n`.
- The source detail page shows no indication that a retry was requested or completed — no flash message, no disabled state on the button, no activity log entry.
- The button itself has no visual feedback on click (no spinner, no disabled state, no `data-confirm` style dialog).

# Desired behaviour

- After the retry action completes, the source detail page shows a one-time notice (using the existing `.alert.alert-info` pattern) stating how many failed downloads were requeued, e.g. "3 failed downloads have been requeued."
- The notice disappears on the next page load (no session/cookie persistence needed).

Implementation approach:
1. Pass the retry count via a query parameter (`?retried=3`) on the redirect in `handleSourceRetryFailed`.
2. In the source detail template, show an alert when the `retried` query parameter is present and non-zero.

Tests to add:
- Handler test: verify the redirect includes the `?retried=N` query parameter when tasks are requeued.
- Template test: verify the alert is shown when `retried` is present and non-zero.
- Template test: verify the alert is hidden when `retried` is absent or zero.

# Out of scope

- Do not add a session/flash message system.
- Do not modify the confirmation dialog used for delete actions.
- Do not add spinners or loading states to the button.
- Do not change the retry logic itself (the SQL, the service layer, the worker pool).
- Do not modify the "Download again" button on media detail pages (only the source-level "Retry all failed" button).
- Do not add analytics or logging beyond what already exists.
- Do not change the behavior of the "Scan now" or "Pause/Resume" buttons.

# Acceptance

After implementing the changes:

```shell
go test -short -race ./...
```

This verifies:
1. All existing tests still pass (no regressions).
2. New handler test confirms the `?retried=N` query parameter is present on redirect.
3. New template test confirms the alert appears when `retried` is non-zero.
4. New template test confirms the alert is hidden when `retried` is absent or zero.
