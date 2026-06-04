package main

import (
	"strings"
	"testing"
)

// These tests pin the OpenAI <-> Anthropic translation in both directions, with
// no network. They are the proof that swapping `-provider` doesn't change any
// behavior the rest of s01 can observe.

// ---- request: Anthropic-shaped -> OpenAI ----

func TestTranslateRequest_SystemAndUser(t *testing.T) {
	req := CreateMessageRequest{
		System: "You edit one file.",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "rewrite it"}}},
		},
	}
	out := translateRequestToOpenAI(req, "deepseek-chat", 1024)
	if out.Model != "deepseek-chat" || out.MaxTokens != 1024 {
		t.Fatalf("model/max_tokens wrong: %+v", out)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("expected system+user = 2 messages, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "You edit one file." {
		t.Fatalf("system message wrong: %+v", out.Messages[0])
	}
	if out.Messages[1].Role != "user" || out.Messages[1].Content != "rewrite it" {
		t.Fatalf("user message wrong: %+v", out.Messages[1])
	}
}

func TestTranslateRequest_ToolsFlatToFunctions(t *testing.T) {
	req := CreateMessageRequest{Tools: []ToolSchema{EditFileTool()}}
	out := translateRequestToOpenAI(req, "x", 0)
	if len(out.Tools) != 1 || out.Tools[0].Type != "function" {
		t.Fatalf("tools not wrapped as functions: %+v", out.Tools)
	}
	if out.Tools[0].Function.Name != "edit_file" {
		t.Fatalf("tool name lost: %s", out.Tools[0].Function.Name)
	}
	props, ok := out.Tools[0].Function.Parameters["properties"].(map[string]interface{})
	if !ok || props["path"] == nil {
		t.Fatalf("parameters lost: %+v", out.Tools[0].Function.Parameters)
	}
}

// ---- response: OpenAI -> Anthropic-shaped ----

func TestTranslateResponse_PlainTextStop(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{
			{Index: 0, FinishReason: "stop", Message: openAIMessage{Role: "assistant", Content: "```\nhi\n```"}},
		},
		Usage: openAIUsage{PromptTokens: 10, CompletionTokens: 5},
	}
	out := translateResponseFromOpenAI(resp)
	if out.StopReason != "end_turn" {
		t.Fatalf("stop_reason: %q want end_turn", out.StopReason)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" {
		t.Fatalf("content wrong: %+v", out.Content)
	}
	if out.Usage.InputTokens != 10 || out.Usage.OutputTokens != 5 {
		t.Fatalf("usage wrong: %+v", out.Usage)
	}
}

func TestTranslateResponse_LengthBecomesMaxTokens(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{FinishReason: "length", Message: openAIMessage{Role: "assistant", Content: "trunc"}}},
	}
	if got := translateResponseFromOpenAI(resp).StopReason; got != "max_tokens" {
		t.Fatalf("got %q want max_tokens", got)
	}
}

// Some OpenAI-compatible servers return content as an array of
// {type:"text", text:"..."} blocks. The translator must concatenate them so the
// whole-file parser still sees one string.
func TestTranslateResponse_StructuredContentArray(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{FinishReason: "stop", Message: openAIMessage{
			Role: "assistant",
			Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "```\n"},
				map[string]interface{}{"type": "text", "text": "code\n```"},
			},
		}}},
	}
	out := translateResponseFromOpenAI(resp)
	if len(out.Content) != 1 || !strings.Contains(out.Content[0].Text, "code") {
		t.Fatalf("structured content not concatenated: %+v", out.Content)
	}
}

// Full round-trip: an OpenAIProvider response flows through the same path
// Coder.Run uses, and extractCodeBlock pulls the file body out of it. This is
// the end-to-end "swap the provider, nothing else changes" guarantee.
func TestOpenAIRoundTrip_FeedsExtractCodeBlock(t *testing.T) {
	file := "package main\n\nfunc main() {}\n"
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{FinishReason: "stop", Message: openAIMessage{
			Role:    "assistant",
			Content: "Here:\n```go\n" + file + "\n```",
		}}},
	}
	internal := translateResponseFromOpenAI(resp)
	body, ok := extractCodeBlock(firstText(internal.Content))
	if !ok {
		t.Fatal("expected to extract a code block from the translated response")
	}
	if body != file {
		t.Fatalf("round-trip body mismatch:\n got %q\nwant %q", body, file)
	}
}
