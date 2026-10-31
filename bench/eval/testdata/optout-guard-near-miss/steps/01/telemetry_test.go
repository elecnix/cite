package telemetry

import "testing"

func TestEnabled(t *testing.T) {
	cases := map[string]bool{"": true, "1": true, "0": false, "false": false, "off": false}
	for v, want := range cases {
		t.Setenv("TELEMETRY", v)
		if got := Enabled(); got != want {
			t.Errorf("TELEMETRY=%q: got %v, want %v", v, got, want)
		}
	}
}
