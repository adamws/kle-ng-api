package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDo_SucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), 3, time.Millisecond, nil, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
}

func TestDo_SucceedsAfterRetries(t *testing.T) {
	calls := 0
	err := Do(context.Background(), 5, time.Millisecond, nil, func() error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("fn called %d times, want 3", calls)
	}
}

func TestDo_ExhaustionReturnsLastError(t *testing.T) {
	calls := 0
	want := errors.New("still failing")
	err := Do(context.Background(), 3, time.Millisecond, nil, func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("Do returned %v, want %v", err, want)
	}
	if calls != 3 {
		t.Fatalf("fn called %d times, want 3", calls)
	}
}

func TestDo_NonRetryableFailsFast(t *testing.T) {
	calls := 0
	permanent := errors.New("permanent")
	err := Do(context.Background(), 5, time.Millisecond,
		func(error) bool { return false },
		func() error {
			calls++
			return permanent
		})
	if !errors.Is(err, permanent) {
		t.Fatalf("Do returned %v, want %v", err, permanent)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1 (no retry on permanent error)", calls)
	}
}

func TestDo_ContextCancelAbortsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	start := time.Now()
	err := Do(ctx, 5, time.Hour, nil, func() error {
		calls++
		cancel() // cancel during the first attempt; backoff must not sleep an hour
		return errors.New("transient")
	})
	if err == nil {
		t.Fatal("Do returned nil, want the transient error")
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Do blocked for %v; ctx cancel should abort backoff promptly", elapsed)
	}
}

func TestDo_ZeroAttemptsRunsOnce(t *testing.T) {
	calls := 0
	_ = Do(context.Background(), 0, time.Millisecond, nil, func() error {
		calls++
		return errors.New("fail")
	})
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
}
