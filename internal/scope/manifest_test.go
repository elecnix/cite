package scope

import (
	"reflect"
	"testing"
)

func TestParseNameStatusBasics(t *testing.T) {
	in := "A\t.github/workflows/deploy.yml\n" +
		"M\tinternal/webhook/handler.go\n" +
		"D\tinternal/legacy/shim.go\n"
	got := ParseNameStatus(in)
	want := []ManifestEntry{
		{Status: "A", Path: ".github/workflows/deploy.yml"},
		{Status: "M", Path: "internal/webhook/handler.go"},
		{Status: "D", Path: "internal/legacy/shim.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseNameStatus = %+v, want %+v", got, want)
	}
}

func TestParseNameStatusRenameBothSources(t *testing.T) {
	// Raw git output: tab-separated old and new.
	tab := ParseNameStatus("R090\tdocs/setup.md\tdocs/getting-started.md")
	if len(tab) != 1 {
		t.Fatalf("len = %d, want 1", len(tab))
	}
	e := tab[0]
	if e.Status != "R090" || e.OldPath != "docs/setup.md" || e.Path != "docs/getting-started.md" {
		t.Fatalf("tab form = %+v", e)
	}
	if e.ExistsAtHead() == false {
		t.Fatal("rename target must exist at head")
	}

	// Rendered form: single "old -> new" field, plus counts column.
	arrow := ParseNameStatus("R090 docs/setup.md -> docs/getting-started.md +3/-3")
	if len(arrow) != 1 {
		t.Fatalf("len = %d, want 1", len(arrow))
	}
	e = arrow[0]
	if e.Status != "R090" || e.OldPath != "docs/setup.md" || e.Path != "docs/getting-started.md" {
		t.Fatalf("arrow form = %+v", e)
	}
	if e.Adds != 3 || e.Dels != 3 {
		t.Fatalf("counts = +%d/-%d, want +3/-3", e.Adds, e.Dels)
	}
}

func TestParseNameStatusRenameSourceDoesNotExist(t *testing.T) {
	got := ParseNameStatus("R100\told/thing.go\tnew/thing.go")
	if got[0].OldPath != "old/thing.go" {
		t.Fatalf("OldPath = %q", got[0].OldPath)
	}
	// A path listed as a rename source no longer exists (§7).
	dead := ManifestEntry{Status: "R100", Path: "new/thing.go", OldPath: "old/thing.go"}
	if !dead.ExistsAtHead() {
		t.Fatal("rename target should exist")
	}
	del := ManifestEntry{Status: "D", Path: "x.go"}
	if del.ExistsAtHead() {
		t.Fatal("deleted path should not exist")
	}
}

func TestParseNameStatusCountsAndBlankLines(t *testing.T) {
	in := "\nM\tcmd/cite/main.go\t+18/-4\n\nC100\ta.txt\tb.txt\t+2/-2\n"
	got := ParseNameStatus(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Adds != 18 || got[0].Dels != 4 {
		t.Fatalf("counts = %+v", got[0])
	}
	if got[1].Status != "C100" || got[1].OldPath != "a.txt" || got[1].Path != "b.txt" || got[1].Adds != 2 || got[1].Dels != 2 {
		t.Fatalf("copy = %+v", got[1])
	}
}

func TestParseNameStatusIgnoresGarbage(t *testing.T) {
	got := ParseNameStatus("this is not a manifest line\n\nM\tok.go\n")
	if len(got) != 1 || got[0].Path != "ok.go" {
		t.Fatalf("got %+v", got)
	}
}

const ghFilesJSON = `[
  {"filename":"handler.go","status":"modified","additions":18,"deletions":4},
  {"filename":"deploy.yml","status":"added","additions":41,"deletions":0},
  {"filename":"shim.go","status":"removed","additions":0,"deletions":212},
  {"filename":"docs/getting-started.md","previous_filename":"docs/setup.md","status":"renamed","additions":3,"deletions":3}
]`

func TestParseGitHubFilesAPI(t *testing.T) {
	got, err := ParseGitHubFilesAPI([]byte(ghFilesJSON))
	if err != nil {
		t.Fatalf("ParseGitHubFilesAPI: %v", err)
	}
	want := []ManifestEntry{
		{Status: "M", Path: "handler.go", Adds: 18, Dels: 4},
		{Status: "A", Path: "deploy.yml", Adds: 41},
		{Status: "D", Path: "shim.go", Dels: 212},
		{Status: "R", Path: "docs/getting-started.md", OldPath: "docs/setup.md", Adds: 3, Dels: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got[3].ExistsAtHead() == false {
		t.Fatal("rename target exists")
	}
}

func TestParseGitHubFilesAPISubmoduleChanged(t *testing.T) {
	got, err := ParseGitHubFilesAPI([]byte(`[{"filename":"vendor/lib","status":"changed","additions":1,"deletions":1}]`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got[0].Status != "M" {
		t.Fatalf("status = %q, want M", got[0].Status)
	}
}

func TestParseGitHubFilesAPIErrors(t *testing.T) {
	if _, err := ParseGitHubFilesAPI([]byte(`not json`)); err == nil {
		t.Fatal("want error on invalid JSON")
	}
	if _, err := ParseGitHubFilesAPI([]byte(`[{"filename":"a.go","status":"teleported"}]`)); err == nil {
		t.Fatal("want error on unknown status")
	}
	if _, err := ParseGitHubFilesAPI([]byte(`[{"status":"modified"}]`)); err == nil {
		t.Fatal("want error on empty filename")
	}
}

func TestIsBinary(t *testing.T) {
	if IsBinary([]byte("plain text\nwith lines\n")) {
		t.Fatal("text flagged binary")
	}
	if !IsBinary([]byte("text\x00with nul")) {
		t.Fatal("NUL byte not detected")
	}
	// NUL beyond the sniff window is not detected — same as git.
	far := make([]byte, 9000)
	for i := range far {
		far[i] = 'x'
	}
	far[8100] = 0
	if IsBinary(far) {
		t.Fatal("NUL beyond sniff window flagged binary")
	}
	if IsBinary(nil) {
		t.Fatal("empty data flagged binary")
	}
}

func TestDefaultSkipReason(t *testing.T) {
	cases := []struct {
		path   string
		reason string
	}{
		{"api/pb/service.pb.go", SkipReasonGenerated},
		{"internal/mock_gen.go", SkipReasonGenerated},
		{"ui/chart.gen.go", SkipReasonGenerated},
		{"web/__snapshots__/app.snap", SkipReasonGenerated},
		{"package-lock.json", SkipReasonLockfile},
		{"web/yarn.lock", SkipReasonLockfile},
		{"go.sum", SkipReasonLockfile},
		{"rust/Cargo.lock", SkipReasonLockfile},
		{"python/poetry.lock", SkipReasonLockfile},
		{"ruby/Gemfile.lock", SkipReasonLockfile},
		{"php/composer.lock", SkipReasonLockfile},
		{"vendor/golang.org/x/x.go", SkipReasonVendored},
		{"third_party/lib/impl.cc", SkipReasonVendored},
		{"node_modules/left-pad/index.js", SkipReasonVendored},
		{"app/app.min.js", SkipReasonMinified},
		{"app/app.min.css", SkipReasonMinified},
		{"internal/handler.go", ""},
		{"README.md", ""},
		{"docs/vendor-list.md", ""}, // filename mention is not a vendored tree
	}
	for _, c := range cases {
		if got := DefaultSkipReason(c.path); got != c.reason {
			t.Errorf("DefaultSkipReason(%q) = %q, want %q", c.path, got, c.reason)
		}
	}
}

func TestSkipReasonBinaryAndIgnore(t *testing.T) {
	reason, ok := SkipReason("data/blob.bin", []byte{0x89, 0x00, 0x01}, nil)
	if !ok || reason != SkipReasonBinary {
		t.Fatalf("got %q %v", reason, ok)
	}
	reason, ok = SkipReason("internal/thing.go", []byte("package main\n"), []string{"internal/legacy/**"})
	if ok {
		t.Fatalf("unexpected skip %q", reason)
	}
	reason, ok = SkipReason("internal/legacy/thing.go", []byte("package main\n"), []string{"internal/legacy/**"})
	if !ok || reason != SkipReasonIgnored {
		t.Fatalf("paths_ignore skip: got %q %v", reason, ok)
	}
	// paths_ignore adds to the list, never subtracts: a default skip stands
	// even when a narrower ignore exists.
	reason, ok = SkipReason("go.sum", []byte("module x\n"), []string{"!*.lock"})
	if !ok || reason != SkipReasonLockfile {
		t.Fatalf("default skip must stand: got %q %v", reason, ok)
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**/*.gen.go", "a/b/c.gen.go", true},
		{"**/*.gen.go", "c.gen.go", true},
		{"**/*.gen.go", "a/b/c.go", false},
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/x/a.md", false},
		{"docs/**/*.md", "docs/x/a.md", true},
		{"docs/**/*.md", "docs/a.md", true},
		{"**", "anything/at/all.go", true},
		{"internal/**", "internal/a/b.go", true},
		{"internal/**", "internalX/a.go", false},
		{"*.go", "a.go", true},
		{"*.go", "dir/a.go", false}, // '*' never crosses '/'
		{"a?.go", "ab.go", true},
		{"a?.go", "a.go", false},
		{"[abc].go", "b.go", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}
