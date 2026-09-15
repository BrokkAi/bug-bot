package bugbot

import (
	"context"
	"errors"
	"testing"
	"time"
)

type timedFailingAgent struct {
	now      *time.Time
	duration time.Duration
	err      error
}

func (a timedFailingAgent) Execute(context.Context, string) (string, error) {
	*a.now = a.now.Add(a.duration)
	return "", a.err
}

func TestRetryDelayAfterFailure(t *testing.T) {
	for _, duration := range []time.Duration{time.Minute, 20 * time.Minute} {
		t.Run(duration.String(), func(t *testing.T) {
			e, s, f, _, _ := fixture(t)
			e.config.Poll = Duration(time.Minute)
			e.config.RetryDelay = Duration(15 * time.Minute)
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			e.now = func() time.Time { return now }
			failure := errors.New("agent failed")
			e.agent = func(Config) Agent { return timedFailingAgent{&now, duration, failure} }
			ctx := context.Background()
			if err := e.step(ctx, s, false); !errors.Is(err, failure) {
				t.Fatalf("expected agent failure, got %v", err)
			}
			deadline := now.Add(time.Duration(e.config.RetryDelay))
			// Reload to verify that the deadline survives a restart.
			saved, err := ReadState(e.config)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Scan == nil || !saved.Scan.RetryAt.Equal(deadline) {
				t.Fatalf("expected retry deadline %s, got %+v", deadline, saved.Scan)
			}
			for _, poll := range []time.Time{now.Add(time.Duration(e.config.Poll)), deadline.Add(-time.Nanosecond)} {
				now = poll
				if err := e.step(ctx, saved, false); err != nil {
					t.Fatal(err)
				}
				if saved.Scan.Tries != 1 || f.reads != 1 {
					t.Fatalf("retried before deadline: tries=%d, history reads=%d", saved.Scan.Tries, f.reads)
				}
			}
			now = deadline
			if err := e.step(ctx, saved, false); !errors.Is(err, failure) {
				t.Fatalf("expected retry at deadline to fail, got %v", err)
			}
			if saved.Scan.Tries != 2 || f.reads != 2 || !saved.Scan.RetryAt.Equal(now.Add(time.Duration(e.config.RetryDelay))) {
				t.Fatalf("retry did not update attempt and deadline: %+v, history reads=%d", saved.Scan, f.reads)
			}
		})
	}
}
