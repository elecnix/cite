package githubclient

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// The compare API's status says how the new head relates to the old one:
// ahead and identical mean the old head is an ancestor of the new one,
// diverged and behind mean a force push or a reset replaced history.
func TestCompareStatus(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/hello/compare/old1...new2" {
			t.Errorf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"status":"diverged","merge_base_commit":{"sha":"mb"}}`)
	}))
	got, err := c.CompareStatus(context.Background(), "old1", "new2")
	if err != nil || got != "diverged" {
		t.Fatalf("status = %q err=%v", got, err)
	}
}

// A previous head that GitHub no longer has is an error, never a status: the
// caller must not read a missing commit as an ancestor.
func TestCompareStatusErrorsWhenTheOldHeadIsGone(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"No common ancestor"}`, http.StatusNotFound)
	}))
	if got, err := c.CompareStatus(context.Background(), "gone", "new2"); err == nil {
		t.Fatalf("status = %q, want an error", got)
	}
}
