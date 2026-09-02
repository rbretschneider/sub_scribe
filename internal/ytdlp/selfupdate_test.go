package ytdlp

import "testing"

func TestPipManagedRecognisesBothMessageGenerations(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "current pip wording",
			output: "ERROR: You installed yt-dlp with pip or using the wheel from PyPi; Use that to update",
			want:   true,
		},
		{
			name:   "older package-manager wording",
			output: "It looks like you installed yt-dlp with a package manager, pip or setup.py; Use that to update",
			want:   true,
		},
		{
			name:   "a standalone binary updating normally",
			output: "Latest version: stable@2026.08.20, Current version: stable@2026.08.20\nyt-dlp is up to date",
			want:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := pipManaged(test.output); got != test.want {
				t.Errorf("pipManaged(%q) = %v, want %v", test.output, got, test.want)
			}
		})
	}
}

func TestLastLineFindsTheMessageThatMatters(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "multi-line output", output: "Updating...\nERROR: no permission\n\n", want: "ERROR: no permission"},
		{name: "single line", output: "done", want: "done"},
		{name: "empty output", output: "", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := lastLine(test.output); got != test.want {
				t.Errorf("lastLine(%q) = %q, want %q", test.output, got, test.want)
			}
		})
	}
}
