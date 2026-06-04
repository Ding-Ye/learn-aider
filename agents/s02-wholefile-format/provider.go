package main

// provider.go — the generic LLM core, re-pasted from the curriculum's shared
// types catalog. Every session in learn-aider is a standalone Go module with NO
// cross-session imports, so each one re-declares Provider / Message /
// ContentBlock / ToolSchema verbatim and extends them. (s10 is where this layer
// grows retries + streaming + a model registry; here it is the bare minimum.)
//
// Nothing in this file is whole-file-specific — it is the same Anthropic wire
// shape s01 used. The new mechanism lives entirely in wholefile.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ---- Generic LLM core (Anthropic wire shape) ----

// Message is a single turn in the conversation. The API takes a list of these
// (alternating user / assistant) plus an optional system prompt.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message: a tagged union over Type
// ("text" | "tool_use" | "tool_result"). The JSON encoder relies on omitempty
// to suppress fields that don't apply to a given block type.
type ContentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	ToolUseID   string      `json:"tool_use_id,omitempty"`
	ToolContent interface{} `json:"content,omitempty"`
}

// ToolSchema is the contract advertised to the model for a built-in tool. s02
// still steers the model to reply in prose (the whole-file format), so this is
// declared for continuity but not wired into a tool-call loop yet.
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

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

// Provider abstracts the LLM call (litellm's job upstream). Because the loop
// only ever names this interface — never a concrete client — tests inject a
// fakeProvider (see wholefile_test.go) and s10 can wrap retries + streaming
// behind the same one method.
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// Tool is the contract for built-in tools (bash, edit-file). Declared here to
// match the shared catalog; the whole-file format does not use it.
type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

// ---- Anthropic transport ----

// AnthropicProvider is the one concrete Provider in s02: a thin POST to the
// Messages API. Anthropic is the native wire shape, so request/response structs
// above serialize directly with no translation layer.
type AnthropicProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

// NewAnthropicProvider builds a provider talking to Anthropic's public endpoint.
func NewAnthropicProvider(apiKey, model string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: "https://api.anthropic.com/v1/messages",
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

// NewOpenAICompatProvider builds a provider for an OpenAI-compatible Anthropic
// gateway (DeepSeek, OpenRouter, a local proxy, ...). The wire shape we send is
// still Anthropic's; only the endpoint + key differ. Upstream litellm hides this
// behind a registry — we keep it a one-liner because s10 is where that grows.
func NewOpenAICompatProvider(apiKey, model, baseURL string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *AnthropicProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	if req.Model == "" {
		req.Model = a.model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("anthropic API %d: %s", resp.StatusCode, string(respBody))
	}

	var out CreateMessageResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w (body=%s)", err, string(respBody))
	}
	return &out, nil
}

// firstText concatenates every text block in a response. The model usually
// replies with one text block, but concatenating is robust if a provider splits
// the reply across blocks.
func firstText(content []ContentBlock) string {
	var b bytes.Buffer
	for _, blk := range content {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}
