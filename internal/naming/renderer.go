package naming

import (
	"fmt"
	"regexp"
	"strings"
)

// tokenPattern matches a template placeholder like "{{ upload_date }}" and
// captures the variable name. Whitespace inside the braces is optional.
var tokenPattern = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// knownVariables is the set of placeholders the renderer will resolve. A
// template referencing anything outside this set is rejected at validation time
// rather than silently rendering an empty value (principle of least surprise).
var knownVariables = map[Variable]struct{}{
	VarSourceName: {}, VarUploader: {}, VarTitle: {}, VarID: {},
	VarUploadDate: {}, VarUploadYear: {}, VarSeason: {}, VarEpisode: {},
	VarUploadMMDD: {}, VarSeasonEpisode: {},
}

// Renderer turns path templates into concrete relative paths. It is stateless
// and safe for concurrent use; callers may share a single instance.
type Renderer struct{}

// NewRenderer constructs a Renderer.
func NewRenderer() *Renderer { return &Renderer{} }

// Validate checks that a template references only known variables and is
// non-empty. It is called before a MediaProfile is saved so users learn about a
// bad template immediately instead of at download time.
func (r *Renderer) Validate(template string) error {
	if strings.TrimSpace(template) == "" {
		return fmt.Errorf("naming template must not be empty")
	}
	for _, match := range tokenPattern.FindAllStringSubmatch(template, -1) {
		name := Variable(match[1])
		if _, ok := knownVariables[name]; !ok {
			return fmt.Errorf("unknown template variable %q", match[1])
		}
	}
	return nil
}

// Render resolves the template against the context and returns a slash-separated
// relative path (no leading slash, no file extension). Each substituted value is
// sanitized for cross-platform filesystem safety before insertion, so literal
// separators in the template define directories while separators inside values
// do not. Callers join the result with the media root using filepath.
func (r *Renderer) Render(template string, ctx Context) (string, error) {
	if err := r.Validate(template); err != nil {
		return "", fmt.Errorf("render: %w", err)
	}

	values := ctx.values()
	rendered := tokenPattern.ReplaceAllStringFunc(template, func(token string) string {
		name := Variable(tokenPattern.FindStringSubmatch(token)[1])
		return sanitizeComponent(values[name])
	})

	path, err := normalizePath(rendered)
	if err != nil {
		return "", fmt.Errorf("render: %w", err)
	}
	return path, nil
}

// normalizePath cleans the substituted template into a safe relative slash path:
// it drops empty segments (from adjacent or leading separators), rejects parent
// traversal, and requires at least one segment.
func normalizePath(rendered string) (string, error) {
	rawSegments := strings.Split(rendered, "/")
	segments := make([]string, 0, len(rawSegments))
	for _, seg := range rawSegments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if seg == ".." {
			return "", fmt.Errorf("template must not contain parent-directory segments")
		}
		segments = append(segments, seg)
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("template rendered to an empty path")
	}
	return strings.Join(segments, "/"), nil
}
