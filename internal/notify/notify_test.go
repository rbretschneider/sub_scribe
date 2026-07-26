package notify

import (
	"context"
	"reflect"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
		urls  []string
		want  []string
	}{
		{
			name:  "single url",
			title: "Done",
			body:  "Downloaded episode",
			urls:  []string{"discord://token"},
			want:  []string{"-t", "Done", "-b", "Downloaded episode", "discord://token"},
		},
		{
			name:  "multiple urls",
			title: "Alert",
			body:  "Two targets",
			urls:  []string{"discord://a", "mailto://b"},
			want:  []string{"-t", "Alert", "-b", "Two targets", "discord://a", "mailto://b"},
		},
		{
			name:  "no urls",
			title: "Empty",
			body:  "No targets",
			urls:  nil,
			want:  []string{"-t", "Empty", "-b", "No targets"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildArgs(tt.title, tt.body, tt.urls)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAppriseNotifyNoURLsIsNoop(t *testing.T) {
	// With no URLs, Notify must return nil without attempting to run the binary.
	n := NewAppriseNotifier("/nonexistent/apprise", nil)
	if err := n.Notify(context.Background(), "title", "body"); err != nil {
		t.Errorf("Notify() with no urls = %v, want nil", err)
	}
}

func TestNopNotifierNotify(t *testing.T) {
	n := NewNopNotifier()
	if err := n.Notify(context.Background(), "title", "body"); err != nil {
		t.Errorf("Notify() = %v, want nil", err)
	}
}
