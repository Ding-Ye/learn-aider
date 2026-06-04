---
title: "s10 · Model/provider config + retries/streaming"
chapter: 10
slug: s10-provider-config-retries
est_read_min: 16
---

# s10 · Model/provider config + retries/streaming

> What this teaches: the *model/provider config layer* — aider's litellm-style abstraction. One `Provider` interface hides many backends (Anthropic native + every OpenAI-compatible API), a `ModelConfig` registry maps a name to `{backend, baseURL, contextWindow, defaultFormat}`, a `RetryProvider` decorator survives 429/5xx with exponential backoff, and an SSE decoder streams tokens live. This is the architectural capstone: the single concrete client of s01 becomes a configurable, resilient, streaming stack.

---

## Problem / 问题

Every chapter from s01 to s09 leaned on one hardcoded `AnthropicProvider` pointing at one endpoint. That is fine for a tutorial but collapses the moment you run aider for real. Users want *different models* — Claude for hard edits, a cheap local model for commit messages, DeepSeek or Qwen because that's what their key is for. Real endpoints return **429 rate-limit** and **5xx overloaded** errors constantly, and a tool that dies on the first transient blip is unusable. And a 30-second silent wait while a long reply generates feels broken — users expect tokens to **stream**.

The naive fixes make it worse: a `switch` on model name threaded through the loop, a bare `for` retry loop copy-pasted at every call site, and a streaming code path bolted on as an afterthought. aider's most distinctive layer — the litellm wrapper — exists precisely to avoid that mess. This chapter rebuilds its load-bearing core in Go: a registry, a retry decorator, and a stream decoder, all behind the *unchanged* `Provider` interface from s01.

## Solution / 解决方案

Treat the provider stack as **three composable pieces around one interface**, never as branches in the loop. (1) **Config is data.** A `ModelConfig` registry maps a name (and aliases like `sonnet`) to a row of settings; `buildProvider` is the only place that knows concrete provider types. Adding a backend is adding a row. (2) **Retries are a decorator.** `RetryProvider` *is* a `Provider` and *wraps* a `Provider`, so it composes cleanly: the registry builds the concrete client, retry wraps it, the loop above sees just a `Provider`. (3) **Streaming is an optional second interface.** A provider that can stream implements `StreamingProvider`; callers type-assert for it and fall back to a unary call otherwise.

Three decisions carry the design:

1. **The retry verdict is "transient vs fatal," nothing else.** Retry 429 + 5xx; fail fast on other 4xx. Burning the rate limit on a request that can never succeed (a 400) is worse than failing now. We read the verdict from a typed `statusError.Code`; upstream reads it from litellm's exception taxonomy.
2. **Backoff doubles and caps, exactly like upstream.** `delay` starts at 125 ms, doubles each retry, clamps at `MaxDelay`, with optional jitter to avoid thundering herds — a near-direct port of `simple_send_with_retries`'s `retry_delay *= 2`.
3. **We decode Anthropic SSE directly.** Upstream normalizes every provider's chunks through litellm; we fold Anthropic's `content_block_delta` events into one `CreateMessageResponse` ourselves. A deliberate divergence (noted below) that keeps the lesson concrete.

## How It Works / 工作原理

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  -model "sonnet"                                                │
│        │  LookupModel: alias → canonical → ModelConfig          │
│        ▼                                                        │
│  ModelConfig{ backend, baseURL, ctxWindow, defaultFormat }     │
│        │  buildProvider (the only switch on backend)           │
│        ▼                                                        │
│   ┌─────────────────────┐    ┌──────────────────────────────┐ │
│   │ AnthropicProvider   │ or │ OpenAIProvider (translates)   │ │
│   └─────────────────────┘    └──────────────────────────────┘ │
│        │                                                        │
│        ▼  NewRetryProvider(base, cfg)                          │
│   ┌───────────────────────────────────────────────┐           │
│   │ RetryProvider  (IS-A Provider, WRAPS Provider) │           │
│   │   429/5xx → backoff ×2 (cap)   4xx → fail fast │           │
│   └───────────────────────────────────────────────┘           │
│        │ CreateMessage (unary)      │ StreamMessage (SSE)      │
│        ▼                            ▼                          │
│   *CreateMessageResponse      accumulateStream folds deltas    │
│   the loop sees just a Provider — oblivious to all the above   │
└────────────────────────────────────────────────────────────────┘
```

The retry core — backoff, the cap, and the transient/fatal gate (excerpt from [`agents/s10-provider-config-retries/retry.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s10-provider-config-retries/retry.go)):

```go
// withRetry runs `call` up to cfg.MaxRetries+1 times. After each transient
// failure it sleeps `delay`, then doubles `delay` (capped at MaxDelay), exactly
// as upstream multiplies retry_delay by 2 and stops once it passes RETRY_TIMEOUT.
func withRetry[T any](ctx context.Context, cfg RetryConfig, call func() (T, error)) (T, error) {
	var zero T
	delay := cfg.BaseDelay

	for attempt := 0; ; attempt++ {
		out, err := call()
		if err == nil {
			return out, nil
		}
		// Fatal errors (400, auth) must not be retried — the `should_retry` gate.
		if !isRetryable(err) {
			return zero, err
		}
		// Out of attempts: surface the last error so the caller sees the cause.
		if attempt >= cfg.MaxRetries {
			return zero, fmt.Errorf("giving up after %d attempts: %w", attempt+1, err)
		}
		sleepFor := applyJitter(cfg, delay)
		select {
		case <-ctx.Done(): // a cancelled context shouldn't sleep
			return zero, ctx.Err()
		default:
		}
		cfg.sleep(sleepFor)

		delay *= 2 // exponential growth, capped
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}
}

// isRetryable: 429 + any 5xx are transient; everything else fails fast.
func isRetryable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.Code == 429 || se.Code >= 500
	}
	var te *transientError
	if errors.As(err, &te) {
		return true
	}
	return false
}
```

**Four non-obvious points**:

1. **`RetryProvider` composes because it satisfies the interface it wraps.** It implements `Provider` and holds a `Provider`. That's the decorator pattern, and it's why nothing above the boundary changes — you can wrap retries around the Anthropic client or the OpenAI-compatible one identically.
2. **Fail-fast on 4xx is a feature, not a gap.** A 400 means the request is malformed; retrying it just wastes the rate limit and delays the real error. The test `TestRetry_FailsFastOnNonRetryable` pins exactly one attempt and zero sleeps.
3. **`withRetry` is generic, so streaming could reuse it.** It's `func[T any]`, decoupled from `*CreateMessageResponse`. The unary path uses it today; the same backoff logic would wrap a streaming call without duplication.
4. **The SSE decoder is forgiving.** `accumulateStream` skips a malformed `data:` line rather than aborting the whole stream — a dropped delta is far better than a crash mid-reply. Many small `content_block_delta` events fold into one text block; `message_delta` carries the final `stop_reason` and output token count.

## What Changed (vs. s09) / 与 s09 的变化

s01–s09 all constructed `NewAnthropicProvider(key, model)` directly and called `CreateMessage`. s10 inserts a registry in front (name → config → provider) and a retry decorator behind it, while the `Provider` interface and the loop that uses it are untouched.

```diff
-// s01..s09: one concrete client, one endpoint, no resilience.
-p := NewAnthropicProvider(apiKey, "claude-sonnet-4-6")
-resp, err := p.CreateMessage(ctx, req)        // a 429 here just kills the run
+// s10: name → config → provider, then wrap it in retries.
+cfg, err := LookupModel("sonnet")             // alias → claude-sonnet-4-6 config
+base := buildProvider(cfg, apiKey)            // Anthropic OR OpenAI-compat
+var provider Provider = NewRetryProvider(base, DefaultRetryConfig())
+resp, err := provider.CreateMessage(ctx, req) // 429/5xx retried; 400 fails fast
+
+// CreateMessageRequest also gains a Stream field; AnthropicProvider gains
+// StreamMessage + accumulateStream to fold SSE deltas into one response.
```

Semantically: in s09 the provider was a fixed dependency — one model, no retries, no streaming. In s10 it becomes a *configured, decorated stack*. The model name selects a data row; resilience is a wrapper that composes over any backend; streaming is an optional capability discovered by type assertion. This is the seam Phase G's multi-model support extends — adding `editor-model` / `weak-model` is adding registry lookups, not new control flow.

## Try It / 动手试一试

```bash
cd agents/s10-provider-config-retries

# INSPECT (no network, no key): the whole registry, backends and default formats
go run . -list

# resolve an alias and call once; -v shows the resolved config + retry shape
export ANTHROPIC_API_KEY=sk-ant-...
go run . -v -model sonnet "say hi in one short sentence"

# stream the reply token-by-token (Anthropic backend)
go run . -model sonnet -stream "count to five"

# swap to an OpenAI-compatible backend — only the config row changes
export DEEPSEEK_API_KEY=sk-...
go run . -v -model deepseek "explain a goroutine in one line"

# unknown model → a typed error that lists the known names
go run . -model gpt-9-ultra "hi"

# tests (no network — lookup, backoff, SSE decode, translation are deterministic)
go test -v ./...
```

Expected output shape:

```
# go run . -v -model sonnet ...  (stderr trace + stdout reply):
[s10] model=claude-sonnet-4-6 backend=anthropic url=- ctx=200000 format=diff stream=false
[s10] retries on: base=125ms cap=32s maxRetries=6
[s10] stop_reason=end_turn in=14 out=9 tokens
Hi there, I'm online and ready to help!

# go run . -model gpt-9-ultra ...  (deterministic):
unknown model "gpt-9-ultra"; known: 4o, claude-haiku-4-5, claude-opus-4-7, ...
exit status 2
```

Switch `-model deepseek` and the same code path translates to OpenAI's wire format and back — the registry row changed, the loop did not. A full illustrative transcript lives in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s10-provider-config-retries/testdata/expected.txt).

## Upstream Source Reading / 上游源码阅读

aider's provider layer is litellm plus two methods on its `Model` class. `send_completion` (aider/models.py L985) builds the kwargs — model id, temperature, `extra_params`, tools — and makes the single `litellm.completion(**kwargs)` call that fans out to ~50 provider SDKs. `simple_send_with_retries` (L1039) wraps a non-streaming call in a `while True` loop with doubling backoff. The excerpt below is that retry loop — the direct ancestor of s10's `withRetry`.

```upstream:aider/models.py#L1039-L1082
def simple_send_with_retries(self, messages):
    from aider.exceptions import LiteLLMExceptions

    litellm_ex = LiteLLMExceptions()
    if "deepseek-reasoner" in self.name:
        messages = ensure_alternating_roles(messages)
    retry_delay = 0.125                      # s10: RetryConfig.BaseDelay (125ms)

    while True:
        try:
            kwargs = {"messages": messages, "functions": None, "stream": False}
            _hash, response = self.send_completion(**kwargs)
            if not response or not hasattr(response, "choices") or not response.choices:
                return None
            res = response.choices[0].message.content
            from aider.reasoning_tags import remove_reasoning_content
            return remove_reasoning_content(res, self.reasoning_tag)

        except litellm_ex.exceptions_tuple() as err:
            ex_info = litellm_ex.get_ex_info(err)
            print(str(err))
            if ex_info.description:
                print(ex_info.description)
            # THE retry gate — litellm's verdict on whether the error is transient.
            # s10's isRetryable() makes the same call from the HTTP status code.
            should_retry = ex_info.retry
            if should_retry:
                retry_delay *= 2             # s10: delay *= 2, capped at MaxDelay
                if retry_delay > RETRY_TIMEOUT:
                    should_retry = False     # give up once past the cap
            if not should_retry:
                return None
            print(f"Retrying in {retry_delay:.1f} seconds...")
            time.sleep(retry_delay)
            continue
        except AttributeError:
            return None
```

**Reading notes**:

- **The transient gate.** Upstream's `ex_info.retry` is litellm classifying a typed exception; s10's `isRetryable` reads `statusError.Code` (429/5xx). Same decision, different signal — we don't have litellm's exception taxonomy, so we use the raw HTTP status.
- **Backoff numbers match deliberately.** `retry_delay = 0.125` and `retry_delay *= 2` are ported verbatim into `DefaultRetryConfig` (`BaseDelay: 125ms`, `delay *= 2`). The cap `RETRY_TIMEOUT` becomes `MaxDelay`. We add jitter (upstream has none) because it's the standard fix for synchronized retries.
- **`send_completion` is the litellm boundary.** That one `litellm.completion(**kwargs)` call is what our entire `Provider` interface stands in for. `extra_params` merged into kwargs is exactly `ModelConfig.ExtraParams`.
- **Streaming diverges on purpose.** `ModelSettings.streaming` (L144) flips `stream=True`, and litellm yields *normalized* chunks the Coder reassembles. s10 decodes Anthropic's `content_block_delta` events *directly* in `accumulateStream` — concrete and self-contained, at the cost of not being provider-agnostic.
- **A piece we deliberately omit.** `aider/llm.py`'s `LazyLiteLLM` (L21-47) defers `import litellm` because it costs 1.5s at startup. Go has no such import cost, so s10 has no lazy wrapper — a real upstream concern that simply doesn't exist in our port.

**Read further**: start at `aider/models.py` → `ModelSettings` (L127) and `MODEL_ALIASES` (L99) for the registry, follow `send_completion` (L985) into `aider/llm.py` → `LazyLiteLLM` (L21) for the litellm boundary, then read `simple_send_with_retries` (L1039) for the retry loop above. That trace — config → call → retry — is the real-source map for s10, and the seam that the integration chapter wires together with every prior mechanism.

---

**Next**: s_full assembles s02–s10 into one `mini-aider` binary — the registry + retry stack from this chapter feeds the coder loop, repo map, git auto-commit, and reflection from the chapters before it.
