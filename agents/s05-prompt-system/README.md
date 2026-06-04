# s05 · Per-format prompt system / 按格式定制的提示系统

Why s02/s03/s04 actually work: the prompt teaches the model the format. / s02/s03/s04 为什么能跑通：是提示词在教模型用哪种格式。

s02..s04 each built a working edit-format **parser** (whole-file, SEARCH/REPLACE, unified diff). But a parser is useless unless the model *emits* that format — and it does so only because the **system prompt** told it to. s05 is the missing other half: a `Prompts` struct with one field per reusable **piece** of the system message (`MainSystem`, `ExampleMessages`, `SystemReminder`, plus lazy/overeager nudges), placeholders like `{fence}` / `{final_reminders}` filled at send time, and a renderer that assembles the final prompt for a chosen format. Switching edit format becomes changing a **data value**, not editing loop code.
s02..s04 各自实现了一个可用的编辑格式**解析器**（整文件、SEARCH/REPLACE、统一 diff）。但解析器毫无用处——除非模型真的*输出*那种格式，而模型之所以这么输出，是因为**系统提示词**这样要求它。s05 补上缺失的另一半：一个 `Prompts` 结构体，每个字段对应系统消息里一个可复用的**片段**（`MainSystem`、`ExampleMessages`、`SystemReminder`，外加 lazy/overeager 提醒），用 `{fence}` / `{final_reminders}` 这类占位符在发送时填充，再用一个渲染器为选定格式拼出最终提示词。切换编辑格式于是变成换一个**数据值**，而不是改循环代码。

## Run / 运行

```bash
cd agents/s05-prompt-system

# INSPECT (no network, no key): print the assembled system prompt for a format
# 查看模式（无网络、无 key）：打印某格式拼装好的系统提示词
go run . -format diff
go run . -format whole -examples     # also print the few-shot turns / 同时打印 few-shot
go run . -format udiff
go run . -list                       # list known formats / 列出已知格式

# RUN: render the prompt and actually call the LLM once
# 运行模式：渲染提示词并真正调用一次 LLM
export ANTHROPIC_API_KEY=sk-ant-...
go run . -format diff -file x.go -instruction "rename foo to bar"

# tests (no network) / 测试（无网络）
go test ./...
```

Supported `-provider` profiles: `anthropic` (native), `openai`, `deepseek`, `moonshot`, `qwen`, `groq`, `openrouter`, `local`. Override with `-model` / `-base-url`.
支持的 `-provider`：`anthropic`（原生）、`openai`、`deepseek`、`moonshot`、`qwen`、`groq`、`openrouter`、`local`。可用 `-model` / `-base-url` 覆盖。

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `prompts.go` | **The heart of the chapter.** The `Prompts` struct (one field per prompt piece), per-format prompt sets (`wholeFilePrompts` / `editBlockPrompts` / `uDiffPrompts`), `PromptsFor` selection, and `Render` / `RenderExamples` / `substitute`. **Start here.** / **本章核心。** 提示片段结构体、三种格式的提示集、选择表与渲染器。**从这里读起。** |
| `provider.go` | The generic LLM core (Anthropic wire shape) + two providers. Unchanged from s01..s04. / 通用 LLM 核心 + 两个 provider，与 s01..s04 一致。 |
| `main.go` | CLI: INSPECT a prompt (default) or RUN it against the LLM. Format is a `-flag`, not hardcoded. / 命令行：查看提示词或真正调用 LLM。格式是 `-flag`，不再写死。 |
| `prompts_test.go` | Tests: placeholder substitution, format selection, example messages, missing-var handling, format-swaps-the-string, nudge toggling. / 测试：占位符替换、格式选择、few-shot、缺失变量、切换格式、提醒开关。 |
| `testdata/expected.txt` | An illustrative transcript of inspect + run. / 一次 查看+运行 的示例记录。 |

## Key teaching points / 关键教学点

1. **Prompts are data, not code.** A coder owns a `Prompts` value (upstream `gpt_prompts`) instead of hardcoding strings. `Render(format, vars)` works for every format; only the value differs. That decoupling is the whole point. See [`prompts.go` `PromptsFor` / `Render`](./prompts.go).
   **提示词是数据，不是代码。** coder 持有一个 `Prompts` 值（上游 `gpt_prompts`），而不是写死字符串。`Render(format, vars)` 对所有格式通用，只有值不同。这种解耦就是本章的全部意义。

2. **Placeholders are filled at SEND time.** `{fence}` can't be baked in — aider switches to a different fence when the code itself contains ```` ``` ````. `{final_reminders}` (lazy/overeager nudges) is toggled per model. The renderer substitutes a KNOWN, small key set, leaving stray braces in example code (like `f"Hey {name}"`) untouched.
   **占位符在发送时才填。** `{fence}` 不能写死——当代码本身含 ```` ``` ```` 时 aider 会换围栏。`{final_reminders}`（lazy/overeager 提醒）按模型开关。渲染器只替换一个已知的小集合，从而保留示例代码里的散落花括号（如 `f"Hey {name}"`）。

3. **Few-shot examples teach the format better than prose.** They render as separate user/assistant turns (not part of the system string), so the model reads them as prior conversation. The editblock example literally shows a `<<<<<<< SEARCH` block; the wholefile one shows a full file listing.
   **few-shot 示例比文字规则更会教格式。** 它们渲染成独立的 user/assistant 轮次（不在系统串里），模型把它们当作之前的对话。editblock 示例直接展示一个 `<<<<<<< SEARCH` 块；wholefile 示例展示整文件清单。

4. **This explains s02..s04.** Each previous chapter's parser only works because a matching prompt elicited that exact format. s05 makes the dependency explicit and pluggable: add a new format = add one prompt set + one selection case.
   **这解释了 s02..s04。** 前面每一章的解析器之所以能用，是因为有一段配套提示词诱导出了那种格式。s05 把这层依赖显式化、可插拔：加一种新格式 = 加一个提示集 + 一个选择分支。

See the full chapter write-up: [`docs/en/s05-prompt-system.md`](../../docs/en/s05-prompt-system.md) · [`docs/zh/s05-prompt-system.md`](../../docs/zh/s05-prompt-system.md).
完整章节讲解见上面两个文档。
