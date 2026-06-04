# s10 · Model/provider config + retries/streaming / 模型/提供方抽象与重试流式

aider's most architecturally distinctive layer: the litellm-style abstraction that hides many backends behind one `Provider`, plus a model registry, resilient retries, and SSE streaming. / aider 最具架构特色的一层：用一个 `Provider` 把众多后端藏在身后的 litellm 式抽象，外加模型注册表、带退避的重试、以及 SSE 流式。

s10 keeps the `Provider` interface unchanged and turns the single concrete client of s01 into a **configurable, resilient, streaming stack**: a `ModelConfig` registry maps a name (`sonnet`, `deepseek`) to `{backend, baseURL, contextWindow, defaultFormat}`; `RetryProvider` wraps any provider with exponential backoff on 429/5xx; the Anthropic provider learns to decode a server-sent-events stream. Nothing above the provider boundary changes.
s10 保持 `Provider` 接口不变，把 s01 那个唯一的具体客户端变成**可配置、可恢复、可流式**的一摞：`ModelConfig` 注册表把名字（`sonnet`、`deepseek`）映射到 `{后端, baseURL, 上下文窗口, 默认格式}`；`RetryProvider` 给任意 provider 包上对 429/5xx 的指数退避；Anthropic provider 学会解码 SSE 流。provider 边界以上一行不改。

## Run / 运行

```bash
cd agents/s10-provider-config-retries

# list the registry: every model id + alias, its backend and default format
# 列出注册表：所有模型 id + 别名、后端、默认格式
go run . -list

# resolve an alias and call once (set the matching key first)
# 解析别名并调用一次（先设置对应的 key）
export ANTHROPIC_API_KEY=sk-ant-...
go run . -model sonnet "say hi in one short sentence"

# stream the reply token-by-token (Anthropic backend)
# 流式逐 token 输出（Anthropic 后端）
go run . -model sonnet -stream "count to five"

# swap to an OpenAI-compatible backend — only the config row changes
# 切到 OpenAI 兼容后端——只换注册表里那一行
export DEEPSEEK_API_KEY=sk-...
go run . -v -model deepseek "explain a goroutine in one line"

# tests (no network) / 测试（无网络）
go test ./...
```

`-model` accepts an alias (`sonnet`, `opus`, `haiku`, `deepseek`, `r1`, `qwen`, `kimi`, `groq`, `4o`...) or a canonical id (`claude-sonnet-4-6`, `gpt-4o`, `deepseek-chat`...). `-v` prints the resolved config + retry shape; `-no-retry` removes the decorator.
`-model` 接受别名或规范 id。`-v` 打印解析后的配置 + 重试形态；`-no-retry` 去掉装饰器。

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `models.go` | `ModelConfig` + the registry + `LookupModel` (alias → canonical → config), default edit format & param defaults per model. **Start here.** / 注册表与查找。**从这里读起。** |
| `retry.go` | `withRetry` + `RetryProvider`: exponential backoff + jitter, retry on 429/5xx, fail fast on 4xx, give up after a cap. / 重试装饰器。 |
| `provider.go` | Shared LLM core (Anthropic wire shape) + `AnthropicProvider` + the SSE stream decoder (`accumulateStream`). / 通用核心 + Anthropic provider + 流式解码。 |
| `provider_openai.go` | `OpenAIProvider` for any OpenAI-compatible API; two-way wire translation. / OpenAI 兼容 provider，双向翻译。 |
| `main.go` | CLI: `-model` selects a config, builds + wraps a provider, unary or streaming send. / 命令行。 |
| `models_test.go` | Alias/canonical lookup, per-model defaults, unknown-model error. / 查找与默认值测试。 |
| `retry_test.go` | Flaky fake provider: recover after N, cap backoff, fail fast on 400, jitter bounds. / 重试测试。 |
| `provider_openai_test.go` | OpenAI ↔ Anthropic round-trip, Anthropic request encode, SSE decode. / 翻译/编码/流式测试。 |
| `testdata/expected.txt` | An illustrative transcript of a real run. / 一次真实运行的示例记录。 |

## Key teaching points / 关键教学点

1. **Configuration is data, not code.** Adding a backend is adding a `ModelConfig` row; `buildProvider` is the only `switch` on backend, and the loop above the `Provider` interface never branches on the model name. This is upstream's `ModelSettings` + `MODEL_ALIASES`, shrunk from 1300 lines to a readable map.
   **配置即数据。** 加一个后端就是加一行；`buildProvider` 是唯一对后端的 `switch`，接口之上的循环从不按模型名分支。

2. **Retries are a decorator.** `RetryProvider` *is* a `Provider` and wraps a `Provider`, so it composes: registry builds the concrete client, retry wraps it, the loop sees just a `Provider`. Backoff doubles and caps exactly like upstream `simple_send_with_retries` (`retry_delay *= 2`, stop past the cap).
   **重试是装饰器。** `RetryProvider` 既是又包 `Provider`，因此可叠加；退避翻倍并封顶，和上游一致。

3. **Transient vs fatal is the whole retry decision.** We retry 429 + 5xx, fail fast on other 4xx — burning the rate limit on a request that can never succeed is worse than failing now. Upstream reads this from litellm's exception taxonomy; we read the HTTP status code via a typed `statusError`.
   **可重试 vs 致命是重试的全部判断。** 重试 429/5xx，其余 4xx 快速失败。

4. **Streaming folds many deltas into one response.** `accumulateStream` reads Anthropic's `content_block_delta` events and rebuilds a single `CreateMessageResponse`, calling `onText` live. We decode Anthropic's SSE directly rather than litellm's normalized chunks — a deliberate divergence.
   **流式把许多增量折叠成一个响应。** 我们直接解码 Anthropic 的 SSE，而非 litellm 的归一化分块——这是有意的差异。

See the full chapter write-up: [`docs/en/s10-provider-config-retries.md`](../../docs/en/s10-provider-config-retries.md) · [`docs/zh/s10-provider-config-retries.md`](../../docs/zh/s10-provider-config-retries.md).
完整章节讲解见上面两个文档。
