package pacing

import (
	"context"
	"testing"
	"time"
)

var noon = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// TestTryClaimSpacesStarts is the property the whole package exists for: after
// one caller goes, the next is told to come back later rather than being let
// through. Without it several workers would start downloads together and the
// pacing would be pacing in name only.
func TestTryClaimSpacesStarts(t *testing.T) {
	now := noon
	pacer := New(Fixed(10 * time.Minute))
	pacer.now = func() time.Time { return now }

	start, ok := pacer.TryClaim()
	if !ok {
		t.Fatal("the first caller was refused; nothing should be waiting on an idle pacer")
	}
	if !start.Equal(noon) {
		t.Errorf("first start = %v, want %v", start, noon)
	}

	next, ok := pacer.TryClaim()
	if ok {
		t.Fatal("a second caller was let through immediately, so the gap is not enforced")
	}
	if want := noon.Add(10 * time.Minute); !next.Equal(want) {
		t.Errorf("next slot = %v, want %v", next, want)
	}
}

// TestRefusedClaimTakesNothing is what stops a queue of waiting items from
// pushing the schedule ever further out. Every refused caller that still
// consumed its slot would add another interval, so a hundred waiting downloads
// would schedule the next one a thousand minutes away.
func TestRefusedClaimTakesNothing(t *testing.T) {
	now := noon
	pacer := New(Fixed(10 * time.Minute))
	pacer.now = func() time.Time { return now }

	pacer.TryClaim() // the slot is now taken
	want := noon.Add(10 * time.Minute)
	for i := range 50 {
		slot, ok := pacer.TryClaim()
		if ok {
			t.Fatalf("caller %d was let through during the gap", i)
		}
		if !slot.Equal(want) {
			t.Fatalf("caller %d was sent to %v, want %v — the queue is drifting", i, slot, want)
		}
	}
}

func TestTryClaimSucceedsOnceTheGapHasPassed(t *testing.T) {
	now := noon
	pacer := New(Fixed(10 * time.Minute))
	pacer.now = func() time.Time { return now }

	pacer.TryClaim()
	now = noon.Add(10 * time.Minute)
	if _, ok := pacer.TryClaim(); !ok {
		t.Fatal("the slot was still refused after the gap elapsed")
	}
}

func TestZeroGapNeverRefuses(t *testing.T) {
	now := noon
	pacer := New(Fixed(0))
	pacer.now = func() time.Time { return now }

	for i := range 3 {
		if _, ok := pacer.TryClaim(); !ok {
			t.Fatalf("caller %d was refused although pacing is disabled", i)
		}
	}
}

func TestNilGapIsDisabled(t *testing.T) {
	if _, ok := New(nil).TryClaim(); !ok {
		t.Fatal("a nil gap must behave as disabled, not as an infinite wait")
	}
}

func TestJitteredStaysWithinItsRange(t *testing.T) {
	const min, max = 8 * time.Minute, 12 * time.Minute
	gap := Jittered(min, max)
	for range 200 {
		if d := gap(); d < min || d > max {
			t.Fatalf("gap = %v, want within %v..%v", d, min, max)
		}
	}
}

// TestJitteredActuallyVaries guards the reason for the jitter: a fixed interval
// is itself a recognisable signature, so a "random" gap that always returned the
// same number would defeat the point while looking correct.
func TestJitteredActuallyVaries(t *testing.T) {
	gap := Jittered(8*time.Minute, 12*time.Minute)
	first := gap()
	for range 100 {
		if gap() != first {
			return
		}
	}
	t.Fatalf("100 draws all returned %v; the interval is not being varied", first)
}

func TestJitteredCollapsesToFixedWhenMaxIsNotAbove(t *testing.T) {
	for _, test := range []struct{ min, max time.Duration }{
		{10 * time.Minute, 10 * time.Minute},
		{10 * time.Minute, 4 * time.Minute},
	} {
		if got := Jittered(test.min, test.max)(); got != test.min {
			t.Errorf("Jittered(%v, %v) = %v, want %v", test.min, test.max, got, test.min)
		}
	}
}

// TestWaitSpacesReservations covers the other half of the API: callers that wait
// rather than reschedule must still each get a distinct slot, not all wake at
// once and race.
func TestWaitSpacesReservations(t *testing.T) {
	pacer := New(Fixed(5 * time.Second))
	pacer.now = func() time.Time { return noon }

	for i := range 4 {
		want := noon.Add(time.Duration(i) * 5 * time.Second)
		if got := pacer.reserve(); !got.Equal(want) {
			t.Errorf("reservation %d = %v, want %v", i, got, want)
		}
	}
}

// TestWaitHonoursCancellation makes sure a shutdown interrupts the wait and is
// reported, rather than either hanging or quietly proceeding unpaced.
func TestWaitHonoursCancellation(t *testing.T) {
	pacer := New(Fixed(time.Hour))
	pacer.reserve() // claim the first slot so the next caller has to wait

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := pacer.Wait(ctx); err == nil {
		t.Fatal("Wait() returned nil for a cancelled context; the call would proceed unpaced")
	}
}

func TestWaitReturnsImmediatelyWhenDisabled(t *testing.T) {
	if err := New(Fixed(0)).Wait(context.Background()); err != nil {
		t.Fatalf("Wait() with no gap = %v, want nil", err)
	}
}
