package retry

import "time"

// Policy schedules retries of a failed call.
type Policy struct {
	Interval time.Duration
	Max      int
}

// NextAttempt returns when retry number attempt (counting from 1) is due,
// measured from the time of the first failure.
func (p Policy) NextAttempt(failedAt time.Time, attempt int) time.Time {
	return failedAt.Add(time.Duration(attempt) * p.Interval)
}
