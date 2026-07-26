// Package hooks runs user-configured lifecycle commands, currently the
// post-download hook. It is the subprocess boundary for profile scripts, kept out
// of the application core so the core depends only on the library's hook
// interface and stays testable without executing anything.
package hooks

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"sub_scribe/internal/library"
)

// Runner executes lifecycle commands as subprocesses.
type Runner struct{}

// Compile-time assertion that Runner satisfies the library port.
var _ library.PostDownloadHook = (*Runner)(nil)

// NewRunner constructs a Runner.
func NewRunner() *Runner { return &Runner{} }

// Run executes command with mediaPath as its single argument. An empty command
// is a no-op, so callers can invoke it unconditionally. The command is run
// directly (not through a shell), so it is an executable or script path rather
// than a shell expression, avoiding shell-injection surprises.
func (r *Runner) Run(ctx context.Context, command, mediaPath string) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, command, mediaPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hooks: post-download command %q: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	return nil
}
