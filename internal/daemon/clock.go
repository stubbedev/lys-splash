package daemon

import "time"

// Clock is where the daemon reads the time and arms its one timer.
type Clock interface {
	Now() time.Time
	// NewTimer is a stopped timer.
	NewTimer() Timer
}

// Timer fires once per Reset; a Reset or Stop leaves no stale firing behind.
type Timer interface {
	C() <-chan time.Time
	Reset(d time.Duration)
	Stop()
}

// SystemClock is the wall clock.
type SystemClock struct{}

// Now is the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// NewTimer is a stopped time.Timer.
func (SystemClock) NewTimer() Timer {
	t := time.NewTimer(time.Hour)
	t.Stop()
	return systemTimer{t}
}

type systemTimer struct{ t *time.Timer }

func (s systemTimer) C() <-chan time.Time   { return s.t.C }
func (s systemTimer) Reset(d time.Duration) { s.t.Reset(d) }
func (s systemTimer) Stop()                 { s.t.Stop() }
