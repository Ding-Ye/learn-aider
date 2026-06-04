---
title: "Multi-model integration guide"
slug: multi-model
est_read_min: 11
---

# Multi-model integration guide

> This course hides "which LLM am I calling" behind a single `Provider` interface. The result: the exact same chapter code runs on Anthropic Claude, and swaps in one line to DeepSeek, Qwen, Moonshot/Kimi, Groq, OpenRouter, or an open model you self-host with vLLM/SGLang — without touching a line of the agent logic.

## Why multi-model

From s01 through s10, every chapter demonstrates one mechanism of a coder agent: whole-file edits, SEARCH/REPLACE blocks, unified diffs, prompt organization, the repo map, linter reflection, and so on. None of those mechanisms care which model is behind them. Pinning the course to one vendor's API would throw away half its value:

- Many learners only have a **DeepSeek / Qwen / Kimi** key on hand; they shouldn't have to sign up elsewhere just to run a demo.
- To save money, you can drive the brain-dead steps with a cheap small model (or a local one) and reserve an expensive model for the genuinely hard edits.
- For offline or self-hosted setups, **vLLM / SGLang** expose an OpenAI-compatible endpoint, which should just work out of the box.

So the course deliberately funnels every LLM call behind one interface. Switching models = swap the implementation + swap a base URL + swap an API-key env var, and that's it. This is exactly what upstream aider achieves with litellm; we reproduce the load-bearing part in minimal Go.

## The Provider abstraction

The whole trick is that the program speaks one internal data shape — Anthropic's Messages format (`Message` / `ContentBlock`, with block types `text` / `tool_use` / `tool_result`, and `stop_reason`). That shape is a clean tagged union that maps cleanly onto every other vendor's protocol. The interface itself is tiny — a single method (see `agents/s01-minimum-coder-loop/provider.go`):

```go
type Provider interface {
    CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}
```

It has two implementations with an identical outward shape:

- **`AnthropicProvider`** (`agents/s01-minimum-coder-loop/provider.go`) — talks directly to `https://api.anthropic.com/v1/messages`. Because the internal shape *is* Anthropic's wire format, there is **no translation here**.
- **`OpenAIProvider`** (`agents/s01-minimum-coder-loop/provider_openai.go`) — works with *any* OpenAI-compatible Chat Completions API. It does the **two-way translation** at the provider boundary:

  ```
  Anthropic-shaped request   ->  OpenAI Chat Completions request
  OpenAI Chat Completions response  ->  Anthropic-shaped response
  ```

  The request side (`translateRequestToOpenAI`) does three things: the system prompt becomes a `{role:"system"}` message; each Anthropic message expands into one or more OpenAI messages (an assistant's `tool_use` blocks become `tool_calls`; a user's `tool_result` blocks each become a separate `{role:"tool"}` message); tool definitions get wrapped under `{type:"function", function:{...}}`. The response side (`translateResponseFromOpenAI`) maps `finish_reason` back to `stop_reason` (`stop`→`end_turn`, `tool_calls`→`tool_use`, `length`→`max_tokens`) and tolerates providers that return `content` as an array of blocks rather than a string (`contentToString`).

Doing the translation in this one place means `coder.go`, `main.go`, and every later chapter stay **byte-for-byte unchanged** no matter which model the user picked.

By **s10** this abstraction is pushed to its full form (`agents/s10-provider-config-retries/`):

- **A model registry** (`agents/s10-provider-config-retries/models.go`) — a `map[string]ModelConfig` mapping a model name to `{backend, baseURL, API-key env var, context window, default edit format}`. There's also an `aliases` table so you can type short names like `sonnet`, `deepseek`, `kimi`, `qwen`. `buildProvider` is the **only** place that knows about concrete provider types — adding a backend is adding a row of data, not writing a branch.
- **A retry decorator** (`agents/s10-provider-config-retries/retry.go`) — `RetryProvider` both *is* a `Provider` and *wraps* a `Provider`, surviving 429/5xx with exponential backoff + jitter. Because both providers raise the same `statusError` type, the retry logic is oblivious to which backend is behind it and classifies transient vs fatal identically.

## One-line swap

s01 is the first entry point a user touches, and its `main.go` exposes the whole set of 8 provider profiles directly as command-line flags: `-provider` picks a profile, `-base-url` overrides the endpoint, `-model` overrides the model id. Switching is just changing the provider name:

```bash
cd agents/s01-minimum-coder-loop

# Default: Anthropic Claude
export ANTHROPIC_API_KEY=sk-ant-...
go run . hello.go "add a doc comment to main"

# Switch to DeepSeek, zero changes to the agent code
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -model deepseek-chat hello.go "rename foo to bar"

# Switch to Qwen (via DashScope's OpenAI-compatible endpoint)
export DASHSCOPE_API_KEY=sk-...
go run . -provider qwen hello.go "fix the typo"

# Self-hosted locally (vLLM / SGLang) — point -base-url at your endpoint
go run . -provider local -base-url http://localhost:8000/v1 -model your-model app.py "..."
```

Each profile maps to an endpoint, a default model, and the env var its key is read from:

| `-provider` | endpoint | default model | API-key env var |
|---|---|---|---|
| `anthropic` (default) | api.anthropic.com (native) | claude-sonnet-4-6 | `ANTHROPIC_API_KEY` |
| `openai` | api.openai.com/v1 | gpt-4o-mini | `OPENAI_API_KEY` |
| `deepseek` | api.deepseek.com/v1 | deepseek-chat | `DEEPSEEK_API_KEY` |
| `moonshot` | api.moonshot.cn/v1 | moonshot-v1-8k | `MOONSHOT_API_KEY` |
| `qwen` | dashscope.aliyuncs.com/compatible-mode/v1 | qwen-plus | `DASHSCOPE_API_KEY` |
| `groq` | api.groq.com/openai/v1 | llama-3.3-70b-versatile | `GROQ_API_KEY` |
| `openrouter` | openrouter.ai/api/v1 | openai/gpt-4o-mini | `OPENROUTER_API_KEY` |
| `local` | http://localhost:8000/v1 | local-model | `OPENAI_API_KEY` |

The logic in s01's `main.go` is plain: pick `anthropic` and you get `NewAnthropicProvider`; everything else goes through `NewOpenAIProvider` (with the profile's base URL). s10 goes one step further — a single `-model` name (e.g. `-model sonnet`, `-model deepseek`, `-model r1`) resolves through the registry into the full config and is wrapped in retries automatically: `go run . -model deepseek "explain goroutines in one line"`.

The other chapters (s02–s09) keep Anthropic by default, and wiring another model into them is easy: copy s01's `providerProfiles` table + flag parsing, or just change the provider-building line to `NewOpenAIProvider(os.Getenv("DEEPSEEK_API_KEY"), "https://api.deepseek.com/v1", "deepseek-chat")`.

## Mapping to upstream aider

aider uses **litellm** for the same purpose — one unified interface that hides every provider. Compared against upstream (pinned in `.learn/upstream`, sha `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`):

- `aider/llm.py` is a thin `LazyLiteLLM` wrapper that lazily imports litellm (because `import litellm` takes 1.5 seconds). Every model call in aider ultimately lands on `litellm.completion(...)`.
- In `aider/models.py`, `MODEL_ALIASES` (L99) gives models short names, `class ModelSettings` (L128) describes per-model settings, `class Model` (L329) resolves aliases at L334, `send_completion` (L985) calls `litellm.completion` (L1036), and `simple_send_with_retries` (L1039) runs a `while True` backoff-retry loop.

Our s10 `models.go` is the minimal version of `ModelSettings` + `MODEL_ALIASES` (its comments cite the upstream line numbers), and `retry.go` is the Go port of `simple_send_with_retries`. **The difference is coverage**: litellm alone supports 50+ providers, internally normalizing each one's quirky auth, params, and chunk formats; our mini covers just two paths — **the broad "OpenAI-compatible" class + Anthropic native**. Fortunately almost every mainstream model exposes an OpenAI-compatible endpoint, so those two paths already cover the vast majority of everyday use, with all the code laid out in front of you — which is exactly the course's trade-off: readability over coverage.

## Caveats

The abstraction isn't a free lunch. When swapping models, watch for these:

- **Tool / JSON differences.** Tool-calling support is uneven across vendors: some models on OpenRouter don't support tools at all; a local vLLM needs `--enable-auto-tool-choice` before it will actually emit a tool call. Some providers (DeepSeek occasionally) return `content` as an array instead of a string — the translation layer's `contentToString` already handles that. This is precisely why the course's edit chapters mostly **steer the model to reply with a fenced code block rather than relying on a tool call**: it's the most portable across providers.
- **Context windows vary a lot.** Each model's `ContextWindow` is right there in the registry: Claude is 200K, gpt-4o 128K, DeepSeek 64K, while `moonshot-v1-8k` is only 8K. Feeding a big file / large repo map to a small-window model overflows immediately — size your input to the model.
- **Default params differ per model.** For example, the registry pins `temperature: 0.0` for DeepSeek (mirroring aider's choice for more deterministic edits) and defaults small models to the whole-file format (more reliable than diff). Each vendor's `max_tokens` default also differs; set it explicitly when it matters.
- **Some chapters don't call an LLM at all.** Several chapters are **pure mechanism demos** — the repo map's PageRank ranking, parsing SEARCH/REPLACE blocks, applying a unified diff — which are deterministic algorithms that need no model. "Multi-model" is moot for them; the chosen provider doesn't affect their output.
- **Rate limits and free tiers.** Groq's free tier has an RPM cap that batch jobs hit easily — which is exactly what s10's retry decorator is for: a 429 is automatically backed off and retried rather than failing outright.
