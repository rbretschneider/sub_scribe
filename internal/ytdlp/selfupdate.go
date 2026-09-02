package ytdlp

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// updateChannel is the release channel the self-update follows. Stable is the
// right default: nightly fixes arrive days earlier but break more often, and an
// unattended archiver values not breaking over being first.
const updateChannel = "stable"

// SelfUpdate brings the yt-dlp at binaryPath up to date and returns the version
// now installed. YouTube changes constantly and an out-of-date extractor is the
// most common way every archiver breaks, so being able to fix it with a restart
// beats waiting for an image rebuild.
//
// A standalone yt-dlp updates itself via --update-to. A pip-managed install
// (which is how the Docker image ships it) refuses that flag and says to use
// pip, so that answer is detected and the pip upgrade run instead.
func SelfUpdate(ctx context.Context, binaryPath string) (string, error) {
	out, err := runForOutput(ctx, binaryPath, "--update-to", updateChannel)
	if err != nil {
		if !pipManaged(out) {
			return "", fmt.Errorf("ytdlp: self-update: %w: %s", err, lastLine(out))
		}
		if pipOut, pipErr := runForOutput(ctx,
			"python3", "-m", "pip", "install", "--quiet", "--upgrade",
			"--break-system-packages", "yt-dlp",
		); pipErr != nil {
			return "", fmt.Errorf("ytdlp: pip upgrade: %w: %s", pipErr, lastLine(pipOut))
		}
	}

	version, err := runForOutput(ctx, binaryPath, "--version")
	if err != nil {
		return "", fmt.Errorf("ytdlp: read version after update: %w", err)
	}
	return strings.TrimSpace(version), nil
}

// pipManaged reports whether yt-dlp's update output says the install is managed
// by pip or a package manager, which is the signal to update it that way
// instead. Both message generations end the same way, so that shared tail is
// what is matched.
func pipManaged(output string) bool {
	return strings.Contains(strings.ToLower(output), "use that to update")
}

// runForOutput executes a command and returns its combined output, so error
// paths can show what the tool actually said.
func runForOutput(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// lastLine returns the final non-empty line of output — where CLI tools put the
// message that matters — so errors stay one line instead of a pasted transcript.
func lastLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
