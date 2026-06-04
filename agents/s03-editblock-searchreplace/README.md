# s03 · Search/replace edit blocks / 搜索/替换编辑块

Surgical edits instead of whole-file rewrites — the heart of aider. / 用外科手术式的局部编辑取代整文件重写——这是 aider 的核心。

s03 switches from s02's whole-file format to **SEARCH/REPLACE blocks**: the model emits only the lines to find and the lines to put in their place, and we locate-and-substitute them in the file. The catch is that LLMs rarely reproduce whitespace perfectly, so a byte-exact match fails on real code. The load-bearing trick is a **tiered fuzzy matcher** — exact match first, then a whitespace-flexible match that tolerates uniformly-shifted indentation — which is what makes SEARCH/REPLACE usable in practice.
s03 把 s02 的整文件格式换成 **SEARCH/REPLACE 编辑块**：模型只输出要查找的几行和要替换成的几行，我们在文件里定位并替换。难点在于 LLM 几乎不可能逐字节还原空白，所以精确匹配在真实代码上常常失败。真正吃重的技巧是**分层模糊匹配器**——先精确匹配，再做能容忍整体缩进偏移的"空白灵活"匹配——这才让 SEARCH/REPLACE 在实践中可用。

## Run / 运行

```bash
cd agents/s03-editblock-searchreplace

# set ONE provider's key / 设置任意一个 provider 的 key
export ANTHROPIC_API_KEY=sk-ant-...

# usage: s03 [flags] <file> <instruction>
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
| `provider.go` | The generic LLM core (Anthropic wire shape) + `AnthropicProvider` + an OpenAI-compatible provider. Unchanged from s01/s02. / 通用 LLM 核心 + 两个 provider，与 s01/s02 一致。 |
| `editblock.go` | **The heart of the chapter.** `GetEdits` parses SEARCH/REPLACE blocks; `ApplyEdits` locates each SEARCH via the tiered matcher (`replaceMostSimilarChunk`) and substitutes. **Start here.** / **本章核心。** 解析 + 分层匹配。**从这里读起。** |
| `main.go` | CLI + `Coder.Run` (read→prompt→call→parse→apply). / 命令行 + Coder 循环。 |
| `editblock_test.go` | Tests with a `fakeProvider`: parse one/many blocks, exact + whitespace-flexible apply, not-found, create-file, end-to-end. / 用 `fakeProvider` 的测试。 |
| `testdata/expected.txt` | An illustrative transcript of a real run. / 一次真实运行的示例记录。 |

## Key teaching points / 关键教学点

1. **SEARCH/REPLACE = surgical edits.** The model reproduces only the lines that change (find + replace), not the whole file. Far cheaper in tokens, far less likely to clobber unrelated code. This is aider's default `diff` format. See [`editblock.go` `GetEdits`](./editblock.go).
   **SEARCH/REPLACE = 外科手术式编辑。** 模型只复述要改的几行，更省 token、更不会误伤无关代码。这是 aider 默认的 `diff` 格式。

2. **Fuzzy matching is the load-bearing trick.** The model generates the SEARCH text from memory, so it re-indents and normalizes whitespace. `replaceMostSimilarChunk` tries tiers: exact → whitespace-flexible (uniformly-shifted indent) → drop-spurious-blank → `...` elision. Without this, a huge fraction of correct edits would fail to apply.
   **模糊匹配是吃重的关键。** 模型凭记忆生成 SEARCH，会重新缩进、归一化空白。`replaceMostSimilarChunk` 逐层尝试：精确 → 空白灵活 → 丢多余空行 → `...` 省略。没有它，大量正确的编辑都会贴不上去。

3. **Failures are soft, not fatal.** An unmatched SEARCH leaves the file untouched and lands the edit in `failed`. Upstream turns this into a reflected message asking the model to retry (our s09); s03 reports it and exits non-zero.
   **失败是软失败。** SEARCH 匹配不上就保持文件不动，把该编辑放进 `failed`。上游会据此让模型重试（我们的 s09）；s03 只报告并非零退出。

4. **Empty SEARCH = create / append.** A block with no SEARCH text means "create this file" (or append), mirroring upstream `do_replace`. First chapter where `Edit.Search` is non-empty for normal edits.
   **空 SEARCH = 创建/追加。** 没有 SEARCH 文本表示"新建该文件"（或追加），对应上游 `do_replace`。这也是 `Edit.Search` 首次在常规编辑中非空的一章。

See the full chapter write-up: [`docs/en/s03-editblock-searchreplace.md`](../../docs/en/s03-editblock-searchreplace.md) · [`docs/zh/s03-editblock-searchreplace.md`](../../docs/zh/s03-editblock-searchreplace.md).
完整章节讲解见上面两个文档。
