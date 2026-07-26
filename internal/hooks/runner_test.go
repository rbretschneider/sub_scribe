package hooks

import (
	"context"
	"testing"
)

func TestRunEmptyCommandIsNoop(t *testing.T) {
	r := NewRunner()
	for _, command := range []string{"", "   ", "\t"} {
		if err := r.Run(context.Background(), command, "/media/video.mp4"); err != nil {
			t.Errorf("Run(%q) = %v, want nil (empty command is a no-op)", command, err)
		}
	}
}
