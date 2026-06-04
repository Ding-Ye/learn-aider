---
title: "s06 · 终端 I/O 与对话内命令"
chapter: 6
slug: s06-inputoutput-commands
est_read_min: 13
---

# s06 · 终端 I/O 与对话内命令

> 教什么：包裹 coder 循环的面向用户的**外壳**。两个部件——一个 **InputOutput** 契约（`io.go`，上游 `aider/io.py`），coder 通过它说话而不直接碰 stdin；一个 **Commands** 分派器（`commands.go`，上游 `aider/commands.py`），它拦截 `/` 开头的行、改动对话内的文件范围、且绝不把它们发给模型。`ParseAndRun` 返回的那条边界——处理了还是没处理——就是本章。

---

## Problem / 问题

s01 到 s05 都是同一副骨架：接一条指令（一个 CLI flag 或参数）、调一次 LLM、应用结果、退出。这足以*演示*一种编辑格式，但它不是一次会话。真正的 aider 运行是一个交互循环，你和模型来回多轮——而你敲下的大部分内容根本*不是*发给模型的消息。"把这个文件加入对话"、"把那个移出去"、"给我看现在范围里有什么"、"提交"、"我能干什么？"，这些是给*工具*的命令，不是给 *LLM* 的提示。如果每一行都转发给模型，一半会变成浪费的 token，而模型也根本没法真正改变它能编辑哪些文件。

这里缺两层。第一，coder 目前直接读写终端，于是它没法在没有真实 TTY 的情况下被测试，也没法在不到处传参的情况下尊重一个 `--yes` flag。第二，没有"对话内**文件范围**"这个概念让用户去增删，也没有地方放管理它的 `/` 命令。本章把两者都补上：一个很窄的 I/O 契约，和一个坐在循环前面的命令分派器。

## Solution / 解决方案

在 coder 前面放一层薄薄的外壳。**InputOutput** 变成一个小接口——`GetInput`、`ConfirmAsk`、`ToolOutput`、`ToolError`、`AddToHistory`——其余一切都依赖它。coder 不再知道 stdin 的存在，它向 `Console` 要一行。这一层间接让循环变得可测（注入脚本化输入），也让 `--yes` 变成 `ConfirmAsk` 里的一个分支，而不是穿过十个调用点的一个 flag。**Commands** 变成一张把命令名映射到处理器、作用于共享会话状态的注册表。以 `/` 开头的行被分派；处理器改动**对话内文件集合**（编辑范围），并通过同一个 `Console` 打印。

三个决策撑起整个设计：

1. **命令在触达模型之前就被拦截。** `ParseAndRun(line)` 对 `/` 行返回 `handled=true`，对普通文本返回 `false`。只有 `false` 这条路径才会变成 `Message`。`/add` 在零网络下改动状态——这正是整章无需 API key 的原因。
2. **分派器是一张 name→handler 表。** 上游用对方法的反射找 `cmd_<word>`；我们用显式的 `map[string]handler`。加一个命令就是加一条目。未知命令产生一行报错（而非崩溃），并且仍算"已处理"，于是笔误永远不会被发给 LLM。
3. **对话内文件集合就是编辑范围。** `/add`/`/drop` 增删它，`/ls` 展示它。在完整版里，这个集合会变成 coder 发送的文件内容消息——所以本章正是"模型能编辑什么"变得动态、由用户驱动的地方。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────────┐
│  user types a line                                                │
│        │                                                         │
│        ▼   io.GetInput("diff> ")        ── Console (io.go) ──     │
│  ┌───────────────┐                                               │
│  │ "/add a.go"   │  starts with "/"?                             │
│  └───────────────┘                                               │
│        │ yes                         │ no                        │
│        ▼                             ▼                           │
│  Commands.ParseAndRun           sendToCoder(line)                │
│   look up cmd in registry        (becomes a Message → LLM)       │
│        │                                                         │
│        ▼  handler mutates Session.InChat  ── the EDIT SCOPE ──   │
│  /add → InChat[a.go]=true   /drop → delete   /ls → print buckets │
│        │                                                         │
│        ▼  handled=true → loop reads the next line (no LLM call)  │
└──────────────────────────────────────────────────────────────────┘
```

核心分派（节选自 [`agents/s06-inputoutput-commands/commands.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s06-inputoutput-commands/commands.go)）：

```go
// IsCommand reports whether a line is a slash command (upstream is_command
// L255-256: `inp[0] in "/!"`). Empty input is not a command.
func (c *Commands) IsCommand(inp string) bool {
	if inp == "" {
		return false
	}
	return inp[0] == '/' || inp[0] == '!'
}

// ParseAndRun is the loop's entry point. If `inp` is a command it dispatches it
// and returns handled=true; otherwise it returns handled=false so the caller
// knows to treat the line as a chat message for the coder.
func (c *Commands) ParseAndRun(inp string) (handled bool) {
	inp = strings.TrimSpace(inp)
	if !c.IsCommand(inp) {
		return false // plain chat → flows on to the coder
	}

	body := inp[1:]
	word, args, _ := strings.Cut(body, " ")
	word = strings.TrimSpace(word)
	args = strings.TrimSpace(args)

	h, ok := c.registry[word]
	if !ok {
		// unknown command → an error line, no crash, and still handled=true so a
		// typo'd command is NOT silently sent to the LLM.
		c.io.ToolError("Invalid command: /%s (try /help)", word)
		return true
	}
	h.run(c, args)
	return true
}
```

**四个非显然之处**：

1. **`handled` 就是整条边界。** 循环里写的是 `if commands.ParseAndRun(line) { continue }`，只有否则才调用 coder。这一个 bool 把命令挡在网络之外、把对话放上网络——它是上游 `preproc_user_input` 的显式版本。
2. **未知命令返回 `true` 而非 `false`。** 把 `/frobnicate` 当作提示转发给 LLM 会是个隐蔽的 bug。报错并保持"已处理"才是安全的选择（上游 `do_run` L291-293）。
3. **注册表取代了反射。** 上游通过 `getattr` 自动注册任何 `cmd_*` 方法。`map[string]handler` 是同样的人体工学但没有魔法，`/help` 只需遍历这张表。
4. **`ConfirmAsk(--yes)` 从不读输入。** `/add` 里的"创建文件"提示走 `ConfirmAsk`；在 `--yes` 下它不碰 reader 直接返回 true（上游 `io.py` L866）。测试用同一个旋钮（`ScriptIO.Confirm`）来确定性地强制 yes 或 no。

## What Changed / 与 s05 的变化

```diff
 // s05: one-shot. Build a prompt, send one message, print the reply, exit.
-resp, err := prov.CreateMessage(ctx, req)
-fmt.Println(firstText(resp.Content))

 // s06: a REPL with a command shell in front of the coder.
+for {
+	line, ok := io.GetInput(promptPrefix(session.Format))   // Console contract
+	if !ok { return }                                       // Ctrl-D / EOF
+	if commands.ParseAndRun(line) { continue }              // "/..." handled locally
+	sendToCoder(io, session, line)                          // plain text → the LLM
+}
```

这次变化是结构性的，不只是加法。s05 的 `main` 是一条直线：拼装 → 发送 → 打印。s06 的 `main` 是一个带分叉的循环，而那个分叉就是本节要讲的。两个新的长生命周期对象出现了——一个 `Console`（I/O 契约）和一个作用于可变 `Session` 的 `Commands` 分派器——而 coder 调用缩成循环里的一个分支。在之前每一章里都是固定 CLI 参数的文件范围，现在变成用户在运行时用 `/add` 和 `/drop` 编辑的东西。

## Try It / 动手试一试

```bash
cd agents/s06-inputoutput-commands

# interactive REPL — no network, no API key (this chapter has no LLM call)
go run .

# scriptable: pipe a sequence of commands + one chat line on stdin
printf '/help\n/add main.go\n/ls\nrename foo to bar\n/quit\n' | go run .

# seed a different repo file universe; change the prompt prefix; auto-confirm
go run . -files a.go,b.go,c.go
go run . -format whole
go run . -yes

# tests (these run in CI, no network)
go test -v ./...
```

期望输出形态：

```
diff> Added main.go to the chat
diff> Repo files not in the chat:
  README.md
  util.go
Files in chat:
  main.go
diff> [coder] would send to the LLM with 1 file(s) in chat: "rename foo to bar"
diff> Bye.
```

关键要看到的：`/add` 和 `/ls` 会打印并改动状态，但 `rename foo to bar` 这一行（不以 `/` 开头）是唯一一个触达 coder 桩的——而桩报告了它*会*发送的文件范围。未知的 `/command` 打印 `Invalid command: ...` 且不会被转发。

## Upstream Source Reading / 上游源码阅读

在 aider 里，等价的外壳是 `aider/commands.py`（`Commands` 类）加上 `aider/io.py`（`InputOutput` 类）。下面的分派器是承重核心；真实文件有 50+ 个 `cmd_*` 处理器和前缀匹配，但形状正是这样：判断这行是不是命令、切出第一个词、按名字查处理器、调用它。

```upstream:aider/commands.py#L312-L332
    def run(self, inp):
        if inp.startswith("!"):
            self.coder.event("command_run")
            return self.do_run("run", inp[1:])

        res = self.matching_commands(inp)
        if res is None:
            return
        matching_commands, first_word, rest_inp = res
        if len(matching_commands) == 1:
            command = matching_commands[0][1:]
            self.coder.event(f"command_{command}")
            return self.do_run(command, rest_inp)
        elif first_word in matching_commands:
            command = first_word[1:]
            self.coder.event(f"command_{command}")
            return self.do_run(command, rest_inp)
        elif len(matching_commands) > 1:
            self.io.tool_error(f"Ambiguous command: {', '.join(matching_commands)}")
        else:
            self.io.tool_error(f"Invalid command: {first_word}")
```

**对照阅读要点**：

- **反射 vs. 映射**：上游 `do_run`（L287-299）用 `getattr(self, "cmd_" + name)` 解析处理器，于是*任何*名为 `cmd_x` 的方法自动成为命令。我们用显式的 `map[string]handler`——同样的"加一个东西=加一个命令"的人体工学，但可见、且被类型检查。
- **前缀匹配**：上游通过 `matching_commands` 把 `/dr` 解析成 `/drop`，并在一个前缀匹配到多个时报 "Ambiguous command"。s06 要求精确的命令词；本章关心的是分派的*形状*，所以跳过前缀解析。
- **`!` shell 别名**：当行以 `!` 开头时，上游的 `run` 会执行一条 shell 命令（L313-315）。本课程里 s06 没有命令执行层，所以 `!` 会落到 "Invalid command" 而不执行任何东西。
- **`--yes` 快速路径**：当 `self.yes` 被设置时，上游 `confirm_ask`（io.py L866-869）不读输入直接返回。那一个分支正是让循环可脚本化的关键；s06 在 `IO.ConfirmAsk` 和 `ScriptIO.Confirm` 里复现了它。
- **一个我们故意保留的"正确但不完美"的设计**：未知命令被报告并仍当作*已处理*，于是像 `/lst` 这样的笔误永远不会被当成提示转发给模型——与上游一致，上游也是打印一行错误然后返回，而不落到 LLM。

**想读更多**：从 `aider/coders/base_coder.py` 的 `run()` 入手，跟着 `preproc_user_input()` 进 `aider/commands.py` 的 `run`/`do_run`，再看对话内集合如何在 `base_coder.py` 的 `get_files_messages()` 里变成提示内容。这条线就是 s06 → s07（git）→ 消息拼装章节的真实代码地图。

---

**下一节预告**：s07 给文件范围装上牙齿——每一次应用的编辑都变成一个原子的、带署名的 **git 提交**（`aider/repo.py`），于是循环多出一个应用后步骤，编辑变得可回退。
