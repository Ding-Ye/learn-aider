# s01 · The minimum coder loop / 最小 Coder 循环

The smallest program that lets an LLM change a file on disk. / 让 LLM 修改磁盘上一个文件的最小程序。

s01 strips [aider](https://github.com/Aider-AI/aider) to its spine: **one instruction → one LLM call → one applied edit**, using aider's simplest edit format (`whole` — return the entire rewritten file in a fenced block). No git, no repo map, no reflection — those are later chapters.
s01 把 aider 砍到只剩骨架：**一条指令 → 一次 LLM 调用 → 一次落盘编辑**，用的是 aider 最简单的编辑格式（`whole`，即在围栏代码块里返回整个重写后的文件）。没有 git、没有 repo map、没有反思——那些在后面的章节。

## Run / 运行

```bash
cd agents/s01-minimum-coder-loop

# set ONE provider's key / 设置任意一个 provider 的 key
export ANTHROPIC_API_KEY=sk-ant-...

# usage: s01 [flags] <file> <instruction>
go run . hello.go "add a doc comment to main"

# -v shows the request/response shape on stderr / -v 在 stderr 打印请求/响应形态
go run . -v hello.go "rename foo to bar"

# swap providers — only the transport changes / 切换 provider——只换传输层
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek hello.go "fix the typo"

# tests (no network) / 测试（无网络）
go test ./...
```

Supported `-provider` profiles: `anthropic` (native), `openai`, `deepseek`, `moonshot`, `qwen`, `groq`, `openrouter`, `local`. Override with `-model` / `-base-url`.
支持的 `-provider`：`anthropic`（原生）、`openai`、`deepseek`、`moonshot`、`qwen`、`groq`、`openrouter`、`local`。可用 `-model` / `-base-url` 覆盖。

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `provider.go` | The generic LLM core (Anthropic wire shape) + `AnthropicProvider`. / 通用 LLM 核心（Anthropic 线格式）+ `AnthropicProvider`。 |
| `provider_openai.go` | `OpenAIProvider` for any OpenAI-compatible API; translates to/from the Anthropic-shaped types. / 适配任意 OpenAI 兼容 API，双向翻译。 |
| `coder.go` | The `Coder` loop: `Run` (read→prompt→call→extract→write), `extractCodeBlock`, `applyWholeFile`. **Start here.** / Coder 循环。**从这里读起。** |
| `main.go` | CLI: provider profiles, flags, wires `Coder.Run`. / 命令行：profile、flag、串起 `Coder.Run`。 |
| `coder_test.go` | Tests with a `fakeProvider`: parse, write, end-to-end, request JSON. / 用 `fakeProvider` 的测试。 |
| `provider_openai_test.go` | OpenAI ↔ Anthropic translation round-trip. / OpenAI ↔ Anthropic 翻译往返。 |
| `testdata/expected.txt` | An illustrative transcript of a real run. / 一次真实运行的示例记录。 |

## Key teaching points / 关键教学点

1. **The loop is five beats.** `read → build prompt → Provider.CreateMessage → extract one edit → write`. Upstream `Coder.run → run_one → send_message → apply_updates` is the same five beats plus reflection, streaming, token budgeting, and git. See [coder.go `Run`](./coder.go).
   **循环就是五拍。** 上游那一大坨 = 这五拍 + 反思 + 流式 + token 预算 + git。

2. **Edit format = a prompting strategy.** We never let the model call a tool. We *steer* it (via the system prompt) to reply with one fenced block, then parse that block. This is aider's `whole` format and the lowest-common-denominator output every model can produce. `EditFileTool()` is defined but intentionally unused — it shows where tool-calling would plug in later.
   **编辑格式 = 一种提示策略。** 我们不让模型调用工具，而是用系统提示"引导"它返回一个围栏块再解析。这就是 aider 的 `whole` 格式，也是所有模型都能产出的最低公分母。

3. **Why whole-file is the baseline.** Applying the edit is a single `os.WriteFile` — no matching, no merging. All the intelligence lives in the parser. That's why aider (and we) start here before SEARCH/REPLACE (s03) and unified diff (s04).
   **为什么整文件是基线。** 落盘就是一次 `os.WriteFile`——不匹配、不合并。聪明之处全在解析器。所以从这里起步，再到 s03/s04。

4. **`Provider` is an interface from day one.** The loop never names a concrete client, so tests inject a `fakeProvider` and s10 can later wrap retries + streaming behind the same interface.
   **`Provider` 从第一天就是接口。** 循环不依赖具体实现，因此测试可注入 `fakeProvider`，s10 也能在同一接口后加重试与流式。

See the full chapter write-up: [`docs/en/s01-minimum-coder-loop.md`](../../docs/en/s01-minimum-coder-loop.md) · [`docs/zh/s01-minimum-coder-loop.md`](../../docs/zh/s01-minimum-coder-loop.md).
完整章节讲解见上面两个文档。
