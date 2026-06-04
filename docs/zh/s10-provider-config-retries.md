---
title: "s10 · 模型/提供方抽象与重试流式"
chapter: 10
slug: s10-provider-config-retries
est_read_min: 16
---

# s10 · 模型/提供方抽象与重试流式

> 教什么：*模型/提供方配置层*——aider 的 litellm 式抽象。一个 `Provider` 接口藏起众多后端（Anthropic 原生 + 所有 OpenAI 兼容 API），一张 `ModelConfig` 注册表把名字映射到 `{后端, baseURL, 上下文窗口, 默认格式}`，一个 `RetryProvider` 装饰器用指数退避扛住 429/5xx，一个 SSE 解码器把 token 实时流出。这是架构的收官之作：s01 那个唯一的具体客户端，变成一摞可配置、可恢复、可流式的栈。

---

## Problem / 问题

从 s01 到 s09，每一章都依赖一个写死的 `AnthropicProvider`，指向一个固定端点。教学够用，但真要跑起来就立刻崩。用户想用*不同的模型*——难的编辑用 Claude，提交信息用便宜的本地模型，或者就因为手里的 key 是 DeepSeek、Qwen 的。真实端点会不断返回 **429 限流**和 **5xx 过载**，一遇到瞬时抖动就死掉的工具没法用。而一条长回复生成时干等 30 秒、屏幕毫无动静，体验上就像坏了——用户期待 token **流式**出现。

笨办法只会更糟：在循环里穿一个对模型名的 `switch`，在每个调用点复制粘贴一个裸 `for` 重试循环，再把流式当成事后补丁硬塞进去。aider 最具特色的那一层——litellm 包装——存在的意义正是为了避开这摊乱麻。本章用 Go 重建它的承重内核：一张注册表、一个重试装饰器、一个流式解码器，全都藏在 s01 那个*原封不动*的 `Provider` 接口之后。

## Solution / 解决方案

把 provider 栈看成**围绕一个接口的三块可组合零件**，而绝不是循环里的分支。(1) **配置即数据。** `ModelConfig` 注册表把名字（以及 `sonnet` 这类别名）映射到一行设置；`buildProvider` 是唯一知道具体 provider 类型的地方。加后端就是加一行。(2) **重试是装饰器。** `RetryProvider` 既*是* `Provider` 又*包* `Provider`，于是天然可组合：注册表造出具体客户端，重试把它包起来，上层循环只看到一个 `Provider`。(3) **流式是可选的第二接口。** 能流式的 provider 实现 `StreamingProvider`；调用方对它做类型断言，断言不中就退回一次性调用。

三个决策撑起整个设计：

1. **重试判断只有"瞬时 vs 致命"，别无其他。** 重试 429 + 5xx；其余 4xx 快速失败。为一个永远不会成功的请求（比如 400）耗掉限流额度，比当场失败更糟。我们从带类型的 `statusError.Code` 读出这个判断；上游从 litellm 的异常分类里读。
2. **退避翻倍并封顶，和上游完全一致。** `delay` 从 125ms 起，每次重试翻倍，在 `MaxDelay` 处夹住，并可加抖动避免惊群——几乎是 `simple_send_with_retries` 里 `retry_delay *= 2` 的直接移植。
3. **我们直接解码 Anthropic SSE。** 上游通过 litellm 把各家 provider 的分块归一化；我们自己把 Anthropic 的 `content_block_delta` 事件折叠成一个 `CreateMessageResponse`。这是有意的差异（见下文），目的是让课程更具体。

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

重试内核——退避、封顶、瞬时/致命的判断闸（节选自 [`agents/s10-provider-config-retries/retry.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s10-provider-config-retries/retry.go)）：

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

**四个非显然之处**：

1. **`RetryProvider` 能组合，因为它满足了自己所包裹的接口。** 它实现 `Provider`，又持有一个 `Provider`。这就是装饰器模式，也正是边界之上一行不用改的原因——你可以用同一套方式给 Anthropic 客户端或 OpenAI 兼容客户端包上重试。
2. **对 4xx 快速失败是特性，不是缺陷。** 400 意味着请求本身就不对，重试它只会浪费限流额度并拖延真正的错误。测试 `TestRetry_FailsFastOnNonRetryable` 钉死了"只尝试一次、零次 sleep"。
3. **`withRetry` 是泛型的，所以流式也能复用它。** 它是 `func[T any]`，与 `*CreateMessageResponse` 解耦。今天一次性路径用它；同一套退避逻辑也能无重复地包住一次流式调用。
4. **SSE 解码器是宽容的。** `accumulateStream` 会跳过一行格式坏掉的 `data:`，而不是把整条流中止——丢一个增量远好过回复中途崩溃。许多个小的 `content_block_delta` 事件折叠成一个文本块；`message_delta` 携带最终的 `stop_reason` 和输出 token 数。

## What Changed (vs. s09) / 与 s09 的变化

s01–s09 全都直接构造 `NewAnthropicProvider(key, model)` 再调 `CreateMessage`。s10 在它前面插入一张注册表（名字 → 配置 → provider），在它后面插入一个重试装饰器，而 `Provider` 接口和用它的循环纹丝不动。

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

语义上：在 s09，provider 是一个固定依赖——一个模型、无重试、无流式。在 s10，它变成一摞*被配置、被装饰的栈*。模型名选中一行数据；韧性是一个能套在任意后端上的包装器；流式是一种靠类型断言发现的可选能力。这正是 Phase G 多模型支持要延伸的接缝——加 `editor-model` / `weak-model` 就是加注册表查找，而不是新增控制流。

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

期望输出形态：

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

把 `-model deepseek` 换上，同一条代码路径会翻译成 OpenAI 的线格式再翻回来——换的是注册表那一行，循环没动。完整的示例记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s10-provider-config-retries/testdata/expected.txt)。

## Upstream Source Reading / 上游源码阅读

aider 的 provider 层是 litellm 加上 `Model` 类上的两个方法。`send_completion`（aider/models.py L985）拼装 kwargs——模型 id、temperature、`extra_params`、tools——并发出那一次扇出到约 50 家 provider SDK 的 `litellm.completion(**kwargs)` 调用。`simple_send_with_retries`（L1039）把一次非流式调用裹进一个带翻倍退避的 `while True` 循环。下面节选的就是这个重试循环——s10 `withRetry` 的直系祖先。

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

**对照阅读要点**：

- **瞬时判断闸。** 上游的 `ex_info.retry` 是 litellm 在对一个带类型的异常分类；s10 的 `isRetryable` 从 `statusError.Code`（429/5xx）读。同一个决定，不同的信号——我们没有 litellm 的异常分类体系，所以用原始 HTTP 状态码。
- **退避数字是刻意对齐的。** `retry_delay = 0.125` 和 `retry_delay *= 2` 被逐字移植进 `DefaultRetryConfig`（`BaseDelay: 125ms`、`delay *= 2`）。封顶 `RETRY_TIMEOUT` 变成 `MaxDelay`。我们加了抖动（上游没有），因为这是对付同步重试的标准做法。
- **`send_completion` 就是 litellm 边界。** 那一行 `litellm.completion(**kwargs)` 就是我们整个 `Provider` 接口所替代的东西。并入 kwargs 的 `extra_params` 正是 `ModelConfig.ExtraParams`。
- **流式是有意分岔的。** `ModelSettings.streaming`（L144）把 `stream=True` 打开，litellm 吐出 Coder 再去重组的*归一化*分块。s10 在 `accumulateStream` 里*直接*解码 Anthropic 的 `content_block_delta` 事件——具体且自包含，代价是不再 provider 无关。
- **一处我们故意省略的。** `aider/llm.py` 的 `LazyLiteLLM`（L21-47）延迟 `import litellm`，因为它启动时要花 1.5 秒。Go 没有这种导入开销，所以 s10 没有惰性包装器——一个真实的上游关切，在我们的移植里根本不存在。

**想读更多**：从 `aider/models.py` 的 `ModelSettings`（L127）和 `MODEL_ALIASES`（L99）入手看注册表，跟着 `send_completion`（L985）进 `aider/llm.py` 的 `LazyLiteLLM`（L21）看 litellm 边界，最后读上面的 `simple_send_with_retries`（L1039）看重试循环。这条线——配置 → 调用 → 重试——就是 s10 的真实代码地图，也是整合章节把前面所有机制串起来的那条接缝。

---

**下一节预告**：s_full 把 s02–s10 装配成一个 `mini-aider` 二进制——本章的注册表 + 重试栈，喂给前面各章的 coder 循环、repo map、git 自动提交和反思。
