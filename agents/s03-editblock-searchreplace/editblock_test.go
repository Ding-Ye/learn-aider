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

// (1) Parse a single SEARCH/REPLACE block into one Edit{Path,Search,Replace}.
func TestGetEdits_SingleBlock(t *testing.T) {
	reply := "Here is the change:\n\n" +
		"hello.go\n" +
		"<<<<<<< SEARCH\n" +
		"\tfmt.Println(\"hello\")\n" +
		"=======\n" +
		"\tfmt.Println(\"hi\")\n" +
		">>>>>>> REPLACE\n"

	c := NewCoder(&fakeProvider{}, ".", false)
	edits, err := c.GetEdits(reply)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("got %d edits, want 1", len(edits))
	}
	e := edits[0]
	if e.Path != "hello.go" {
		t.Errorf("path = %q, want hello.go", e.Path)
	}
	if e.Search != "\tfmt.Println(\"hello\")\n" {
		t.Errorf("search = %q", e.Search)
	}
	if e.Replace != "\tfmt.Println(\"hi\")\n" {
		t.Errorf("replace = %q", e.Replace)
	}
}

// (2) Parse multiple blocks — one with its own filename, a second that inherits
// the sticky current filename, and a third for a different file.
func TestGetEdits_MultipleBlocks(t *testing.T) {
	reply := "" +
		"a.go\n" +
		"<<<<<<< SEARCH\n" +
		"one\n" +
		"=======\n" +
		"ONE\n" +
		">>>>>>> REPLACE\n" +
		"\n" +
		"<<<<<<< SEARCH\n" + // no filename above -> inherits a.go
		"two\n" +
		"=======\n" +
		"TWO\n" +
		">>>>>>> REPLACE\n" +
		"\n" +
		"b.go\n" +
		"<<<<<<< SEARCH\n" +
		"three\n" +
		"=======\n" +
		"THREE\n" +
		">>>>>>> REPLACE\n"

	c := NewCoder(&fakeProvider{}, ".", false)
	edits, err := c.GetEdits(reply)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(edits) != 3 {
		t.Fatalf("got %d edits, want 3", len(edits))
	}
	if edits[0].Path != "a.go" || edits[1].Path != "a.go" || edits[2].Path != "b.go" {
		t.Fatalf("paths = %q,%q,%q; want a.go,a.go,b.go", edits[0].Path, edits[1].Path, edits[2].Path)
	}
	if edits[1].Replace != "TWO\n" {
		t.Errorf("second block replace = %q", edits[1].Replace)
	}
}

// (2b) A malformed block (SEARCH with no closing REPLACE) is a hard error, not a
// partial parse.
func TestGetEdits_Malformed(t *testing.T) {
	reply := "x.go\n<<<<<<< SEARCH\nfoo\n=======\nbar\n" // missing >>>>>>> REPLACE
	c := NewCoder(&fakeProvider{}, ".", false)
	if _, err := c.GetEdits(reply); err == nil {
		t.Fatal("expected an error for a block with no closing REPLACE marker")
	}

	// A SEARCH with no filename anywhere is also an error.
	noName := "<<<<<<< SEARCH\nfoo\n=======\nbar\n>>>>>>> REPLACE\n"
	if _, err := c.GetEdits(noName); err == nil {
		t.Fatal("expected an error for a block with no filename")
	}
}

// (3) Exact-match apply: the SEARCH text is present byte-for-byte, so
// perfectReplace splices in the REPLACE text.
func TestApplyEdits_ExactMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.go")
	original := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := newTestCoder(t, &fakeProvider{}, dir)
	edit := Edit{
		Path:    "hello.go",
		Search:  "\tfmt.Println(\"hello\")\n",
		Replace: "\tfmt.Println(\"hi\")\n",
	}
	applied, failed, err := c.ApplyEdits([]Edit{edit})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(applied) != 1 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0", len(applied), len(failed))
	}
	got, _ := os.ReadFile(path)
	want := "package main\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
	if string(got) != want {
		t.Fatalf("file mismatch:\n got %q\nwant %q", string(got), want)
	}
}

// (4) Whitespace-flexible apply: the model reproduced the right line but dropped
// ALL leading indentation. The exact match fails; the whitespace-tolerant tier
// recovers the file's real indent and applies the edit anyway. This is the
// load-bearing fuzzy behavior.
func TestApplyEdits_WhitespaceFlexible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "indented.go")
	// The real file indents the body with a tab.
	original := "func f() {\n\t\tx := 1\n\t\treturn x\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := newTestCoder(t, &fakeProvider{}, dir)
	// The model's SEARCH/REPLACE has NO leading whitespace at all.
	edit := Edit{
		Path:    "indented.go",
		Search:  "x := 1\nreturn x\n",
		Replace: "x := 2\nreturn x\n",
	}
	applied, failed, err := c.ApplyEdits([]Edit{edit})
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(applied) != 1 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0 (whitespace-flexible match should succeed)", len(applied), len(failed))
	}
	got, _ := os.ReadFile(path)
	// The file's two-tab indentation must be preserved on the replaced lines.
	want := "func f() {\n\t\tx := 2\n\t\treturn x\n}\n"
	if string(got) != want {
		t.Fatalf("indentation not preserved:\n got %q\nwant %q", string(got), want)
	}
}

// (5) Unmatched SEARCH -> reported in `failed`, file left untouched, no crash.
func TestApplyEdits_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.txt")
	original := "line one\nline two\nline three\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := newTestCoder(t, &fakeProvider{}, dir)
	edit := Edit{
		Path:    "keep.txt",
		Search:  "this text is not in the file at all\n",
		Replace: "whatever\n",
	}
	applied, failed, err := c.ApplyEdits([]Edit{edit})
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

// (6) Empty SEARCH creates a brand-new file. Then a second empty-SEARCH edit to
// the same path appends, mirroring upstream do_replace's create-or-append path.
func TestApplyEdits_CreateFile(t *testing.T) {
	dir := t.TempDir()
	c := newTestCoder(t, &fakeProvider{}, dir)

	create := Edit{Path: "created.txt", Search: "", Replace: "hello\n"}
	applied, failed, err := c.ApplyEdits([]Edit{create})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(applied) != 1 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0", len(applied), len(failed))
	}
	got, _ := os.ReadFile(filepath.Join(dir, "created.txt"))
	if string(got) != "hello\n" {
		t.Fatalf("created file contents = %q", string(got))
	}

	// A second empty-SEARCH edit appends to the now-existing file.
	appendEdit := Edit{Path: "created.txt", Search: "", Replace: "world\n"}
	if _, _, err := c.ApplyEdits([]Edit{appendEdit}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "created.txt"))
	if string(got) != "hello\nworld\n" {
		t.Fatalf("after append = %q, want %q", string(got), "hello\nworld\n")
	}
}

// (7) End-to-end: Coder.Run reads a temp file, the fake provider returns a
// SEARCH/REPLACE reply, the edit is parsed and applied to disk.
func TestCoderRun_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "greet.go")
	original := "package main\n\nfunc greet() string {\n\treturn \"hello\"\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	reply := "I'll update the greeting:\n\n" +
		"greet.go\n" +
		"<<<<<<< SEARCH\n" +
		"\treturn \"hello\"\n" +
		"=======\n" +
		"\treturn \"hi\"\n" +
		">>>>>>> REPLACE\n"
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
	// system prompt that mentions the SEARCH marker.
	userText := fp.lastReq.Messages[0].Content[0].Text
	if !strings.Contains(userText, "hello") || !strings.Contains(userText, "rename the greeting to hi") {
		t.Error("request did not include the original file and instruction")
	}
	if !strings.Contains(fp.lastReq.System, "SEARCH") {
		t.Error("system prompt does not define the SEARCH/REPLACE format")
	}
}

// (8) The "drop a spurious leading blank line" tier: the model prefixed the
// SEARCH block with a blank line that isn't in the file. The matcher retries
// without it. (Upstream issue #25.)
func TestReplaceMostSimilarChunk_DropsSpuriousBlankLine(t *testing.T) {
	whole := "alpha\nbeta\ngamma\n"
	part := "\nbeta\ngamma\n" // leading blank line that isn't in `whole`
	replace := "BETA\nGAMMA\n"

	got, ok := replaceMostSimilarChunk(whole, part, replace)
	if !ok {
		t.Fatal("expected match after dropping the spurious leading blank line")
	}
	want := "alpha\nBETA\nGAMMA\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
