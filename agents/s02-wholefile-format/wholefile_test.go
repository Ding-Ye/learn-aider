package main

// wholefile_test.go — tests for the whole-file parser + apply.
//
// No network: fakeProvider implements the Provider interface and returns a
// canned reply, exactly like every chapter's tests. The parser tests feed
// GetEdits raw reply strings; the apply test writes into t.TempDir().

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider returns a fixed reply, ignoring the request. It records the last
// request so a test could assert on the prompt if it wanted to.
type fakeProvider struct {
	reply   string
	lastReq CreateMessageRequest
}

func (f *fakeProvider) CreateMessage(_ context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	f.lastReq = req
	return &CreateMessageResponse{
		Role:       "assistant",
		Content:    []ContentBlock{{Type: "text", Text: f.reply}},
		StopReason: "end_turn",
	}, nil
}

// Test 1: a single file block parses into exactly one Edit with the right path
// and the fenced body as Replace (the fences themselves stripped).
func TestGetEdits_SingleFile(t *testing.T) {
	reply := "Here is the change you asked for:\n\n" +
		"hello.go\n" +
		"```go\n" +
		"package main\n\n" +
		"func main() {}\n" +
		"```\n"

	c := NewWholeFileCoder(".", []string{"hello.go"})
	edits, err := c.GetEdits(reply)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("want 1 edit, got %d: %+v", len(edits), edits)
	}
	if edits[0].Path != "hello.go" {
		t.Errorf("path = %q, want hello.go", edits[0].Path)
	}
	if edits[0].Search != "" {
		t.Errorf("whole-file Search must be empty, got %q", edits[0].Search)
	}
	want := "package main\n\nfunc main() {}"
	if edits[0].Replace != want {
		t.Errorf("Replace = %q, want %q", edits[0].Replace, want)
	}
}

// Test 2: two fenced blocks in one reply parse into two Edits with the correct
// paths and bodies — the multi-file behavior s01 could not do.
func TestGetEdits_MultipleFiles(t *testing.T) {
	reply := "I split the parser out.\n\n" +
		"main.go\n" +
		"```go\n" +
		"package main\n" +
		"```\n\n" +
		"util.go\n" +
		"```go\n" +
		"package main\n\n" +
		"func parse() {}\n" +
		"```\n"

	c := NewWholeFileCoder(".", []string{"main.go", "util.go"})
	edits, err := c.GetEdits(reply)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(edits) != 2 {
		t.Fatalf("want 2 edits, got %d: %+v", len(edits), edits)
	}
	byPath := map[string]string{}
	for _, e := range edits {
		byPath[e.Path] = e.Replace
	}
	if byPath["main.go"] != "package main" {
		t.Errorf("main.go body = %q", byPath["main.go"])
	}
	if byPath["util.go"] != "package main\n\nfunc parse() {}" {
		t.Errorf("util.go body = %q", byPath["util.go"])
	}
}

// Test 3: the filename on the line above the fence is recovered even when the
// model wraps it in **bold**, backticks, a markdown heading, or a trailing
// colon. This is the heuristic core of the chapter.
func TestGetEdits_FilenameCleanup(t *testing.T) {
	cases := []struct {
		name      string
		fnameLine string
	}{
		{"bold", "**app.go**"},
		{"backticks", "`app.go`"},
		{"trailing colon", "app.go:"},
		{"heading", "# app.go"},
		{"bold and colon", "**app.go:**"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reply := tc.fnameLine + "\n```go\npackage app\n```\n"
			c := NewWholeFileCoder(".", []string{"app.go"})
			edits, err := c.GetEdits(reply)
			if err != nil {
				t.Fatalf("GetEdits: %v", err)
			}
			if len(edits) != 1 || edits[0].Path != "app.go" {
				t.Fatalf("filename %q not cleaned to app.go: %+v", tc.fnameLine, edits)
			}
		})
	}
}

// Test 4: ApplyEdits writes each whole-file Edit to disk under root, creating
// parent directories. We apply into t.TempDir() and read the bytes back.
func TestApplyEdits_WritesFiles(t *testing.T) {
	dir := t.TempDir()
	c := NewWholeFileCoder(dir, nil)

	edits := []Edit{
		{Path: "a.go", Replace: "package a\n"},
		{Path: "sub/b.go", Replace: "package b\n"}, // nested dir must be created
	}
	applied, failed, err := c.ApplyEdits(edits)
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	if len(applied) != 2 || len(failed) != 0 {
		t.Fatalf("applied=%d failed=%d, want 2/0", len(applied), len(failed))
	}
	for _, e := range edits {
		got, readErr := os.ReadFile(filepath.Join(dir, e.Path))
		if readErr != nil {
			t.Fatalf("read back %s: %v", e.Path, readErr)
		}
		if string(got) != e.Replace {
			t.Errorf("%s = %q, want %q", e.Path, string(got), e.Replace)
		}
	}
}

// Test 5: a bare fence with no filename above it falls back to the sole chat
// file (upstream's len(chat_files)==1 case). With >1 chat file it is an error.
func TestGetEdits_SingleChatFileFallback(t *testing.T) {
	// A "bare" fence has a BLANK line above it, so no filename is recovered and
	// the fallback must kick in. (A non-blank preceding line, even prose, would
	// be taken as the filename — faithful to upstream's L57-84 ordering.)
	reply := "Rewritten below:\n\n```go\npackage only\n```\n"

	// One chat file -> the bare block must target it.
	c := NewWholeFileCoder(".", []string{"only.go"})
	edits, err := c.GetEdits(reply)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(edits) != 1 || edits[0].Path != "only.go" {
		t.Fatalf("bare fence did not fall back to only.go: %+v", edits)
	}

	// Two chat files + no filename -> genuinely ambiguous -> error.
	c2 := NewWholeFileCoder(".", []string{"a.go", "b.go"})
	if _, err := c2.GetEdits(reply); err == nil {
		t.Fatalf("expected an error for an unnamed block with 2 chat files")
	}
}

// Test 6: a >250-char "filename" is rejected (Issue #1232); a bogus "path/to/"
// prefix collapses to the basename when the basename is a chat file (L71-72).
func TestGetEdits_FilenameGuards(t *testing.T) {
	// Bogus directory prefix collapses to basename.
	reply := "src/very/deep/foo.go\n```go\npackage foo\n```\n"
	c := NewWholeFileCoder(".", []string{"foo.go"})
	edits, err := c.GetEdits(reply)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(edits) != 1 || edits[0].Path != "foo.go" {
		t.Fatalf("bogus prefix not collapsed to foo.go: %+v", edits)
	}

	// Absurdly long filename -> dropped -> falls back to the single chat file.
	long := strings.Repeat("x", 300)
	reply2 := long + "\n```go\npackage bar\n```\n"
	c2 := NewWholeFileCoder(".", []string{"bar.go"})
	edits2, err := c2.GetEdits(reply2)
	if err != nil {
		t.Fatalf("GetEdits: %v", err)
	}
	if len(edits2) != 1 || edits2[0].Path != "bar.go" {
		t.Fatalf("250+ char name not rejected/fallen-back: %+v", edits2)
	}
}

// Test 7: end-to-end through the run() loop with a fakeProvider — proves the
// parse -> apply pipeline edits real files on disk for a multi-file reply.
func TestRun_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	// Seed two files the "model" will rewrite.
	mustWrite(t, filepath.Join(dir, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "util.go"), "package main\n")

	reply := "main.go\n```go\npackage main\n\nfunc main() { parse() }\n```\n\n" +
		"util.go\n```go\npackage main\n\nfunc parse() {}\n```\n"
	fp := &fakeProvider{reply: reply}

	// run() roots the coder at ".", so operate from the temp dir.
	chdir(t, dir)
	if err := run(context.Background(), fp, []string{"main.go", "util.go"}, "wire parse()", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(got), "parse()") {
		t.Errorf("main.go not rewritten: %q", string(got))
	}
	got2, _ := os.ReadFile(filepath.Join(dir, "util.go"))
	if !strings.Contains(string(got2), "func parse()") {
		t.Errorf("util.go not rewritten: %q", string(got2))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
}

// chdir switches to dir for the duration of the test, restoring afterward.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}
