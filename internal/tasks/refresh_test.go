package tasks

import (
	"context"
	"errors"
	"testing"

	"sub_scribe/internal/jobs"
)

type fakeRefresher struct {
	called   bool
	sourceID int64
	changed  int
	err      error
}

func (f *fakeRefresher) RefreshSourceSidecars(_ context.Context, sourceID int64) (int, error) {
	f.called = true
	f.sourceID = sourceID
	return f.changed, f.err
}

func TestRefreshSidecarsHandler(t *testing.T) {
	id := int64(9)
	t.Run("delegates with the task's source", func(t *testing.T) {
		f := &fakeRefresher{changed: 3}
		if err := RefreshSidecarsHandler(f)(context.Background(), jobs.Task{SourceID: &id}); err != nil {
			t.Fatal(err)
		}
		if !f.called || f.sourceID != 9 {
			t.Errorf("refresher called = %v with %d, want source 9", f.called, f.sourceID)
		}
	})
	t.Run("missing source id fails loudly", func(t *testing.T) {
		f := &fakeRefresher{}
		if err := RefreshSidecarsHandler(f)(context.Background(), jobs.Task{}); err == nil || f.called {
			t.Errorf("err = %v, called = %v; want an error and no call", err, f.called)
		}
	})
	t.Run("propagates errors", func(t *testing.T) {
		want := errors.New("boom")
		f := &fakeRefresher{err: want}
		if err := RefreshSidecarsHandler(f)(context.Background(), jobs.Task{SourceID: &id}); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestRegisterWiresRefreshSidecars(t *testing.T) {
	reg := jobs.NewRegistry()
	Register(reg, Deps{SidecarRefresher: &fakeRefresher{}})
	if !reRegisterPanics(reg, jobs.TaskRefreshSidecars) {
		t.Error("refresh_sidecars was not registered by Register")
	}
}
