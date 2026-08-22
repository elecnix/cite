package githubclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// readBody drains and returns a request body as a string.
func readBody(r *http.Request) string {
	raw, _ := ioReadAll(r.Body)
	return string(raw)
}

func ioReadAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return out, nil
		}
	}
}

// decodeBody parses a recorded JSON request body.
func decodeBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decoding request body %q: %v", raw, err)
	}
	return m
}

func TestCreateCheckRunTargetsHeadSHA(t *testing.T) {
	var path, body string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body = readBody(r)
		fmt.Fprint(w, `{"id": 901234}`)
	}))
	id, err := c.CreateCheckRun(context.Background(), "headsha123", "cite review",
		"Cite is reviewing", "queued on the pull request head", CheckStatusQueued)
	if err != nil {
		t.Fatal(err)
	}
	if id != 901234 {
		t.Fatalf("id = %d", id)
	}
	if path != "/repos/octo/hello/check-runs" {
		t.Fatalf("path = %q", path)
	}
	m := decodeBody(t, body)
	if m["head_sha"] != "headsha123" {
		// §11: never github.sha — the caller's PR head SHA, verbatim.
		t.Errorf("head_sha = %v", m["head_sha"])
	}
	if m["status"] != "queued" || m["name"] != "cite review" {
		t.Errorf("payload = %v", m)
	}
	out := m["output"].(map[string]any)
	if out["title"] != "Cite is reviewing" || out["summary"] == "" {
		t.Errorf("output = %v", out)
	}
}

func TestConcludeCheckRun(t *testing.T) {
	var path, body, method string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		body = readBody(r)
		w.WriteHeader(200)
	}))
	err := c.ConcludeCheckRun(context.Background(), 42, "failure", "FOUND", "1 finding blocks")
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPatch || path != "/repos/octo/hello/check-runs/42" {
		t.Fatalf("%s %s", method, path)
	}
	m := decodeBody(t, body)
	if m["status"] != "completed" || m["conclusion"] != "failure" {
		t.Errorf("payload = %v", m)
	}
}

func TestCreateReviewEventIsComment(t *testing.T) {
	var body string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = readBody(r)
		w.WriteHeader(200)
		fmt.Fprint(w, `{"id": 5}`)
	}))
	err := c.CreateReview(context.Background(), 7, "Two findings.", []ReviewComment{
		{Path: "a.go", StartLine: 3, Line: 5, Side: "RIGHT", Body: "off-by-one"},
		{Path: "b.go", Line: 2, Body: "unused var"},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := decodeBody(t, body)
	// Hard requirement §10: blocking lives in the check run or nowhere.
	if m["event"] != "COMMENT" {
		t.Errorf("event = %v, want COMMENT (never REQUEST_CHANGES)", m["event"])
	}
	if m["body"] != "Two findings." {
		t.Errorf("body = %v", m["body"])
	}
	comments := m["comments"].([]any)
	first := comments[0].(map[string]any)
	if first["path"] != "a.go" || first["line"] != float64(5) ||
		first["start_line"] != float64(3) || first["side"] != "RIGHT" {
		t.Errorf("comment[0] = %v", first)
	}
	second := comments[1].(map[string]any)
	if _, has := second["start_line"]; has {
		t.Errorf("single-line comment must omit start_line: %v", second)
	}
}

// failing422Handler rejects reviews carrying more than maxComments inline
// comments with a 422, like GitHub does for one unanchorable line.
func failing422Handler(t *testing.T, maxComments int, seen *[]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := readBody(r)
		*seen = append(*seen, raw)
		m := decodeBody(t, raw)
		comments, _ := m["comments"].([]any)
		if len(comments) > maxComments {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"message":"Validation Failed","errors":["end_line 99 is not in the diff"]}`)
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, `{"id": 6}`)
	}
}

func TestCreateReviewBisectsOn422(t *testing.T) {
	var seen []string
	c := newTestClient(t, failing422Handler(t, 1, &seen))
	comments := []ReviewComment{
		{Path: "a.go", Line: 1, Body: "finding one"},
		{Path: "b.go", Line: 2, Body: "finding two"},
		{Path: "c.go", Line: 3, Body: "finding three"},
		{Path: "d.go", Line: 99, Body: "finding four"}, // the unanchorable one
	}
	if err := c.CreateReview(context.Background(), 7, "Review body.", comments); err != nil {
		t.Fatal(err)
	}
	// Bisect halving: 4 -> 2 -> 1 attempts, then success with one comment.
	var last map[string]any
	for _, raw := range seen {
		last = decodeBody(t, raw)
	}
	got, _ := last["comments"].([]any)
	if len(got) != 1 {
		t.Fatalf("final review carries %d comments, want 1", len(got))
	}
	finalBody, _ := last["body"].(string)
	if !strings.Contains(finalBody, "Not anchored to a diff line") {
		t.Errorf("body lacks demoted section: %q", finalBody)
	}
	// Nothing silently dropped: all four finding bodies appear somewhere —
	// three demoted into the body, one posted inline.
	for _, want := range []string{"finding one", "finding two", "finding three", "finding four"} {
		inBody := strings.Contains(finalBody, want)
		inComments := false
		for _, cm := range got {
			if strings.Contains(cm.(map[string]any)["body"].(string), want) {
				inComments = true
			}
		}
		if !inBody && !inComments {
			t.Errorf("finding %q lost during bisect", want)
		}
	}
	if len(seen) != 3 { // 4 comments, 2 comments, 1 comment
		t.Errorf("attempts = %d, want 3", len(seen))
	}
}

func TestCreateReviewAllDemotedFallsBackToBodyOnly(t *testing.T) {
	var seen []string
	c := newTestClient(t, failing422Handler(t, 0, &seen)) // every anchor rejected
	comments := []ReviewComment{
		{Path: "a.go", Line: 1, Body: "only finding"},
	}
	if err := c.CreateReview(context.Background(), 7, "Body.", comments); err != nil {
		t.Fatal(err)
	}
	var last map[string]any
	for _, raw := range seen {
		last = decodeBody(t, raw)
	}
	if _, has := last["comments"]; has {
		t.Errorf("body-only review must omit comments: %v", last)
	}
	b := last["body"].(string)
	if !strings.Contains(b, "only finding") || !strings.Contains(b, demotedHeading) {
		t.Errorf("body = %q", b)
	}
}

func TestCreateReviewBodyOnlyFailureReturnsError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"Validation Failed"}`)
	}))
	err := c.CreateReview(context.Background(), 7, "Body.", nil)
	if err == nil {
		t.Fatal("body-only failure must surface, never vanish")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.StatusCode != 422 {
		t.Fatalf("err = %#v", err)
	}
}

func TestListReviewThreadsQueryShape(t *testing.T) {
	var query, vars string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		raw := readBody(r)
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		query, vars = payload.Query, string(raw)
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
			{"id":"T1","isOutdated":true,"isResolved":false,"comments":{"nodes":[
				{"id":"c1","databaseId":11,"body":"old finding","path":"a.go","line":5,"startLine":3,"author":{"login":"cite-bot"}},
				{"id":"c2","databaseId":12,"body":"file-level","path":"a.go","line":null,"startLine":null,"author":null}
			]}},
			{"id":"T2","isOutdated":false,"isResolved":true,"comments":{"nodes":[]}}
		]}}}}}`)
	}))
	threads, err := c.ListReviewThreads(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"reviewThreads(first:100)", "isOutdated", "isResolved", "databaseId", "startLine", "author{ login }"} {
		if !strings.Contains(query, want) {
			t.Errorf("query missing %q:\n%s", want, query)
		}
	}
	if !strings.Contains(vars, `"number":7`) {
		t.Errorf("variables missing pr number: %s", vars)
	}
	if len(threads) != 2 {
		t.Fatalf("threads = %+v", threads)
	}
	th := threads[0]
	if th.ID != "T1" || !th.IsOutdated || th.IsResolved {
		t.Errorf("thread = %+v", th)
	}
	if len(th.Comments) != 2 {
		t.Fatalf("comments = %+v", th.Comments)
	}
	cm := th.Comments[0]
	if cm.ID != 11 || cm.Body != "old finding" || cm.Path != "a.go" ||
		cm.Line != 5 || cm.StartLine != 3 || cm.AuthorLogin != "cite-bot" {
		t.Errorf("comment = %+v", cm)
	}
	// null line/startLine decode to 0, not an error.
	if th.Comments[1].Line != 0 || th.Comments[1].StartLine != 0 || th.Comments[1].AuthorLogin != "" {
		t.Errorf("null handling: %+v", th.Comments[1])
	}
}

func TestGraphQLMutationShapes(t *testing.T) {
	var bodies []string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, readBody(r))
		fmt.Fprint(w, `{"data":{}}`)
	}))
	if err := c.ResolveReviewThread(context.Background(), "THREAD_1"); err != nil {
		t.Fatal(err)
	}
	if err := c.MinimizeComment(context.Background(), 4242); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d", len(bodies))
	}
	var resolve, minimize struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	json.Unmarshal([]byte(bodies[0]), &resolve)
	json.Unmarshal([]byte(bodies[1]), &minimize)
	if !strings.Contains(resolve.Query, "resolveReviewThread(input:{threadId:$threadId})") {
		t.Errorf("resolve query = %s", resolve.Query)
	}
	if resolve.Variables["threadId"] != "THREAD_1" {
		t.Errorf("resolve vars = %v", resolve.Variables)
	}
	if !strings.Contains(minimize.Query, "minimizeComment") ||
		!strings.Contains(minimize.Query, "classifier:OUTDATED") {
		// OUTDATED is the only classifier Cite uses: stale is not abuse.
		t.Errorf("minimize query = %s", minimize.Query)
	}
	if minimize.Variables["subjectId"] != "4242" {
		t.Errorf("minimize vars = %v", minimize.Variables)
	}
}

func TestGraphQLErrorsSurfaceTyped(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"errors":[{"message":"Could not resolve to a node"}]}`)
	}))
	err := c.ResolveReviewThread(context.Background(), "NOPE")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want APIError, got %#v", err)
	}
	if !strings.Contains(apiErr.Message, "Could not resolve to a node") {
		t.Errorf("message = %q", apiErr.Message)
	}
}

func TestUpsertIssueCommentCreatesAndUpdates(t *testing.T) {
	existing := false
	var lastPath, lastMethod, lastBody string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath, lastMethod = r.URL.Path, r.Method
		lastBody = readBody(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/hello/issues/7/comments":
			if existing {
				fmt.Fprint(w, `[{"id":555,"body":"stale <!-- cite-ledger-v1 --> blob","user":{"login":"cite-bot","type":"Bot"}}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/hello/issues/7/comments":
			fmt.Fprint(w, `{"id":556}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/octo/hello/issues/comments/555":
			fmt.Fprint(w, `{"id":555}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	const marker = "<!-- cite-ledger-v1 -->"
	// No comment yet: POST.
	if err := c.UpsertIssueComment(context.Background(), 7, marker, marker+" ledger-a"); err != nil {
		t.Fatal(err)
	}
	if lastMethod != http.MethodPost || !strings.Contains(lastBody, "ledger-a") {
		t.Fatalf("create: %s %s", lastMethod, lastBody)
	}
	// Comment exists with marker: PATCH the same comment.
	existing = true
	if err := c.UpsertIssueComment(context.Background(), 7, marker, marker+" ledger-b"); err != nil {
		t.Fatal(err)
	}
	if lastMethod != http.MethodPatch || lastPath != "/repos/octo/hello/issues/comments/555" {
		t.Fatalf("update: %s %s", lastMethod, lastPath)
	}
	if !strings.Contains(lastBody, "ledger-b") {
		t.Errorf("patched body = %s", lastBody)
	}
}

func TestListPRFilesEmpty(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	files, err := c.ListPRFiles(context.Background(), 1)
	if err != nil || len(files) != 0 {
		t.Fatalf("files = %+v err = %v", files, err)
	}
}
