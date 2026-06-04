package main

// provider.go — the generic LLM core (Anthropic wire shape) + the native
// AnthropicProvider, ported from the curriculum's provider template and extended
// for s10 with streaming.
//
// Every session in this curriculum re-pastes the shared types below verbatim and
// extends them; there are NO cross-session imports. What s10 adds on top of the
// s01 baseline:
//
//   - CreateMessageRequest gains a `Stream` field (the wire flag),
//   - the Provider interface gains a streaming sibling method (StreamMessage),
//   - AnthropicProvider learns to decode Anthropic's server-sent-events (SSE)
//     stream and reassemble it into ONE CreateMessageResponse.
//
// The retry decorator (retry.go) and the model registry (models.go) wrap this
// same Provider interface — that is the whole point of the chapter: the single
// concrete Provider of s01 becomes a configurable, resilient, streaming stack
// without the loop above ever changing.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---- Generic LLM core (Anthropic wire shape) ----

// Message is a single turn in the conversation. The API takes a list of these
// (alternating user / assistant) plus an optional system prompt.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message. Anthropic's wire format is a tagged
// union over Type ("text" | "tool_use" | "tool_result"); the JSON encoder relies
// on `omitempty` to suppress fields that don't apply to a given block type.
type ContentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	ToolUseID   string      `json:"tool_use_id,omitempty"`
	ToolContent interface{} `json:"content,omitempty"`
}

type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// CreateMessageRequest is the request body. s10 adds Stream: when true the
// provider opens an SSE stream instead of waiting for the whole JSON body.
type CreateMessageRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Messages  []Message    `json:"messages"`
	Tools     []ToolSchema `json:"tools,omitempty"`
	System    string       `json:"system,omitempty"`
	Stream    bool         `json:"stream,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type CreateMessageResponse struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// Provider abstracts the LLM call (litellm's job upstream). The whole agent loop
// is written against this one method, so retries, a model registry, streaming,
// and fakes all slot in behind it without touching the loop.
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// StreamingProvider is an OPTIONAL second interface. A provider that can stream
// implements it; callers that want live tokens type-assert for it and fall back
// to CreateMessage otherwise. Keeping it separate means s01-style providers and
// fakes that only do unary calls still satisfy Provider.
type StreamingProvider interface {
	Provider
	// StreamMessage calls onText for each text delta as it arrives, then returns
	// the fully reassembled response (same shape a unary call would return).
	StreamMessage(ctx context.Context, req CreateMessageRequest, onText func(string)) (*CreateMessageResponse, error)
}

// ---- Native Anthropic provider ----

type AnthropicProvider struct {
	apiKey string
	model  string
	client *http.Client
}

func NewAnthropicProvider(apiKey, model string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// statusError carries the HTTP status code so the retry decorator can decide
// whether the failure is transient (429 / 5xx) or fatal (4xx). Upstream gets
// this signal from litellm's exception taxonomy; we get it from the raw code.
type statusError struct {
	Code int
	Body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("anthropic API %d: %s", e.Code, e.Body)
}

func (a *AnthropicProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	a.applyDefaults(&req)
	req.Stream = false

	resp, err := a.do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &statusError{Code: resp.StatusCode, Body: string(respBody)}
	}

	var out CreateMessageResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w (body=%s)", err, string(respBody))
	}
	return &out, nil
}

// StreamMessage opens the SSE stream and folds its deltas back into one
// CreateMessageResponse via accumulateStream. onText fires for every text delta
// so a CLI can print tokens live.
func (a *AnthropicProvider) StreamMessage(ctx context.Context, req CreateMessageRequest, onText func(string)) (*CreateMessageResponse, error) {
	a.applyDefaults(&req)
	req.Stream = true

	resp, err := a.do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, &statusError{Code: resp.StatusCode, Body: string(body)}
	}
	return accumulateStream(resp.Body, onText)
}

func (a *AnthropicProvider) applyDefaults(req *CreateMessageRequest) {
	if req.Model == "" {
		req.Model = a.model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}
}

func (a *AnthropicProvider) do(ctx context.Context, req CreateMessageRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	return a.client.Do(httpReq)
}

// ---- SSE stream decoding ----

// sseEvent is one decoded `data:` line from Anthropic's stream. We only care
// about a few of the event types; the rest are ignored.
type sseEvent struct {
	Type  string `json:"type"` // message_start | content_block_delta | message_delta | ...
	Index int    `json:"index"`
	Delta struct {
		Type       string `json:"type"`        // text_delta | input_json_delta
		Text       string `json:"text"`        // for text_delta
		StopReason string `json:"stop_reason"` // on message_delta
	} `json:"delta"`
	Message struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		Usage Usage  `json:"usage"`
	} `json:"message"`
	Usage Usage `json:"usage"` // output tokens arrive on message_delta
}

// accumulateStream reads an Anthropic SSE body line by line and rebuilds the
// final response. This is the heart of s10's streaming half: many small
// `content_block_delta` events get folded into one text block, while
// `message_start` / `message_delta` carry the id, role, stop_reason and usage.
//
// We decode Anthropic's events DIRECTLY rather than going through litellm's
// provider-agnostic chunk shape — a deliberate divergence noted in the docs.
func accumulateStream(r io.Reader, onText func(string)) (*CreateMessageResponse, error) {
	out := &CreateMessageResponse{Role: "assistant"}
	var text strings.Builder

	sc := bufio.NewScanner(r)
	// SSE lines can be long (a whole JSON event); grow the buffer past the
	// 64 KiB default so a big delta never silently truncates.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()
		// SSE frames are "event: X\n" then "data: {...}\n\n". We only need data.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var ev sseEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			// A malformed event is skipped rather than aborting the whole stream;
			// the worst case is a missing delta, not a crash.
			continue
		}

		switch ev.Type {
		case "message_start":
			out.ID = ev.Message.ID
			if ev.Message.Role != "" {
				out.Role = ev.Message.Role
			}
			out.Usage.InputTokens = ev.Message.Usage.InputTokens
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				text.WriteString(ev.Delta.Text)
				if onText != nil {
					onText(ev.Delta.Text)
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				out.StopReason = ev.Delta.StopReason
			}
			if ev.Usage.OutputTokens != 0 {
				out.Usage.OutputTokens = ev.Usage.OutputTokens
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	if s := text.String(); s != "" {
		out.Content = append(out.Content, ContentBlock{Type: "text", Text: s})
	}
	if out.StopReason == "" {
		out.StopReason = "end_turn"
	}
	return out, nil
}
