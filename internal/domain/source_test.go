package domain

import (
	"testing"
	"time"
)

func TestSourceEffectiveCutoff(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	fixed := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("rolling window resolves relative to now", func(t *testing.T) {
		src := Source{CutoffWindow: 365 * 24 * time.Hour}
		got := src.EffectiveCutoff(now)
		if got == nil || !got.Equal(now.Add(-365*24*time.Hour)) {
			t.Errorf("EffectiveCutoff = %v, want now minus 365 days", got)
		}
	})
	t.Run("window takes precedence over fixed date", func(t *testing.T) {
		src := Source{CutoffWindow: 30 * 24 * time.Hour, DownloadCutoff: &fixed}
		got := src.EffectiveCutoff(now)
		if got == nil || !got.Equal(now.Add(-30*24*time.Hour)) {
			t.Errorf("EffectiveCutoff = %v, want the rolling window, not the fixed date", got)
		}
	})
	t.Run("falls back to fixed date when no window", func(t *testing.T) {
		src := Source{DownloadCutoff: &fixed}
		if got := src.EffectiveCutoff(now); got == nil || !got.Equal(fixed) {
			t.Errorf("EffectiveCutoff = %v, want the fixed date", got)
		}
	})
	t.Run("nil when neither set", func(t *testing.T) {
		if got := (Source{}).EffectiveCutoff(now); got != nil {
			t.Errorf("EffectiveCutoff = %v, want nil", got)
		}
	})
}

func TestSourceIsDueForIndex(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	hourAgo := now.Add(-time.Hour)
	minuteAgo := now.Add(-time.Minute)

	tests := []struct {
		name    string
		source  Source
		wantDue bool
	}{
		{
			name:    "never indexed and enabled is due",
			source:  Source{Enabled: true, IndexFrequency: 30 * time.Minute},
			wantDue: true,
		},
		{
			name:    "disabled is never due even if never indexed",
			source:  Source{Enabled: false, IndexFrequency: 30 * time.Minute},
			wantDue: false,
		},
		{
			name:    "elapsed past frequency is due",
			source:  Source{Enabled: true, IndexFrequency: 30 * time.Minute, LastIndexedAt: &hourAgo},
			wantDue: true,
		},
		{
			name:    "within frequency is not due",
			source:  Source{Enabled: true, IndexFrequency: 30 * time.Minute, LastIndexedAt: &minuteAgo},
			wantDue: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.source.IsDueForIndex(now); got != tc.wantDue {
				t.Errorf("IsDueForIndex() = %v, want %v", got, tc.wantDue)
			}
		})
	}
}
