package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConformanceDate(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    time.Time
		ok      bool
	}{
		{
			name: "actual repo header",
			content: strings.Join([]string{
				"# Conformance",
				"",
				"**compat_profile: `2026-08`**",
				"**Profile date: 2026-08-21**",
				"",
			}, "\n"),
			want: time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC),
			ok:   true,
		},
		{
			name:    "dated prose line",
			content: "# Conformance\n\nThis profile is dated 2026-08-21 and is a snapshot.\n",
			want:    time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC),
			ok:      true,
		},
		{
			name:    "dated-profile alias form",
			content: "Dated profile: compat 2026-08 observed 2025-01-02.\n",
			want:    time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC),
			ok:      true,
		},
		{
			name:    "date beyond the scan window is ignored",
			content: strings.Repeat("no dates here\n", 25) + "observed 2026-08-21\n",
			ok:      false,
		},
		{
			name:    "undated",
			content: "# Conformance\n\nNo date on this snapshot.\n",
			ok:      false,
		},
		{
			name:    "malformed near-dates are not dates",
			content: "**Profile date: 2026-13-45**\nnot-a-date 20260821 2026/08/21\n",
			ok:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ConformanceDate([]byte(tt.content))
			if ok != tt.ok {
				t.Fatalf("ConformanceDate ok = %v, want %v", ok, tt.ok)
			}
			if ok && !got.Equal(tt.want) {
				t.Errorf("ConformanceDate = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStalenessWarning(t *testing.T) {
	date := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	at := func(days int) time.Time {
		return date.Add(time.Duration(days) * 24 * time.Hour)
	}
	tests := []struct {
		name     string
		now      time.Time
		warnDays int // 0 means no warning expected
	}{
		{name: "same day", now: at(0)},
		{name: "fresh", now: at(10)},
		{name: "exactly 90 days is within the window", now: at(90)},
		{name: "91 days is stale", now: at(91), warnDays: 91},
		{name: "a quarter past", now: at(120), warnDays: 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stalenessWarning(date, tt.now)
			if tt.warnDays == 0 {
				if got != "" {
					t.Errorf("stalenessWarning = %q, want empty", got)
				}
				return
			}
			want := "WARNING: CONFORMANCE.md is " + strconv.Itoa(tt.warnDays) + " days old (>90). Conformance observations expire; re-run the quarterly hand-check."
			if got != want {
				t.Errorf("stalenessWarning =\n%q\nwant\n%q", got, want)
			}
			if !strings.Contains(got, "WARNING") || !strings.Contains(got, ">90") {
				t.Errorf("warning %q lacks required wording", got)
			}
		})
	}
}
