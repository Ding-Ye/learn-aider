# s04 · Unified diff edit format / 统一 diff 编辑格式

A third edit format: standard `git diff` hunks, applied by context — not by line number. / 第三种编辑格式：标准的 `git diff` hunk，靠上下文（而非行号）来定位应用。

s04 adds a **unified-diff** coder alongside s02's whole-file and s03's SEARCH/REPLACE. Some models would rather emit a ```diff block (`--- / +++ / @@` with `-`/`+` lines) than aider's bespoke fences. We parse the hunks, derive each hunk's before-text (context + `-` lines) and after-text (context + `+` lines), and then reuse **s03's fuzzy matcher** to locate the before-text in the file and splice the after-text. The crucial choice: we **ignore the `@@ -a,b +c,d @@` line numbers** — LLMs compute them from memory and get them wrong constantly — and locate the hunk by its **context lines** instead, with the same whitespace flexibility as s03.
s04 在 s02 的整文件、s03 的 SEARCH/REPLACE 之外，再加一个 **统一 diff** coder。有些模型更愿意输出 ```diff 块（`--- / +++ / @@` 加 `-`/`+` 行），而非 aider 自创的围栏。我们解析这些 hunk，推导出每个 hunk 的 before 文本（上下文 + `-` 行）与 after 文本（上下文 + `+` 行），再复用 **s03 的模糊匹配器** 在文件里定位 before 文本、把 after 文本拼接进去。关键抉择：我们**忽略 `@@ -a,b +c,d @@` 行号**——LLM 凭记忆算行号，几乎总会算错——改用 hunk 的**上下文行**定位，并沿用 s03 的空白灵活性。

## Run / 运行

```bash
cd agents/s04-unified-diff-format

# set ONE provider's key / 设置任意一个 provider 的 key
export ANTHROPIC_API_KEY=sk-ant-...

# usage: s04 [flags] <file> <instruction>
go run . greet.go "change the greeting to 'hi there'"

# -v shows the request/response shape on stderr / -v 在 stderr 打印请求/响应形态
go run . -v greet.go "add error handling to readConfig"

# -root resolves parsed paths under a directory / -root 指定解析路径的根目录
go run . -root /tmp greet.go "rename foo to bar"

# tests (no network) / 测试（无网络）
go test ./...
```

Supported `-provider` profiles: `anthropic` (native), `openai`, `deepseek`, `moonshot`, `qwen`, `groq`, `openrouter`, `local`. Override with `-model` / `-base-url`.
支持的 `-provider`：`anthropic`（原生）、`openai`、`deepseek`、`moonshot`、`qwen`、`groq`、`openrouter`、`local`。可用 `-model` / `-base-url` 覆盖。

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `provider.go` | The generic LLM core (Anthropic wire shape) + `AnthropicProvider` + an OpenAI-compatible provider. Unchanged from s01–s03. / 通用 LLM 核心 + 两个 provider，与 s01–s03 一致。 |
| `udiff.go` | **The heart of the chapter.** `GetEdits`/`findDiffs` parse ```diff hunks; `hunkToBeforeAfter` derives before/after text; `ApplyEdits` locates each hunk by its context via the s03 matcher (`replaceMostSimilarChunk`) and splices. **Start here.** / **本章核心。** 解析 hunk + 推导 before/after + 上下文定位。**从这里读起。** |
| `main.go` | CLI + `Coder.Run` (read→prompt→call→parse→apply). / 命令行 + Coder 循环。 |
| `udiff_test.go` | Tests with a `fakeProvider`: parse a hunk, apply add/remove lines, context-not-found, whitespace-flexible context, multi-hunk order, create-file, end-to-end. / 用 `fakeProvider` 的测试。 |
| `testdata/expected.txt` | An illustrative transcript of a real run. / 一次真实运行的示例记录。 |

## Key teaching points / 关键教学点

1. **Unified diff = another edit format, diff-shaped.** Same loop as s02/s03; only the parser and the way `before`/`after` are derived change. The model emits a `git diff` instead of SEARCH/REPLACE fences. This is aider's `udiff` format. See [`udiff.go` `findDiffs`](./udiff.go).
   **统一 diff = 又一种编辑格式，长得像 diff。** 与 s02/s03 同一个循环，只换了解析器和 before/after 的推导方式。模型输出 `git diff` 而非 SEARCH/REPLACE 围栏。这是 aider 的 `udiff` 格式。

2. **A hunk reduces to s03's problem.** `hunkToBeforeAfter` turns ` `/`-`/`+` lines into (before, after): context lines go to both sides, `-` lines only to before, `+` lines only to after. From there it IS search/replace — so we reuse s03's tiered matcher untouched.
   **一个 hunk 退化成 s03 的问题。** `hunkToBeforeAfter` 把 ` `/`-`/`+` 行变成 (before, after)：上下文行进两边，`-` 行只进 before，`+` 行只进 after。到这一步就是搜索/替换——于是 s03 的分层匹配器原样复用。

3. **Locate by context, ignore the @@ numbers.** The `@@ -a,b +c,d @@` header is the most error-prone part of a model-generated diff (it must count lines exactly). The context lines are reliable because the model quotes real code. So we anchor on context — with whitespace flexibility, since the model re-indents what it quotes. This is aider's udiff flexibility, far more forgiving than `patch(1)`.
   **靠上下文定位，忽略 @@ 行号。** `@@ -a,b +c,d @@` 头是模型生成 diff 里最易错的部分（要精确数行）。上下文行是可靠的，因为模型在引用真实代码。所以我们锚定上下文——并保留空白灵活性，因为模型引用时会重新缩进。这就是 aider 的 udiff 灵活性，比 `patch(1)` 宽容得多。

4. **Pure-context hunks are dropped; failures are soft.** A hunk with no `-`/`+` changes nothing and is discarded. A hunk whose context can't be located leaves the file untouched and lands in `failed` (upstream reflects this back to the model; our s09).
   **纯上下文 hunk 被丢弃；失败是软失败。** 没有 `-`/`+` 的 hunk 什么都不改，直接丢掉。上下文定位不到的 hunk 保持文件不动并进入 `failed`（上游会反射给模型，即我们的 s09）。

See the full chapter write-up: [`docs/en/s04-unified-diff-format.md`](../../docs/en/s04-unified-diff-format.md) · [`docs/zh/s04-unified-diff-format.md`](../../docs/zh/s04-unified-diff-format.md).
完整章节讲解见上面两个文档。
