// Package notify delivers user notifications. AppriseNotifier shells out to the
// apprise CLI; NopNotifier is a no-op used when notifications are disabled.
package notify

import (
	"context"
	"fmt"
	"os/exec"

	"sub_scribe/internal/library"
)

// Apprise CLI flags used when building arguments.
const (
	flagTitle = "-t"
	flagBody  = "-b"
)

// AppriseNotifier delivers notifications by invoking the apprise CLI binary with
// one or more Apprise URLs.
type AppriseNotifier struct {
	binaryPath string
	urls       []string
}

// NewAppriseNotifier returns an AppriseNotifier that runs binaryPath and delivers
// to the given Apprise URLs.
func NewAppriseNotifier(binaryPath string, urls []string) *AppriseNotifier {
	return &AppriseNotifier{binaryPath: binaryPath, urls: urls}
}

// Notify sends title and message to the configured Apprise URLs. It is a no-op
// (returning nil) when no URLs are configured.
func (n *AppriseNotifier) Notify(ctx context.Context, title, message string) error {
	if len(n.urls) == 0 {
		return nil
	}
	args := buildArgs(title, message, n.urls)
	cmd := exec.CommandContext(ctx, n.binaryPath, args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run apprise: %w", err)
	}
	return nil
}

// buildArgs builds the apprise CLI argument list: the title and body flags
// followed by every target URL.
func buildArgs(title, body string, urls []string) []string {
	args := make([]string, 0, len(urls)+4)
	args = append(args, flagTitle, title, flagBody, body)
	args = append(args, urls...)
	return args
}

// Compile-time assertion that AppriseNotifier satisfies library.Notifier.
var _ library.Notifier = (*AppriseNotifier)(nil)
