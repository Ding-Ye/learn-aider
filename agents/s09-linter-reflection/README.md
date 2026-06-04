# s09 · Linter + reflection loop / 检查器与反思循环

The loop stops being one-shot: apply the edit, lint it, and if it's broken, hand the errors back to the model and let it try again. / 循环不再是一次性的：应用编辑后做检查，如果坏了就把错误交回给模型，让它重试。

s01..s08 ended at "apply the edit" — whatever the model produced landed on disk and the turn was over. But models sometimes emit code that doesn't compile. s09 adds the step that makes aider a *pair programmer* rather than a one-shot generator: after applying edits it runs a **Linter** over the changed files, and a non-clean result becomes a synthetic user message ("# Fix any errors below…" + the linter output) that re-enters the loop. We stop the instant the lint is clean, or when we hit a small **reflection cap** — because a model that keeps producing broken code must not loop forever. This is the first time the control flow becomes multi-turn beyond a single send.
s01..s08 到"应用编辑"就结束了——模型产出什么就落盘什么，这一轮就完了。但模型有时会写出编译不过的代码。s09 补上让 aider 成为*结对程序员*而非一次性生成器的那一步：应用编辑后，对改动的文件跑一个**检查器（Linter）**，不干净的结果会变成一条合成的用户消息（"# Fix any errors below…" + 检查器输出）重新进入循环。一旦检查通过就停，或者到达一个小的**反思上限**就停——因为一个不断产出坏代码的模型绝不能无限循环下去。这是控制流第一次超出单次发送、变成多轮。

## Run / 运行

```bash
cd agents/s09-linter-reflection

# LINT mode (no network, no key): print exactly what the loop would reflect back.
# 检查模式（无网络、无 key）：打印循环将要反思回去的那段文本。
go run . -lint broken.go
go run . -lint provider.go            # a clean file => "nothing to reflect" / 干净文件
go run . -lint x.go -lint-cmd "go vet"  # override the checker / 覆盖检查命令

# RUN mode: edit a file via the LLM, then lint+reflect until clean or the cap.
# 运行模式：用 LLM 改文件，然后检查+反思，直到干净或到达上限。
export ANTHROPIC_API_KEY=sk-ant-...
go run . -v -file calc.go -instruction "add a Divide(a, b int) int function"
go run . -file calc.go -instruction "..." -max-reflections 2

# tests (no network) / 测试（无网络）
go test ./...
```

Supported `-provider` profiles: `anthropic` (native), `openai`, `deepseek`, `moonshot`, `qwen`, `groq`, `openrouter`, `local`. Override with `-model` / `-base-url`.
支持的 `-provider`：`anthropic`（原生）、`openai`、`deepseek`、`moonshot`、`qwen`、`groq`、`openrouter`、`local`。可用 `-model` / `-base-url` 覆盖。

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `linter.go` | **Half the chapter.** The `Linter`: a per-extension command map (`.go` -> `gofmt -l -e`), an all-languages override, a built-in syntax check, and `Lint(paths) -> (errText, ok)` that labels failures the way the model expects. Port of `aider/linter.py`. **Start here.** / **本章一半。** 检查器：按扩展名的命令表、全语言覆盖、内置检查，以及把失败标注成模型期望格式的 `Lint`。**从这里读起。** |
| `reflect.go` | **The other half.** The `ReflectLoop`: send -> apply -> lint -> if broken and under the cap, the lint text becomes the next user message and we re-run. Direct transcription of upstream `run_one` L924-944. / **另一半。** 反思循环：发送→应用→检查→若坏且未到上限，检查文本变成下一条用户消息并重跑。 |
| `coder.go` | A tiny whole-file `Coder` so the loop has a real `GetEdits`/`ApplyEdits` to drive. NOT the lesson (that was s02..s05). / 一个极小的整文件 Coder，给循环一个真实的解析/应用对象。不是本章重点。 |
| `provider.go` | The generic LLM core (Anthropic wire shape) + two providers. Unchanged from s01..s08. / 通用 LLM 核心 + 两个 provider，与 s01..s08 一致。 |
| `main.go` | CLI: LINT a file (default) or RUN the reflection loop against the LLM. / 命令行：检查文件或对 LLM 跑反思循环。 |
| `linter_test.go` | Linter tests: clean/broken, non-zero-without-output, all-cmd override, unknown extension skipped, line-number scraping. / 检查器测试。 |
| `reflect_test.go` | Loop tests with a `fakeProvider` + scripted linter: clean first try (0 reflections), one error then fixed (1 reflection), cap reached stops, lint text forwarded to the provider, the clean path writes the file. / 循环测试，含假 provider 与脚本化检查器。 |
| `testdata/expected.txt` | An illustrative transcript of LINT + RUN. / 一次 检查+运行 的示例记录。 |

## Key teaching points / 关键教学点

1. **One assignment turns a one-shot call into a feedback loop.** `message = lintText` is the whole trick: the linter's report becomes the next user turn, so the model sees its own broken output followed by the failure — exactly like a human pasting a compiler error back into the chat. See [`reflect.go` `Run`](./reflect.go).
   **一个赋值把一次性调用变成反馈循环。** `message = lintText` 就是全部诀窍：检查器的报告变成下一条用户消息，模型于是看到自己的坏输出后面跟着失败信息——就像人把编译错误粘回对话里。

2. **The cap is not optional.** Without `MaxReflections` a model that keeps emitting broken code loops forever. Upstream caps at 3; we default to the same and stop with a warning rather than spin. Better to hand a still-broken file to a human than burn tokens indefinitely.
   **上限不是可选项。** 没有 `MaxReflections`，不断产出坏代码的模型会无限循环。上游上限是 3，我们默认相同，到顶就带警告停下而不是空转。把仍然坏的文件交给人，好过无限烧 token。

3. **A linter is "run a command, capture its output, label it."** That's all `Lint` does: pick a checker per extension (or a built-in `gofmt -e`), run it, and on a non-zero exit wrap the output under the `# Fix any errors below` header. The 40-language tree-sitter walk and flake8 code-selection upstream does are quality-of-life, not the load-bearing idea.
   **检查器就是"跑一条命令、抓它的输出、打上标签"。** `Lint` 做的全部就是这个：按扩展名选检查器（或内置 `gofmt -e`），跑它，非零退出就把输出包进 `# Fix any errors below` 头。上游的 40 语言 tree-sitter 遍历和 flake8 选码是锦上添花，不是承重点。

4. **The loop is format- and provider-agnostic.** `reflect.go` never names a provider or an edit format; it talks to a `Coder` and a `SendFunc`. Swap the whole-file coder for s03's SEARCH/REPLACE coder and the loop is unchanged — the reflection mechanism is orthogonal to *how* edits are expressed.
   **循环与格式、provider 无关。** `reflect.go` 从不提及具体 provider 或编辑格式；它只跟 `Coder` 和 `SendFunc` 打交道。把整文件 coder 换成 s03 的 SEARCH/REPLACE coder，循环不变——反思机制与编辑*如何表达*正交。

See the full chapter write-up: [`docs/en/s09-linter-reflection.md`](../../docs/en/s09-linter-reflection.md) · [`docs/zh/s09-linter-reflection.md`](../../docs/zh/s09-linter-reflection.md).
完整章节讲解见上面两个文档。
