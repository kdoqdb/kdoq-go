// Package retry runs an operation again with jittered exponential backoff.
package retry

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// Policy bounds the attempts and spaces them out. Zero fields take the
// defaults: 3 attempts, 100ms initial delay, 5s maximum, factor 2, jitter.
type Policy struct {
	Attempts int
	Initial  time.Duration
	Max      time.Duration
	Factor   float64
	NoJitter bool
}

func (p Policy) attempts() int {
	if p.Attempts <= 0 {
		return 3
	}
	return p.Attempts
}

// Delay returns the wait before attempt number attempt+2 (attempt counts from
// zero for the first failure). jitter is a number in [0,1).
func (p Policy) Delay(attempt int, jitter float64) time.Duration {
	initial, max, factor := p.Initial, p.Max, p.Factor
	if initial <= 0 {
		initial = 100 * time.Millisecond
	}
	if max <= 0 {
		max = 5 * time.Second
	}
	if factor <= 0 {
		factor = 2
	}
	d := float64(initial) * math.Pow(factor, float64(attempt))
	if d > float64(max) || math.IsInf(d, 0) {
		d = float64(max)
	}
	if !p.NoJitter {
		d *= 0.5 + jitter/2
	}
	return time.Duration(d)
}

// Do calls fn until it succeeds, fails with an error retryable rejects, runs
// out of attempts or ctx ends. The last error of fn is returned, or the
// context's error when waiting was interrupted.
func Do(ctx context.Context, p Policy, retryable func(error) bool, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(); err == nil || !retryable(err) || attempt+1 >= p.attempts() {
			return err
		}
		timer := time.NewTimer(p.Delay(attempt, rand.Float64()))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return err
		}
	}
}
