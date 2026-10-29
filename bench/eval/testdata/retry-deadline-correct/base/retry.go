package retry

import "time"

// Policy schedules retries of a failed call.
type Policy struct {
	Interval time.Duration
	Max      int
}
