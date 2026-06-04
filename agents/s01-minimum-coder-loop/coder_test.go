package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProvider implements Provider without any network. It records the request
// it was handed (so a test can assert on the wire shape) and returns a canned
// reply. This is the same trick every later session uses: the loop programs
// against the Provider interface, so a struct literal can stand in for Anthropic.
type fakeProvider struct {
	lastReq CreateMessageRequest
	reply   string // becomes the single text block of the response
}

func (f *fakeProvider) CreateMessage(_ context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	f.lastReq = req
	return &CreateMessageResponse{
		Role:       "assistant",
		StopReason: "end_turn",
		Content:    []ContentBlock{{Type: "text", Text: f.reply}},
		Usage:      Usage{InputTokens: 11, OutputTokens: 22},
	}, nil
}

// (1) extractCodeBlock parses a fenced block and strips the fences + lang tag.
func TestExtractCodeBlock(t *testing.T) {
	reply := "Sure, here is the file:\n\n```go\npackage main\n\nfunc main() {}\n```\n\nDone."
	body, ok := extractCodeBlock(reply)
	if !ok {
		t.Fatal("expected to find a fenced block")
	}
	want := "package main\n\nfunc main() {}"
	if body != want {
		t.Fatalf("body mismatch:\n got %q\nwant %q", body, want)
	}

	// No fence -> not found, no panic.
	if _, ok := extractCodeBlock("just prose, no code"); ok {
		t.Fatal("expected no block in fence-less text")
	}
	// Opening fence but no closing fence -> malformed -> not found.
	if _, ok := extractCodeBlock("```go\npackage main"); ok {
		t.Fatal("expected unterminated fence to be rejected")
	}
}

// (2) applyWholeFile writes Edit.Replace as the complete file contents.
func TestApplyWholeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	c := NewCoder(&fakeProvider{}, false)

	edit := Edit{Path: path, Search: "", Replace: "new contents\n"}
	if err := c.applyWholeFile(edit); err != nil {
		t.Fatalf("applyWholeFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "new contents\n" {
		t.Fatalf("file contents = %q", string(got))
	}

	// A non-whole-file edit (Search != "") must be rejected, not silently mishandled.
	if err := c.applyWholeFile(Edit{Path: path, Search: "x", Replace: "y"}); err == nil {
		t.Fatal("expected applyWholeFile to reject a diff-style edit")
	}
}

// (3) Coder.Run end-to-end against the fake provider: it reads a temp file,
// sends it, and rewrites the file with the model's fenced block.
func TestCoderRun_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.go")
	original := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	rewritten := "package main\n\n// main is the entry point.\nfunc main() {}\n"
	fp := &fakeProvider{reply: "Here you go:\n\n```go\n" + rewritten + "\n```\n"}

	c := NewCoder(fp, false)
	c.out, _ = os.Open(os.DevNull) // swallow the "Applied edit" line
	if err := c.Run(context.Background(), path, "add a doc comment to main"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != rewritten {
		t.Fatalf("file not rewritten:\n got %q\nwant %q", string(got), rewritten)
	}

	// The request must have carried the original file + the instruction.
	userText := fp.lastReq.Messages[0].Content[0].Text
	if !strings.Contains(userText, original) {
		t.Error("request did not include the original file contents")
	}
	if !strings.Contains(userText, "add a doc comment to main") {
		t.Error("request did not include the instruction")
	}
	if fp.lastReq.System == "" {
		t.Error("request had no system prompt defining the edit format")
	}
}

// (3b) A reply with no fenced block yields an error and leaves the file untouched.
func TestCoderRun_NoFenceNoChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keep.txt")
	original := "do not touch\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	c := NewCoder(&fakeProvider{reply: "I cannot help with that."}, false)
	c.errw, _ = os.Open(os.DevNull) // swallow the diagnostic dump
	if err := c.Run(context.Background(), path, "anything"); err == nil {
		t.Fatal("expected an error when the reply has no code block")
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("file should be unchanged, got %q", string(got))
	}
}

// (4) The provider request JSON encodes role/content the way the wire format
// expects: a "user" role with a "text" content block.
func TestProviderRequestJSONShape(t *testing.T) {
	req := CreateMessageRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 4096,
		System:    "be terse",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Round-trip into a generic map and assert the nested shape.
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs, ok := m["messages"].([]interface{})
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages not a 1-element array: %v", m["messages"])
	}
	first := msgs[0].(map[string]interface{})
	if first["role"] != "user" {
		t.Fatalf("role = %v, want user", first["role"])
	}
	content := first["content"].([]interface{})
	block := content[0].(map[string]interface{})
	if block["type"] != "text" || block["text"] != "hi" {
		t.Fatalf("content block wrong: %v", block)
	}
	// omitempty must drop the union fields that don't apply to a text block.
	if _, present := block["tool_use_id"]; present {
		t.Error("tool_use_id should be omitted on a text block")
	}
	if _, present := block["input"]; present {
		t.Error("input should be omitted on a text block")
	}
}

// EditFileTool is documented but unused by the loop; assert it at least produces
// a well-formed schema (a learner who switches to tool-calling later starts here).
func TestEditFileToolSchema(t *testing.T) {
	s := EditFileTool()
	if s.Name != "edit_file" {
		t.Fatalf("tool name = %q", s.Name)
	}
	props, ok := s.InputSchema["properties"].(map[string]interface{})
	if !ok || props["path"] == nil || props["contents"] == nil {
		t.Fatalf("schema missing path/contents: %+v", s.InputSchema)
	}
}
