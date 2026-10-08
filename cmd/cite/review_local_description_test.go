package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/model"
	"github.com/elecnix/cite/internal/publisher"
)

// TestReviewLocalPassesTheDescription: a local run given --description puts
// that text in the review call, quoted as the untrusted pull request
// description, so a replayed pull request is reviewed with the intent its
// author stated.
func TestReviewLocalPassesTheDescription(t *testing.T) {
	chdirTempWithPostImage(t)
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cleanReviewResponse))
	}))
	t.Cleanup(srv.Close)
	setFakeModelEnv(t, srv.URL)

	diffPath, cfgPath := writeDiffAndConfig(t, "")
	descPath := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(descPath, []byte("Rename the greeting so the banner fits.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var report bytes.Buffer
	if err := reviewLocal(diffPath, descPath, cfgPath, model.StructuredOutputResponseFormat, "", false, publisher.JSONReportSink(&report)); err != nil {
		t.Fatalf("reviewLocal: %v", err)
	}
	if len(bodies) == 0 {
		t.Fatal("the model was never called")
	}
	for _, b := range bodies {
		if strings.Contains(b, "| Rename the greeting so the banner fits.") {
			return
		}
	}
	t.Fatalf("no review call carried the description as a quoted line; first call: %.400s", bodies[0])
}
