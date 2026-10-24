package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// startCountingProvider is startFakeProvider that also counts requests, so a
// test can prove a run reached the model once however many reports it wrote.
func startCountingProvider(t *testing.T, calls *int32, response string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestReviewWritesEveryRequestedReportFromOneRun covers issue #215 end to
// end: one --report per format writes both files of one review, and asking for
// a second format does not cost a second model pass. One pass already runs the
// reviewer and its verifier, so the check is that two reports cost the same as
// one rather than a fixed number.
func TestReviewWritesEveryRequestedReportFromOneRun(t *testing.T) {
	runWith := func(t *testing.T, reports ...[]string) (calls int32, files map[string]string) {
		t.Helper()
		chdirTempWithFile(t, "x.txt", "A `tool` launch\nhello world\n")

		srv := startCountingProvider(t, &calls, cleanReviewResponse)
		setFakeModelEnv(t, srv.URL)

		diffPath := filepath.Join(t.TempDir(), "diff.txt")
		if err := os.WriteFile(diffPath, []byte(localPlainDiff), 0o644); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		args := []string{"--diff", diffPath, "--config", filepath.Join(dir, "absent.yml")}
		names := []string{"review.md", "review.json"}
		for i, r := range reports {
			args = append(args, "--report", r[0], "--out", filepath.Join(dir, names[i]))
		}
		if err := runReview(args); err != nil {
			t.Fatalf("runReview(%v): %v", reports, err)
		}
		files = map[string]string{}
		for _, n := range names[:len(reports)] {
			b, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				t.Fatalf("report %s: %v", n, err)
			}
			files[n] = string(b)
		}
		return atomic.LoadInt32(&calls), files
	}

	one, _ := runWith(t, []string{"json"})
	two, files := runWith(t, []string{"markdown"}, []string{"json"})
	if one != two {
		t.Fatalf("one report cost %d model calls, two cost %d; a second format must reuse the one run", one, two)
	}
	if !strings.Contains(files["review.md"], "Cite review") {
		t.Fatalf("markdown report is missing its header:\n%s", files["review.md"])
	}
	var doc struct {
		Run struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"run"`
	}
	if err := json.Unmarshal([]byte(files["review.json"]), &doc); err != nil {
		t.Fatalf("json report is not JSON: %v\n%s", err, files["review.json"])
	}
	if len(doc.Run.Files) != 1 || doc.Run.Files[0].Path != "x.txt" {
		t.Fatalf("json report files = %+v, want only x.txt", doc.Run.Files)
	}
}

// TestReviewReportFormatCarriesItsOwnPath covers the FORMAT=PATH spelling the
// issue also allows, so one flag can name both.
func TestReviewReportFormatCarriesItsOwnPath(t *testing.T) {
	chdirTempWithFile(t, "x.txt", "A `tool` launch\nhello world\n")

	var calls int32
	srv := startCountingProvider(t, &calls, cleanReviewResponse)
	setFakeModelEnv(t, srv.URL)

	diffPath := filepath.Join(t.TempDir(), "diff.txt")
	if err := os.WriteFile(diffPath, []byte(localPlainDiff), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "review.md")
	jsonPath := filepath.Join(dir, "review.json")

	err := runReview([]string{"--diff", diffPath,
		"--config", filepath.Join(dir, "absent.yml"),
		"--report", "markdown=" + mdPath,
		"--report", "json=" + jsonPath,
	})
	if err != nil {
		t.Fatalf("runReview: %v", err)
	}
	for _, p := range []string{mdPath, jsonPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("report %s was not written: %v", p, err)
		}
	}
}

// TestReviewReportFlagRejectsConflictingPaths covers the ways a set of
// report flags can name no unambiguous destination.
func TestReviewReportFlagRejectsConflictingPaths(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "two reports to stdout",
			args: []string{"--report", "json", "--report", "markdown"},
			want: "stdout",
		},
		{
			name: "same file twice",
			args: []string{"--report", "json=" + filepath.Join(dir, "a"), "--report", "markdown=" + filepath.Join(dir, "a")},
			want: "same file",
		},
		{
			name: "out without a pending report",
			args: []string{"--report", "json=" + filepath.Join(dir, "b"), "--out", filepath.Join(dir, "c")},
			want: "--out",
		},
		{
			name: "unknown format",
			args: []string{"--report", "yaml"},
			want: "json or markdown",
		},
		{
			name: "empty path after equals",
			args: []string{"--report", "json="},
			want: "empty report path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--diff", filepath.Join(dir, "absent.diff")}, tc.args...)
			err := runReview(args)
			if err == nil {
				t.Fatal("runReview accepted the flags, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestReviewOutStillRequiresReport keeps the documented pairing check: --out
// on its own is a usage error, not a silent stdout report.
func TestReviewOutStillRequiresReport(t *testing.T) {
	err := runReview([]string{"--diff", filepath.Join(t.TempDir(), "absent.diff"), "--out", "x"})
	if err == nil || !strings.Contains(err.Error(), "--report") {
		t.Fatalf("error = %v, want a --report requirement", err)
	}
}

// TestReviewNoReportFlagIsNotAReportMode guards the flag parsing: with no
// --report, review must fall through to the publishing path rather than
// building an empty sink. The diff path is missing on purpose, so the run
// fails at the manifest read, past flag validation.
func TestReviewNoReportFlagIsNotAReportMode(t *testing.T) {
	err := runReview([]string{"--diff", filepath.Join(t.TempDir(), "absent.diff")})
	if err == nil {
		t.Fatal("runReview read a missing diff without failing")
	}
	if strings.Contains(err.Error(), "report") {
		t.Fatalf("error = %q, want a diff read failure, not a report flag error", err)
	}
}
