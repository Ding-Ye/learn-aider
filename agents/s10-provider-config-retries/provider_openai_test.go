package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// ---- OpenAI <-> Anthropic translation round-trip ----

// TestTranslateRequest_SystemUserAndTools: an Anthropic-shaped request becomes a
// well-formed OpenAI Chat Completions request — system message first, then the
// user turn, with tools wrapped under {type:function,function:{...}}.
func TestTranslateRequest_SystemUserAndTools(t *testing.T) {
	req := CreateMessageRequest{
		System: "You edit one file.",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "rewrite it"}}},
		},
		Tools: []ToolSchema{EditFileTool()},
	}
	out := translateRequestToOpenAI(req, "deepseek-chat", 1024)

	if out.Model != "deepseek-chat" || out.MaxTokens != 1024 {
		t.Fatalf("model/max_tokens wrong: %+v", out)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("expected system+user, got %d messages", len(out.Messages))
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "You edit one file." {
		t.Fatalf("system message wrong: %+v", out.Messages[0])
	}
	if out.Messages[1].Role != "user" || out.Messages[1].Content != "rewrite it" {
		t.Fatalf("user message wrong: %+v", out.Messages[1])
	}
	if len(out.Tools) != 1 || out.Tools[0].Type != "function" || out.Tools[0].Function.Name != "edit_file" {
		t.Fatalf("tool not wrapped as function: %+v", out.Tools)
	}
}

// TestOpenAIRoundTrip_TextResponse: an OpenAI response translates back to our
// Anthropic-shaped response with the right stop_reason and usage, and the body
// can be pulled out with extractCodeBlock — proving "swap the provider, the rest
// of the loop is unchanged".
func TestOpenAIRoundTrip_TextResponse(t *testing.T) {
	file := "package main\n\nfunc main() {}\n"
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{
			FinishReason: "stop",
			Message:      openAIMessage{Role: "assistant", Content: "Here:\n```go\n" + file + "```"},
		}},
		Usage: openAIUsage{PromptTokens: 12, CompletionTokens: 7},
	}
	out := translateResponseFromOpenAI(resp)

	if out.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", out.StopReason)
	}
	if out.Usage.InputTokens != 12 || out.Usage.OutputTokens != 7 {
		t.Fatalf("usage lost in translation: %+v", out.Usage)
	}
	body, ok := extractCodeBlock(firstText(out.Content))
	if !ok || body != file {
		t.Fatalf("round-trip body mismatch: ok=%v got %q", ok, body)
	}
}

// TestOpenAIRoundTrip_ToolCall: an OpenAI tool_calls response becomes an
// Anthropic tool_use block with parsed input and stop_reason=tool_use.
func TestOpenAIRoundTrip_ToolCall(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{
			FinishReason: "tool_calls",
			Message: openAIMessage{
				Role: "assistant",
				ToolCalls: []openAIToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: openAIToolCallFunc{
						Name:      "edit_file",
						Arguments: `{"path":"a.go","content":"x"}`,
					},
				}},
			},
		}},
	}
	out := translateResponseFromOpenAI(resp)
	if out.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use", out.StopReason)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "tool_use" {
		t.Fatalf("expected one tool_use block: %+v", out.Content)
	}
	if out.Content[0].Name != "edit_file" || out.Content[0].Input["path"] != "a.go" {
		t.Fatalf("tool_use payload wrong: %+v", out.Content[0])
	}
}

// ---- Anthropic request encoding ----

// TestAnthropicRequestEncode: the wire body carries the canonical fields. We
// don't hit the network; we encode the request body the provider would send and
// assert its JSON shape, including stream=true when set and its omission otherwise.
func TestAnthropicRequestEncode(t *testing.T) {
	req := CreateMessageRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 256,
		System:    "be terse",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if got["model"] != "claude-sonnet-4-6" {
		t.Fatalf("model field wrong: %v", got["model"])
	}
	if got["max_tokens"].(float64) != 256 {
		t.Fatalf("max_tokens wrong: %v", got["max_tokens"])
	}
	if got["system"] != "be terse" {
		t.Fatalf("system field wrong: %v", got["system"])
	}
	// stream is omitempty: a false value must NOT appear on the wire.
	if _, present := got["stream"]; present {
		t.Fatalf("stream should be omitted when false, body=%s", body)
	}

	// And when streaming is on, the flag IS present and true.
	req.Stream = true
	body2, _ := json.Marshal(req)
	if !bytes.Contains(body2, []byte(`"stream":true`)) {
		t.Fatalf("stream=true not encoded: %s", body2)
	}
}

// ---- Anthropic SSE streaming decode ----

// sampleSSE is a realistic Anthropic event stream: message_start (id + input
// usage), three text deltas, then message_delta (stop_reason + output usage).
const sampleSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","role":"assistant","usage":{"input_tokens":9,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`

// TestStreamDecoder_ReassemblesChunks: the SSE decoder folds many text deltas
// into one text block, recovers id/stop_reason/usage, and calls onText for each
// delta in order. This is s10's streaming half.
func TestStreamDecoder_ReassemblesChunks(t *testing.T) {
	var seen []string
	resp, err := accumulateStream(strings.NewReader(sampleSSE), func(tok string) {
		seen = append(seen, tok)
	})
	if err != nil {
		t.Fatalf("accumulateStream: %v", err)
	}
	if resp.ID != "msg_01" || resp.Role != "assistant" {
		t.Fatalf("message_start not captured: %+v", resp)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "Hello, world" {
		t.Fatalf("text not reassembled: %+v", resp.Content)
	}
	if resp.StopReason != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage wrong: %+v", resp.Usage)
	}
	wantSeen := []string{"Hello", ", ", "world"}
	if strings.Join(seen, "|") != strings.Join(wantSeen, "|") {
		t.Fatalf("onText callbacks = %v, want %v", seen, wantSeen)
	}
}

// TestStreamDecoder_SkipsGarbage: a malformed data line is skipped, not fatal —
// the stream still produces the valid text around it.
func TestStreamDecoder_SkipsGarbage(t *testing.T) {
	stream := "data: {not json}\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n" +
		`data: [DONE]` + "\n"
	resp, err := accumulateStream(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("garbage should be skipped, not error: %v", err)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "ok" {
		t.Fatalf("valid delta after garbage lost: %+v", resp.Content)
	}
	if resp.StopReason != "end_turn" { // default when no message_delta arrived
		t.Fatalf("stop_reason default = %q, want end_turn", resp.StopReason)
	}
}
