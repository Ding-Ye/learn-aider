package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider implements Provider without any network. It records the request
// it was handed and returns a canned reply. The loop programs against the
// Provider interface, so this struct literal stands in for a real LLM in CI.
type fakeProvider struct {
	lastReq CreateMessageRequest
	reply   string
}

func (f *fakeProvider) CreateMessage(_ context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	f.lastReq = req
	return &CreateMessageResponse{
		Role:       "assistant",
		StopReason: "end_turn",
		Content:    []ContentBlock{{Type: "text", Text: f.reply}},
		Usage:      Usage{InputTokens: 13, OutputTokens: 21},
	}, nil
}

// newTestCoder builds a coder rooted at dir, with terminal output discarded so
// the test log stays clean.
func newTestCoder(t *testing.T, p Provider, dir string) *Coder {
	t.Helper()
	c := NewCoder(p, dir, false)
	c.out, _ = os.Open(os.DevNull)
	c.errw, _ = os.Open(os.DevNull)
	return c
}

// (1) Parse a `--- a/x +++ b/x @@` block into a hunk with the right path and
// diff lines. The git a//b/ prefixes must be stripped to recover "greet.go".
func TestGetEdits_ParseHunk(t *testing.T) {
	reply := "Here is the change:\n\n" +
		"```diff\n" +
		"--- a/greet.go\n" +
		"+++ b/greet.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" func greet() string {\n" +
		"-\treturn \"hello\"\n" +
		"+\treturn \"hi\"\n" +
		" }\n" +
		"```\n"

	c := NewCoder(&fakeProvider{}, ".", false)
	hunks, err := c.GetEdits(reply)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(hunks) != 1 {
		t.Fatalf("got %d hunks, want 1", len(hunks))
	}
	h := hunks[0]
	if h.Path != "greet.go" {
		t.Errorf("path = %q, want greet.go", h.Path)
	}
	// The hunk lines must retain their leading ' '/'-'/'+' and exclude the
	// ---/+++/@@ headers.
	want := []string{
		" func greet() string {\n",
		"-\treturn \"hello\"\n",
		"+\treturn \"hi\"\n",
		" }\n",
	}
	if len(h.Lines) != len(want) {
		t.Fatalf("got %d hunk lines, want %d: %q", len(h.Lines), len(want), h.Lines)
	}
	for i := range want {
		if h.Lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, h.Lines[i], want[i])
		}
	}

	// And the hunk derives the right before/after texts.
	before, after := hunkToBeforeAfter(h)
	if before != "func greet() string {\n\treturn \"hello\"\n}\n" {
		t.Errorf("before = %q", before)
	}
	if after != "func greet() string {\n\treturn \"hi\"\n}\n" {
		t.Errorf("after = %q", after)
	}
}

// (2) A pure-context hunk (no -/+ lines) is dropped during parsing: it changes
// nothing, so process_fenced_block never flushes it.
func TestGetEdits_DropsPureContextHunk(t *testing.T) {
	reply := "```diff\n" +
		"--- a/x.go\n" +
		"+++ b/x.go\n" +
		"@@ -1,2 +1,2 @@\n" +
		" line one\n" +
		" line two\n" +
		"```\n"
	c := NewCoder(&fakeProvider{}, ".", false)
	hunks, err := c.GetEdits(reply)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(hunks) != 0 {
		t.Fatalf("got %d hunks, want 0 (pure-context hunk should be dropped)", len(hunks))
	}
}

// (3) Apply a hunk that ADDS a line: context anchors locate the spot, the `+`
// line is inserted, and the rest of the file is preserved verbatim.
func TestApplyEdits_AddLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.go")
	original := "items := []string{\n\t\"a\",\n\t\"c\",\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := newTestCoder(t, &fakeProvider{}, dir)
	// Insert "b" between "a" and "c". Context lines anchor the insertion point.
	h := Hunk{Path: "list.go", Lines: []string{
		" \t\"a\",\n",
		"+\t\"b\",\n",
		" \t\"c\",\n",
	}}
	applied, failed, err := c.ApplyEdits([]Hunk{h})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(applied) != 1 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0", len(applied), len(failed))
	}
	got, _ := os.ReadFile(path)
	want := "items := []string{\n\t\"a\",\n\t\"b\",\n\t\"c\",\n}\n"
	if string(got) != want {
		t.Fatalf("file mismatch:\n got %q\nwant %q", string(got), want)
	}
}

// (4) Apply a hunk that REMOVES a line: the `-` line is deleted, surrounding
// context preserved.
func TestApplyEdits_RemoveLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	original := "func main() {\n\tdebugPrint()\n\trun()\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := newTestCoder(t, &fakeProvider{}, dir)
	h := Hunk{Path: "main.go", Lines: []string{
		" func main() {\n",
		"-\tdebugPrint()\n",
		" \trun()\n",
	}}
	applied, failed, err := c.ApplyEdits([]Hunk{h})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(applied) != 1 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0", len(applied), len(failed))
	}
	got, _ := os.ReadFile(path)
	want := "func main() {\n\trun()\n}\n"
	if string(got) != want {
		t.Fatalf("file mismatch:\n got %q\nwant %q", string(got), want)
	}
}

// (5) Context-not-found: a hunk whose context lines don't exist in the file is a
// soft failure — reported in `failed`, the file left untouched, no crash. This
// is the guard against silent corruption.
func TestApplyEdits_ContextNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.txt")
	original := "line one\nline two\nline three\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := newTestCoder(t, &fakeProvider{}, dir)
	h := Hunk{Path: "keep.txt", Lines: []string{
		" this context is not in the file\n",
		"-neither is this\n",
		"+something new\n",
	}}
	applied, failed, err := c.ApplyEdits([]Hunk{h})
	if err != nil {
		t.Fatalf("ApplyEdits returned a hard error, want soft failure: %v", err)
	}
	if len(applied) != 0 || len(failed) != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", len(applied), len(failed))
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("file should be untouched on a failed match, got %q", string(got))
	}
}

// (6) Whitespace-flexible apply: the model quoted the hunk's context with NO
// leading indentation, but the file's lines are tab-indented. The exact match
// fails; the whitespace-tolerant tier (inherited from s03) recovers the file's
// real indent and applies the change anyway. This is doubly important for diffs,
// where context is quoted from memory.
func TestApplyEdits_WhitespaceFlexibleContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "indented.go")
	original := "func f() {\n\t\tx := 1\n\t\treturn x\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := newTestCoder(t, &fakeProvider{}, dir)
	// Hunk context + change has NO leading whitespace at all.
	h := Hunk{Path: "indented.go", Lines: []string{
		" x := 1\n",
		"-return x\n",
		"+return x * 2\n",
	}}
	applied, failed, err := c.ApplyEdits([]Hunk{h})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(applied) != 1 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0 (whitespace-flexible context should match)", len(applied), len(failed))
	}
	got, _ := os.ReadFile(path)
	// The file's two-tab indentation must be preserved on the replaced line.
	want := "func f() {\n\t\tx := 1\n\t\treturn x * 2\n}\n"
	if string(got) != want {
		t.Fatalf("indentation not preserved:\n got %q\nwant %q", string(got), want)
	}
}

// (7) Multiple hunks for one file are applied IN ORDER, each against the result
// of the previous one, so all changes accumulate.
func TestApplyEdits_MultipleHunksInOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	original := "alpha\nbeta\ngamma\ndelta\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := newTestCoder(t, &fakeProvider{}, dir)
	hunks := []Hunk{
		{Path: "doc.txt", Lines: []string{" alpha\n", "-beta\n", "+BETA\n"}},
		{Path: "doc.txt", Lines: []string{" gamma\n", "-delta\n", "+DELTA\n"}},
	}
	applied, failed, err := c.ApplyEdits(hunks)
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(applied) != 2 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 2/0", len(applied), len(failed))
	}
	got, _ := os.ReadFile(path)
	want := "alpha\nBETA\ngamma\nDELTA\n"
	if string(got) != want {
		t.Fatalf("file mismatch:\n got %q\nwant %q", string(got), want)
	}
}

// (8) A hunk with only `+` lines (no context, no removals) creates a new file,
// mirroring upstream do_replace's create/append path.
func TestApplyEdits_CreateFile(t *testing.T) {
	dir := t.TempDir()
	c := newTestCoder(t, &fakeProvider{}, dir)

	h := Hunk{Path: "created.txt", Lines: []string{"+hello\n", "+world\n"}}
	applied, failed, err := c.ApplyEdits([]Hunk{h})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(applied) != 1 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0", len(applied), len(failed))
	}
	got, _ := os.ReadFile(filepath.Join(dir, "created.txt"))
	if string(got) != "hello\nworld\n" {
		t.Fatalf("created file contents = %q", string(got))
	}
}

// (9) A hunk with no `--- / +++` header anywhere is a hard parse error — there
// is no file to apply it to.
func TestGetEdits_NoFilename(t *testing.T) {
	reply := "```diff\n" +
		"@@ -1,1 +1,1 @@\n" +
		"-old\n" +
		"+new\n" +
		"```\n"
	c := NewCoder(&fakeProvider{}, ".", false)
	if _, err := c.GetEdits(reply); err == nil {
		t.Fatal("expected an error for a hunk with no --- / +++ filename header")
	}
}

// (10) End-to-end: Coder.Run reads a temp file, the fake provider returns a
// ```diff reply, the hunk is parsed and applied to disk.
func TestCoderRun_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "greet.go")
	original := "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reply := "I'll update the greeting:\n\n" +
		"```diff\n" +
		"--- a/greet.go\n" +
		"+++ b/greet.go\n" +
		"@@ -3,3 +3,3 @@\n" +
		" func greet() string {\n" +
		"-\treturn \"hello\"\n" +
		"+\treturn \"hi\"\n" +
		" }\n" +
		"```\n"
	fp := &fakeProvider{reply: reply}

	c := newTestCoder(t, fp, dir)
	if err := c.Run(context.Background(), "greet.go", "rename the greeting to hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, _ := os.ReadFile(path)
	want := "package main\n\nfunc greet() string {\n\treturn \"hi\"\n}\n"
	if string(got) != want {
		t.Fatalf("file not edited:\n got %q\nwant %q", string(got), want)
	}

	// The request must have carried the original file + the instruction + a
	// system prompt that mentions the diff format.
	userText := fp.lastReq.Messages[0].Content[0].Text
	if !strings.Contains(userText, "hello") || !strings.Contains(userText, "rename the greeting to hi") {
		t.Error("request did not include the original file and instruction")
	}
	if !strings.Contains(fp.lastReq.System, "diff") {
		t.Error("system prompt does not define the unified-diff format")
	}
}
