package reviewer

import (
	"strings"
	"testing"

	"github.com/elecnix/cite/internal/xref"
)

type stubRelated struct {
	got map[string]map[int]bool
}

func (s *stubRelated) Related(path string, post []string, added map[int]bool) []xref.Snippet {
	if s.got == nil {
		s.got = map[string]map[int]bool{}
	}
	s.got[path] = added
	if path != "a.go" {
		return nil
	}
	return []xref.Snippet{{
		Path: "lib/parse.go", Symbol: "ParseMode", Kind: xref.KindDefinition, StartLine: 10,
		Lines: []string{"func ParseMode(s string) Mode {", `	if s == "" {`, "		return Default", "	}"},
	}}
}

// The review call for a file carries the related excerpts the finder returns
// for that file, after the cache breakpoint, each line labelled with its own
// path and number so it can never pass for an anchor in the file under review.
func TestReviewCallCarriesRelatedCode(t *testing.T) {
	c := &fakeClient{fn: defaultScript()}
	in := baseInputs()
	finder := &stubRelated{}
	in.Related = finder
	if _, err := runOnce(t, in, baseOptions(c)); err != nil {
		t.Fatal(err)
	}
	if len(finder.got["a.go"]) == 0 {
		t.Fatal("the finder was not asked about a.go's changed lines")
	}
	var review string
	for _, call := range c.calls {
		if !isTriageCall(call) && requestPath(call) == "a.go" {
			review = call.User
		}
	}
	parts := strings.SplitN(review, cacheBreakpoint, 2)
	if len(parts) != 2 {
		t.Fatal("no review call for a.go with a cache breakpoint")
	}
	if strings.Contains(parts[0], "related_code") {
		t.Error("related code leaked into segment B, which must be identical across files")
	}
	for _, want := range []string{
		`<related_code trust="untrusted">`,
		"-- definition of ParseMode in lib/parse.go",
		"lib/parse.go:10 |func ParseMode(s string) Mode {",
		`lib/parse.go:11 |	if s == "" {`,
		"</related_code>",
	} {
		if !strings.Contains(parts[1], want) {
			t.Errorf("payload missing %q:\n%s", want, parts[1])
		}
	}
}
