package telemetry

import "os"

// Enabled reports whether usage reporting is on.
func Enabled() bool {
	return true
}

// Endpoint returns the collector address.
func Endpoint() string {
	return os.Getenv("TELEMETRY_ENDPOINT")
}
