# s02 · Whole-file edit format / 整文件编辑格式

Aider's simplest edit format, done for real: the LLM returns the **complete rewritten file(s)**, each as a fenced block with its path on the line above. / aider 最简单编辑格式的"正式版"：LLM 返回**完整重写后的文件**，每个文件一个围栏块，路径写在围栏上一行。

s01 parsed exactly one block and already knew the path (from argv) — a toy. s02 promotes that inline parser into a reusable `WholeFileCoder` that satisfies the curriculum's `Coder` interface and does what the real format requires: parse **multiple** files from one reply, recover the **filename above each fence**, clean the markdown the model wraps it in (`**foo.go**`, `` `foo.go` ``, `# foo.go`, `foo.go:`), and fall back sensibly when a fence has no name.
s01 只解析一个块、且路径已知（来自命令行参数）——那是玩具。s02 把内联解析器提升为可复用的 `WholeFileCoder`，实现真实格式所需：从一条回复里解析**多个**文件、从**每个围栏上一行**恢复文件名、清理模型给文件名套的 markdown（`**foo.go**`、`` `foo.go` ``、`# foo.go`、`foo.go:`），并在围栏没有文件名时合理回退。

## Run / 运行

```bash
cd agents/s02-wholefile-format

# set ONE provider's key / 设置任意一个 provider 的 key
export ANTHROPIC_API_KEY=sk-ant-...

# usage: s02 [flags] <file...> <instruction>  (LAST arg is the instruction)
go run . hello.go "add a doc comment to main"

# MULTIPLE files in one shot — the new capability vs s01
go run . -v main.go util.go "move the parser into util.go"

# tests (no network) / 测试（无网络）
go test ./...
```

Supported `-provider` profiles: `anthropic` (native), `deepseek`, `openrouter`, `local` (all reached via the Anthropic wire shape). Override with `-model` / `-base-url`.
支持的 `-provider`：`anthropic`（原生）、`deepseek`、`openrouter`、`local`（均走 Anthropic 线格式）。可用 `-model` / `-base-url` 覆盖。

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `provider.go` | The generic LLM core (Anthropic wire shape) + `AnthropicProvider`, re-pasted from the shared catalog. / 通用 LLM 核心 + `AnthropicProvider`，从共享 catalog 重新粘贴。 |
| `wholefile.go` | The `WholeFileCoder`: `GetEdits` (the line-by-line state machine + `cleanFilename`) and `ApplyEdits` (write-per-file). **Start here.** / 整文件 Coder：`GetEdits`（逐行状态机 + `cleanFilename`）与 `ApplyEdits`（逐文件写）。**从这里读起。** |
| `main.go` | CLI: provider profiles, flags, and the loop that now iterates over MANY edits. / 命令行：profile、flag，以及现在遍历**多个**编辑的循环。 |
| `wholefile_test.go` | Tests with a `fakeProvider`: single file, multiple files, filename cleanup, apply to `t.TempDir()`, fallbacks, end-to-end. / 用 `fakeProvider` 的测试。 |
| `testdata/expected.txt` | An illustrative transcript of a real run. / 一次真实运行的示例记录。 |

## Key teaching points / 关键教学点

1. **The filename lives ABOVE the fence.** The whole-file format puts the path on its own line right before the opening ```` ``` ````. Recovering it (and stripping `**bold**`, backticks, `#`, trailing `:`) is the mechanism this chapter adds. See [`wholefile.go` `cleanFilename`](./wholefile.go).
   **文件名在围栏上一行。** 整文件格式把路径单独写在开围栏前一行。恢复它（并去掉 `**粗体**`、反引号、`#`、结尾 `:`）就是本章新增的机制。

2. **One reply, many files.** `GetEdits` is a state machine: a fence line toggles "inside/outside a block"; each closing fence emits one file. This is why s02 can rewrite `main.go` and `util.go` from a single LLM response — s01 couldn't.
   **一次回复，多个文件。** `GetEdits` 是状态机：围栏行切换"块内/块外"，每个闭围栏产出一个文件。所以 s02 能从一次回复同时重写 `main.go` 和 `util.go`——s01 不能。

3. **Fallbacks in reliability order.** A bare fence with no name falls back to: a filename mentioned earlier in prose (`saw`) → the sole chat file (`chat`) → otherwise an error. When names collide, the most reliable source wins (`block` > `saw` > `chat`). This mirrors upstream exactly.
   **按可靠度回退。** 没有文件名的裸围栏依次回退到：散文里提到的文件名（`saw`）→ 唯一的 chat 文件（`chat`）→ 否则报错。文件名冲突时最可靠来源胜出（`block` > `saw` > `chat`）。与上游完全一致。

4. **Apply stays trivial.** `ApplyEdits` is just resolve-path + `os.WriteFile` per file, because `Replace` already IS the whole file. All the intelligence is in the parser — the defining property of whole-file, and why it's the baseline before SEARCH/REPLACE (s03) and unified diff (s04).
   **落盘依旧平凡。** `ApplyEdits` 就是逐文件解析路径 + `os.WriteFile`，因为 `Replace` 本身就是整个文件。聪明之处全在解析器——这是整文件格式的本质，也是它先于 s03/s04 的原因。

See the full chapter write-up: [`docs/en/s02-wholefile-format.md`](../../docs/en/s02-wholefile-format.md) · [`docs/zh/s02-wholefile-format.md`](../../docs/zh/s02-wholefile-format.md).
完整章节讲解见上面两个文档。
