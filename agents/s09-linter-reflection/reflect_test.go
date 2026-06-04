package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider scripts a fixed sequence of assistant replies and records every
// request it received, so a test can both drive the loop and assert what the
// model was shown. It implements the Provider interface — no network. This is
// the standard test double the whole curriculum uses.
type fakeProvider struct {
	replies []string  // one per call, in order
	calls   int       // how many CreateMessage calls happened
	got     []Message // the messages from the LAST call (for assertions)
}

func (f *fakeProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	f.got = req.Messages
	reply := ""
	if f.calls < len(f.replies) {
		reply = f.replies[f.calls]
	}
	f.calls++
	return &CreateMessageResponse{
		Role:    "assistant",
		Content: []ContentBlock{{Type: "text", Text: reply}},
	}, nil
}

// scriptedRunner returns a Linter runner that yields a different (output, exit)
// on each successive call, so tests can model "dirty then clean". Once the script
// is exhausted it keeps returning the final entry.
type lintStep struct {
	out  string
	exit int
}

func scriptedRunner(steps []lintStep) func(string, []string, string) (string, int) {
	i := 0
	return func(name string, args []string, dir string) (string, int) {
		step := steps[len(steps)-1]
		if i < len(steps) {
			step = steps[i]
		}
		i++
		return step.out, step.exit
	}
}

// wholeFileReply wraps content in the whole-file format the demo Coder parses:
// a fenced block (the filename line is optional for our last-block parser).
func wholeFileReply(content string) string {
	return "Here is the file:\n```go\n" + content + "\n```\n"
}

// newTestLoop builds a ReflectLoop over a real WholeFileCoder writing to a temp
// file, a fakeProvider with the given replies, and a Linter with a scripted
// runner. Returns the loop and the provider so tests can inspect calls.
func newTestLoop(t *testing.T, replies []string, lintSteps []lintStep) (*ReflectLoop, *fakeProvider) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")

	prov := &fakeProvider{replies: replies}
	coder := NewWholeFileCoder(path)
	linter := NewLinter("")
	linter.runner = scriptedRunner(lintSteps)

	fence := [2]string{"```", "```"}
	send := func(ctx context.Context, messages []Message) (string, error) {
		req := CreateMessageRequest{System: coder.SystemPrompt(fence), Messages: messages}
		resp, err := prov.CreateMessage(ctx, req)
		if err != nil {
			return "", err
		}
		return replyText(resp.Content), nil
	}
	return NewReflectLoop(coder, linter, send), prov
}

// Test (1): clean on the first try -> zero reflections, one provider call.
func TestReflectCleanFirstTry(t *testing.T) {
	loop, prov := newTestLoop(t,
		[]string{wholeFileReply("package main\n\nfunc main() {}")},
		[]lintStep{{"", 0}}, // lint clean immediately
	)

	res, err := loop.Run(context.Background(), "make a main func")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reflections != 0 {
		t.Fatalf("expected 0 reflections on a clean first try, got %d", res.Reflections)
	}
	if !res.Clean || res.HitCap {
		t.Fatalf("expected Clean && !HitCap, got Clean=%v HitCap=%v", res.Clean, res.HitCap)
	}
	if prov.calls != 1 {
		t.Fatalf("expected exactly 1 provider call, got %d", prov.calls)
	}
}

// Test (2): one lint error, then fixed -> exactly one reflection, two calls, and
// the final state is clean. This is the core self-correction path.
func TestReflectOneErrorThenFixed(t *testing.T) {
	loop, prov := newTestLoop(t,
		[]string{
			wholeFileReply("package main\n\nfunc main() {"),  // broken (1st reply)
			wholeFileReply("package main\n\nfunc main() {}"), // fixed (2nd reply)
		},
		[]lintStep{
			{"main.go:3:13: expected '}', found EOF", 1}, // 1st lint: fail
			{"", 0}, // 2nd lint: clean
		},
	)

	res, err := loop.Run(context.Background(), "make a main func")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reflections != 1 {
		t.Fatalf("expected exactly 1 reflection, got %d", res.Reflections)
	}
	if !res.Clean || res.HitCap {
		t.Fatalf("expected converged-clean, got Clean=%v HitCap=%v", res.Clean, res.HitCap)
	}
	if prov.calls != 2 {
		t.Fatalf("expected 2 provider calls (initial + 1 reflection), got %d", prov.calls)
	}
}

// Test (3): the model never fixes it -> the loop stops at the cap rather than
// looping forever, reporting HitCap with the unresolved lint text.
func TestReflectCapReachedStops(t *testing.T) {
	brokenReplies := []string{
		wholeFileReply("package main\n\nfunc main() {"),
		wholeFileReply("package main\n\nfunc main() {"),
		wholeFileReply("package main\n\nfunc main() {"),
		wholeFileReply("package main\n\nfunc main() {"),
		wholeFileReply("package main\n\nfunc main() {"),
	}
	alwaysFail := []lintStep{{"main.go:3:13: still broken", 1}}

	loop, prov := newTestLoop(t, brokenReplies, alwaysFail)
	loop.MaxReflections = 2 // small cap to keep the test tight

	res, err := loop.Run(context.Background(), "make a main func")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.HitCap {
		t.Fatalf("expected HitCap==true when the model never converges")
	}
	if res.Clean {
		t.Fatalf("expected Clean==false at the cap")
	}
	if res.Reflections != 2 {
		t.Fatalf("expected Reflections==MaxReflections==2, got %d", res.Reflections)
	}
	// initial call + 2 reflections == 3 calls, then we stop (don't call a 4th time)
	if prov.calls != 3 {
		t.Fatalf("expected 3 provider calls (1 + 2 reflections), got %d", prov.calls)
	}
	if !strings.Contains(res.LintText, "still broken") {
		t.Fatalf("HitCap result should carry the last lint text, got %q", res.LintText)
	}
}

// Test (4): the lint error text is forwarded to the provider as the next user
// message. We assert the SECOND call's message list contains the lint report —
// this is the mechanism that lets the model see what to fix.
func TestReflectLintTextForwardedToProvider(t *testing.T) {
	loop, prov := newTestLoop(t,
		[]string{
			wholeFileReply("package main\n\nfunc main() {"),  // broken
			wholeFileReply("package main\n\nfunc main() {}"), // fixed
		},
		[]lintStep{
			{"main.go:3:13: UNIQUE_LINT_MARKER expected '}'", 1},
			{"", 0},
		},
	)

	if _, err := loop.Run(context.Background(), "make a main func"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// prov.got holds the messages from the LAST (2nd) call. It must include the
	// reflected lint message as a user turn.
	var found bool
	for _, m := range prov.got {
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if strings.Contains(b.Text, "UNIQUE_LINT_MARKER") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("lint error text was not forwarded to the provider as a user message; got %d messages", len(prov.got))
	}
	// And it must carry the upstream header so the model reads it as a fix request.
	var hasHeader bool
	for _, m := range prov.got {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "# Fix any errors below") {
				hasHeader = true
			}
		}
	}
	if !hasHeader {
		t.Fatalf("reflected message missing the '# Fix any errors below' header")
	}
}

// Test (5): the ok/clean path actually writes the fixed file to disk and stops.
// This guards that "converged" means the edit really landed, not just that the
// counter stopped.
func TestReflectAppliesEditsAndConverges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")

	prov := &fakeProvider{replies: []string{
		wholeFileReply("package main\n\nfunc main() { println(\"hi\") }"),
	}}
	coder := NewWholeFileCoder(path)
	linter := NewLinter("")
	linter.runner = scriptedRunner([]lintStep{{"", 0}})

	fence := [2]string{"```", "```"}
	send := func(ctx context.Context, messages []Message) (string, error) {
		req := CreateMessageRequest{System: coder.SystemPrompt(fence), Messages: messages}
		resp, err := prov.CreateMessage(ctx, req)
		if err != nil {
			return "", err
		}
		return replyText(resp.Content), nil
	}
	loop := NewReflectLoop(coder, linter, send)

	res, err := loop.Run(context.Background(), "print hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Clean || len(res.Applied) != 1 {
		t.Fatalf("expected one applied edit on the clean path, got Clean=%v applied=%d", res.Clean, len(res.Applied))
	}
	// Read back what landed on disk.
	wrote := mustRead(t, path)
	if !strings.Contains(wrote, "println(\"hi\")") {
		t.Fatalf("edit did not land on disk; file = %q", wrote)
	}
}

// Test: no fenced block in the reply -> no edits, nothing to lint, loop ends
// clean without spinning. Guards the "model returned prose only" edge case.
func TestReflectNoEditsEndsClean(t *testing.T) {
	loop, prov := newTestLoop(t,
		[]string{"I'm not sure how to do that."}, // no fence
		[]lintStep{{"should not run", 1}},
	)
	res, err := loop.Run(context.Background(), "do something")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Reflections != 0 || !res.Clean {
		t.Fatalf("no edits should end clean with 0 reflections, got Reflections=%d Clean=%v", res.Reflections, res.Clean)
	}
	if prov.calls != 1 {
		t.Fatalf("expected a single call, got %d", prov.calls)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
