package main

// provider.go — the LLM transport layer + the shared type catalog.
//
// This is the curriculum's "generic agent core" (see .learn/plan.md). Every
// session (s01..s10) re-pastes these same types verbatim and extends them, so
// each chapter is a self-contained Go module with NO cross-session imports. The
// shape is Anthropic's Messages API wire format: a clean tagged-union model that
// maps onto every other provider.
//
// Upstream aider hides all of this behind litellm (aider/llm.py,
// aider/models.py send_completion). We expose it directly so a learner can see
// exactly what bytes go over the wire. s09 changes NOTHING about the transport
// here vs s01..s08 — the new mechanism this chapter teaches is the REFLECTION
// LOOP in reflect.go and the Linter in linter.go. The one type below that this
// chapter leans on is `Coder`: reflect.go drives a Coder.Run repeatedly,
// feeding lint errors back in as a synthetic user message until the code is
// clean or a reflection cap is hit. That is exactly upstream's run_one loop
// around `reflected_message` (aider/coders/base_coder.py L924-944).

import (
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
// (alternating user / assistant) plus an optional system prompt. In s09 the
// reflection message we synthesize from lint output is exactly this shape: one
// more user turn appended to the running conversation.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message. The wire format is a tagged union
// over Type ("text" | "tool_use" | "tool_result"); the JSON encoder relies on
// `omitempty` to suppress fields that don't apply. s09 only uses Type=="text",
// but we carry the full union so the type matches the catalog verbatim.
type ContentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	ToolUseID   string      `json:"tool_use_id,omitempty"`
	ToolContent interface{} `json:"content,omitempty"`
}

// ToolSchema describes a callable tool to the model. aider (and s09) steer the
// model with a system PROMPT to reply in a text edit format rather than calling
// a tool, so this type is carried for catalog fidelity but unused by the loop.
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

// Provider abstracts the single LLM call. This is litellm's job upstream. s10
// will add retries + streaming + a model registry behind this exact interface,
// which is why even s09 programs against the interface rather than a concrete
// client (tests inject a fakeProvider that scripts a "broken then fixed" reply
// sequence to exercise the reflection loop).
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// Tool is the contract for built-in tools (bash, edit-file). Unused by s09's
// loop but kept for catalog fidelity.
type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

// ---- aider-specific shared types (canonical, from .learn/plan.md) ----

// EditFormat names the strategy the LLM is told to emit (upstream
// Coder.edit_format).
type EditFormat string

const (
	FormatWhole EditFormat = "whole" // wholefile_coder.py  (s02) — return the entire new file
	FormatDiff  EditFormat = "diff"  // editblock_coder.py  (s03) — SEARCH/REPLACE blocks
	FormatUDiff EditFormat = "udiff" // udiff_coder.py      (s04) — unified diff
)

// Edit is one parsed file change. For whole-file, Search is "" and Replace holds
// the entire new file. For diff/udiff, Search is the text to find and Replace
// the substitution (upstream tuples: (path, original, updated)).
type Edit struct {
	Path    string
	Search  string // "" means whole-file replace / create-file
	Replace string
}

// Coder is the per-format strategy: parse the LLM reply, then apply edits.
// Mirrors upstream Coder.get_edits / Coder.apply_edits. s09 wraps a Coder's
// single turn (Run) in a reflection loop; the Coder itself is format-agnostic
// from the loop's point of view, so the demo ships a tiny whole-file coder.
type Coder interface {
	Format() EditFormat
	SystemPrompt(fence [2]string) string
	GetEdits(response string) ([]Edit, error)
	ApplyEdits(edits []Edit) (applied, failed []Edit, err error)
}

// ---- AnthropicProvider: native /v1/messages ----

// AnthropicProvider talks to Anthropic's native /v1/messages endpoint. The wire
// format IS our internal format, so there is no translation to do here (compare
// OpenAIProvider below, which translates both ways).
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

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
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

// ---- OpenAIProvider: any OpenAI-compatible Chat Completions API ----

// OpenAIProvider works with any OpenAI-compatible endpoint (OpenAI, DeepSeek,
// Moonshot/Kimi, Qwen via DashScope, Groq, OpenRouter, self-hosted vLLM/SGLang).
// We keep the program's INTERNAL types Anthropic-shaped and translate at this
// boundary, so reflect.go and main.go are identical no matter which model the
// user picked.
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

func NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (o *OpenAIProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	// Translate our Anthropic-shaped request into OpenAI chat messages.
	var msgs []map[string]interface{}
	if req.System != "" {
		msgs = append(msgs, map[string]interface{}{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		var sb strings.Builder
		for _, b := range m.Content {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		msgs = append(msgs, map[string]interface{}{"role": m.Role, "content": sb.String()})
	}
	payload := map[string]interface{}{
		"model":      model,
		"messages":   msgs,
		"max_tokens": maxTokens,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("openai-compat API %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("decode openai response: %w (body=%s)", err, string(respBody))
	}

	out := &CreateMessageResponse{
		Role:       "assistant",
		StopReason: "end_turn",
		Usage: Usage{
			InputTokens:  oaiResp.Usage.PromptTokens,
			OutputTokens: oaiResp.Usage.CompletionTokens,
		},
	}
	if len(oaiResp.Choices) > 0 {
		out.Content = append(out.Content, ContentBlock{Type: "text", Text: oaiResp.Choices[0].Message.Content})
		if oaiResp.Choices[0].FinishReason == "length" {
			out.StopReason = "max_tokens"
		}
	}
	return out, nil
}
