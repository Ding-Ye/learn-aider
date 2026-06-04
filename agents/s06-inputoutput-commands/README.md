# s06 · InputOutput layer + in-chat commands / 终端 I/O 与对话内命令

A user-facing shell wraps the loop: `/`-commands mutate file scope locally; plain text flows to the coder. / 给循环套上面向用户的外壳：`/` 命令在本地改动文件范围，普通文本才流向 coder。

s01..s05 took one instruction on the command line, made one LLM call, and exited. A real aider session is a REPL with two extra layers. **InputOutput** (`io.go`, upstream `aider/io.py`) is a narrow contract — prompt for a line, ask yes/no, print output, record chat history — so the coder never touches stdin and the loop becomes testable. **Commands** (`commands.go`, upstream `aider/commands.py`) is a registry of `/add` `/drop` `/ls` `/diff` `/help` handlers dispatched by name; a line starting with `/` is intercepted and mutates the in-chat **file set** (the edit scope) without ever reaching the model, while a plain line is forwarded to the coder. That boundary — `ParseAndRun` returns `handled` — is the heart of the chapter.
s01..s05 在命令行上接一条指令、调一次 LLM、然后退出。真正的 aider 会话是一个带两层外壳的 REPL。**InputOutput**（`io.go`，上游 `aider/io.py`）是一个很窄的契约——读一行、问是/否、打印输出、记录对话历史——于是 coder 不必碰 stdin，循环也变得可测。**Commands**（`commands.go`，上游 `aider/commands.py`）是一张 `/add` `/drop` `/ls` `/diff` `/help` 处理器的注册表，按名字分派；以 `/` 开头的行被拦截下来，改动"在对话中的**文件集合**"（即编辑范围）而**不**触达模型，普通行才转发给 coder。这条边界——`ParseAndRun` 返回 `handled`——就是本章的核心。

## Run / 运行

```bash
cd agents/s06-inputoutput-commands

# interactive REPL (no network, no API key — this chapter has no LLM call)
# 交互式 REPL（无网络、无 key——本章没有 LLM 调用）
go run .

# scriptable: pipe commands on stdin / 可脚本化：用 stdin 灌入命令
printf '/add main.go\n/ls\nrename foo to bar\n/quit\n' | go run .

go run . -files a.go,b.go,c.go   # seed the repo file universe / 设定 repo 文件全集
go run . -format whole           # prompt prefix becomes "whole> " / 提示前缀变 "whole> "
go run . -yes                    # auto-answer confirmations / 自动确认（--yes）

# tests (no network) / 测试（无网络）
go test ./...
```

Inside the REPL: `/help` lists commands, `/add <file>` and `/drop <file>` change the edit scope, `/ls` shows it, anything without a leading `/` goes to the coder stub, and `/quit` (or Ctrl-D) exits.
REPL 内：`/help` 列出命令，`/add <file>` 与 `/drop <file>` 改动编辑范围，`/ls` 查看范围，不以 `/` 开头的内容会进入 coder 桩，`/quit`（或 Ctrl-D）退出。

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `commands.go` | **The heart of the chapter.** `IsCommand` (upstream `is_command`), `ParseAndRun` (the dispatch + chat boundary, upstream `run`/`do_run`), a name→handler `registry`, and the `/add` `/drop` `/ls` `/diff` `/help` handlers over a `Session`. **Start here.** / **本章核心。** `IsCommand`、`ParseAndRun`（分派 + 对话边界）、name→handler 注册表，以及作用于 `Session` 的五个命令。**从这里读起。** |
| `io.go` | The `Console` interface (`GetInput` / `ConfirmAsk` / `ToolOutput` / `ToolError` / `AddToHistory`) + the real `IO` and the test double `ScriptIO`. / `Console` 接口 + 真实 `IO` 与测试替身 `ScriptIO`。 |
| `main.go` | The REPL: read a line → `ParseAndRun` (command) or send to the coder (chat) → loop. / REPL：读一行 → `ParseAndRun`（命令）或交给 coder（对话）→ 循环。 |
| `provider.go` | The shared type catalog. Unchanged from s01..s05; carried so the module is self-contained. The shell sits ABOVE the Provider. / 共享类型目录，与 s01..s05 一致；外壳位于 Provider 之上。 |
| `commands_test.go` / `io_test.go` | Tests: dispatch, unknown command, `/add`→`/ls` reflects state, non-command passthrough, confirm yes/no via injected IO, full REPL drive. / 测试：分派、未知命令、`/add`→`/ls` 反映状态、非命令穿透、注入式 yes/no、整段 REPL。 |
| `testdata/expected.txt` | An illustrative (and here fully deterministic) REPL transcript. / 一段示例（且完全确定性的）REPL 记录。 |

## Key teaching points / 关键教学点

1. **Commands are intercepted before the model.** `ParseAndRun` returns `handled=true` for a `/`-line and `false` for chat. Only the chat path becomes a `Message`. A `/add` mutates state with zero network — that's why this chapter needs no API key. See [`commands.go` `ParseAndRun`](./commands.go).
   **命令在触达模型前就被拦截。** `ParseAndRun` 对 `/` 行返回 `handled=true`，对普通对话返回 `false`，只有对话路径才会变成 `Message`。`/add` 在零网络下改动状态——这正是本章无需 key 的原因。

2. **A dispatcher is just a name→handler map.** Upstream finds `cmd_<word>` by reflection over methods; we use an explicit `map[string]handler`. Adding a command = adding one entry. Unknown command → an error line, not a crash, and still "handled" so a typo isn't sent to the LLM.
   **分派器不过是一张 name→handler 表。** 上游用反射找 `cmd_<word>` 方法；我们用显式的 `map[string]handler`。加命令 = 加一条目。未知命令 → 一行报错而非崩溃，且仍算"已处理"，避免把笔误发给 LLM。

3. **IO is an interface so the loop is testable.** Programming against `Console` (not a concrete terminal) lets `ScriptIO` feed canned input and capture output. `ConfirmAsk` with `--yes` returns true without reading stdin (upstream `io.py` L866) — the same knob tests use to force yes/no.
   **IO 是接口，所以循环可测。** 面向 `Console`（而非具体终端）编程，`ScriptIO` 就能灌入预设输入并捕获输出。`--yes` 下的 `ConfirmAsk` 不读 stdin 直接返回 true（上游 `io.py` L866）——测试也用同一个旋钮来强制 yes/no。

4. **The file set IS the edit scope.** `/add`/`/drop` grow/shrink `Session.InChat`; `/ls` reports it; the coder stub prints how many files it WOULD send. In a full build, that set becomes the file-content messages — so this chapter is where edit scope becomes dynamic and user-driven.
   **文件集合就是编辑范围。** `/add`/`/drop` 增删 `Session.InChat`，`/ls` 展示它，coder 桩打印它会发送多少个文件。在完整版里，这个集合会变成文件内容消息——所以本章正是编辑范围变得动态、由用户驱动的地方。

See the full chapter write-up: [`docs/en/s06-inputoutput-commands.md`](../../docs/en/s06-inputoutput-commands.md) · [`docs/zh/s06-inputoutput-commands.md`](../../docs/zh/s06-inputoutput-commands.md).
完整章节讲解见上面两个文档。
