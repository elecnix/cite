package telemetry

import "os"

// Enabled reports whether usage reporting is on. Setting TELEMETRY to "0",
// "false" or "off" turns it off; any other value, or none, leaves it on.
func Enabled() bool {
	v := os.Getenv("TELEMETRY")
	return v != "0" && v != "false" && v != "off"
}

// Endpoint returns the collector address.
func Endpoint() string {
	return os.Getenv("TELEMETRY_ENDPOINT")
}
