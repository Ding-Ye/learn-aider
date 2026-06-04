---
title: "s09 · 检查器与反思循环"
chapter: 9
slug: s09-linter-reflection
est_read_min: 11
---

# s09 · 检查器与反思循环

> 教什么：aider 如何自我纠错。应用一次编辑后，它对改动过的文件跑一个**检查器（linter）**，如果检查器报错，就把错误作为一条新的用户消息交回给模型、让它重试——这是一个有小上限约束的**反思循环（reflection loop）**。这是控制流第一次超出"单次发送"、变成多轮，因此单独成章。

---

## Problem / 问题

s01..s08 搭出了一条完整的单次（single-shot）流水线：用户指令变成提示词，模型回复，我们解析出编辑、应用它，再（s07）自动提交。RepoMap（s08）甚至给模型一份按相关性排序的仓库视图，让它带着上下文去改。但这些章节无一例外都停在同一个地方——模型产出什么就落盘什么，这一轮就结束了。

问题在于模型并不完美。它会漏一个花括号、把函数写到一半、引用一个不存在的符号。在单次循环里，这段坏代码会被提交，然后由人事后手工发现。真正的结对程序员不是这么干的：他们会注意到编译错误，并在交出改动*之前*把它修好。s09 要做的正是加上这个反射动作——对我们刚写下的东西跑一次检查，如果坏了，就把错误连同一次修复机会给模型，无需人来回一趟。

## Solution / 解决方案

心智模型是：**在既有的"发送-应用"这一轮外面裹上一个反馈循环**。应用编辑后，我们对改动过的文件跑一个 `Linter`。干净的结果结束这一轮（没什么要修的）。不干净的结果会被变成一条合成的*用户*消息——就是检查器自己的输出，前面加上 `# Fix any errors below`——这条消息重新进入循环，就好像人把编译错误粘回了对话里。我们一直重复，直到检查通过，或者撞上一个小的**反思上限（reflection cap）**。

三个设计决策撑起本章：

1. **检查报告变成下一条消息。** 那一句赋值 `message = lintText` 就是全部机制。模型看到自己的坏输出后面紧跟着失败信息，这是最自然不过的"修一下"信号。
2. **上限是必需的，不是锦上添花。** 一个不断产出坏代码的模型会无限循环。上游上限是 3 次反思；我们默认相同，到顶就带警告停下而不是空转——把仍然坏的文件交给人，好过无限烧 token。
3. **循环与格式、provider 无关。** 它只跟一个 `Coder`（负责解析/应用）和一个 `SendFunc`（负责调用模型）打交道。检查器无非就是"跑一条命令、抓输出、打标签"。换掉 coder 或换掉模型，循环都不变。

## How It Works / 工作原理

```ascii-anim frames=2
┌─────────────────────────────────────────────────────────────┐
│  user instruction                                            │
│        │                                                     │
│        ▼                                                     │
│   ┌─────────┐   reply   ┌──────────┐  edits  ┌───────────┐  │
│   │  Send   │ ────────▶ │ GetEdits │ ──────▶ │ ApplyEdits│  │
│   └─────────┘           └──────────┘         └─────┬─────┘  │
│        ▲                                            ▼        │
│        │                                      ┌───────────┐  │
│        │  message = lintText                  │  Linter   │  │
│        │  (reflections++)                     │  .Lint    │  │
│        │                                      └─────┬─────┘  │
│        │           not clean & under cap            │        │
│        └────────────────◀──────────────────────────┤        │
│                                       clean OR cap  ▼        │
│                                                   DONE       │
└─────────────────────────────────────────────────────────────┘
```

循环主体几乎是上游 `run_one` 的逐字转写（节选自 [`agents/s09-linter-reflection/reflect.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s09-linter-reflection/reflect.go)）：

```go
func (r *ReflectLoop) Run(ctx context.Context, userInstruction string) (*ReflectResult, error) {
	res := &ReflectResult{}
	messages := []Message{userMsg(userInstruction)}
	message := userInstruction

	for message != "" {
		// 1. 针对当前 message 做一次模型往返。
		reply, err := r.Send(ctx, messages)
		if err != nil {
			return res, fmt.Errorf("send (reflection %d): %w", res.Reflections, err)
		}
		res.Replies = append(res.Replies, reply)
		messages = append(messages, assistantMsg(reply))

		// 2. 落实编辑，然后检查改动过的文件。
		edits, err := r.Coder.GetEdits(reply)
		if err != nil {
			return res, fmt.Errorf("parse edits (reflection %d): %w", res.Reflections, err)
		}
		applied, _, err := r.Coder.ApplyEdits(edits)
		if err != nil {
			return res, fmt.Errorf("apply edits (reflection %d): %w", res.Reflections, err)
		}
		res.Applied = append(res.Applied, applied...)
		lintText, ok := r.Linter.Lint(r.pathsToLint(applied))

		// 3. 收敛：检查器满意了。
		if ok {
			res.Clean = true
			return res, nil
		}
		// 4. 不干净。如果预算用完了就停。
		if res.Reflections >= r.MaxReflections {
			res.HitCap = true
			res.LintText = lintText
			return res, nil
		}
		// 5. 反思：检查报告变成下一条用户消息。
		res.Reflections++
		message = lintText
		messages = append(messages, userMsg(message))
	}
	res.Clean = true
	return res, nil
}
```

**四个非显然之处**：

1. **`messages` 和 `message` 不是一回事** —— `messages` 是每一轮发给模型的完整对话（每次反思会增加一个 assistant 轮 + 一个 user 轮）；`message` 只是循环用来判断"还有没有事要做"的哨兵。它一旦变空，循环就结束。
2. **上限统计的是*额外*的轮次，不是总调用数。** `Reflections == 0` 表示模型第一次就做对了；`MaxReflections == 3` 最多允许 4 次模型调用。检查用的是*自增之前*的 `>=`，所以我们永远不会超出预算。
3. **没有编辑是干净退出，不是错误。** 如果模型只回了散文、没有围栏代码块，`GetEdits` 返回空，没什么可检查的，循环干净结束。反思循环绝不能把"模型拒绝了"当成需要重试的失败。
4. **是检查器判定"干净"，而不是"没有编辑"。** 即便是成功应用的编辑也会被重新检查；收敛意味着*检查器通过了*，这才保证落盘的文件确实能编译。

## What Changed (vs. s08) / 与 s08 的变化

```diff
  // s08（以及 s01..s07）：这一轮到 apply 就结束。
- reply := send(messages)
- edits, _ := coder.GetEdits(reply)
- coder.ApplyEdits(edits)
- // ...自动提交，然后等待下一条用户消息。

  // s09：apply 之后跟着 lint，而 lint 可以让循环重新进入。
+ for message != "" {
+     reply := send(messages)
+     edits, _ := coder.GetEdits(reply)
+     applied, _, _ := coder.ApplyEdits(edits)
+     lintText, ok := linter.Lint(pathsToLint(applied))
+     if ok { break }                          // 干净 -> 完成
+     if reflections >= maxReflections { break } // 到上限就放弃
+     reflections++
+     message = lintText                        // 错误变成下一轮
+ }
```

这个变化是结构性的，不是表面的。到 s08 为止，循环是一条直线：一个输入，一个输出。s09 把它合拢成一个由检查器把关的环——程序现在自己决定要不要再调一次模型。这种自驱的重新进入，加上一个保证总会终止的硬上限，就是新机制。

## Try It / 动手试一试

```bash
cd agents/s09-linter-reflection

# 检查模式（无网络、无 key）：打印循环将要反思回去的那段文本。
go run . -lint broken.go

# 干净文件 => "nothing to reflect"
go run . -lint provider.go

# 运行模式：用 LLM 改文件，然后检查 + 反思，直到干净或到达上限。
export ANTHROPIC_API_KEY=sk-ant-...
go run . -v -file calc.go -instruction "add a Divide(a, b int) int function"

# 把自我纠错的额外轮次限制为 2
go run . -v -file calc.go -instruction "..." -max-reflections 2

# 测试（无网络）
go test -v ./...
```

期望输出形态：

```
# 对坏文件跑检查模式：
# Fix any errors below, if possible.

## Running: gofmt broken.go

broken.go:4:9: expected operand, found '}'

# 运行模式，模型在第二轮修好了自己的错误：
[s09] converged after 1 reflection(s) — lint is now clean.
applied 2 edit(s) to calc.go across 2 pass(es)
```

## Upstream Source Reading / 上游源码阅读

在 aider 里，检查器和反思循环分处两个文件。检查器是 `aider/linter.py`；循环是 `aider/coders/base_coder.py` 里 `run_one` 内部那个 `while message:` 块（L924-944，s01 读过）。下面这段节选是检查器的分发器（dispatcher）——也就是我们 Go 里 `Linter.Lint`/`lintOne`/`runCmd` 直接移植的那段。

```upstream:aider/linter.py#L82-L116
    def lint(self, fname, cmd=None):
        rel_fname = self.get_rel_fname(fname)
        try:
            code = Path(fname).read_text(encoding=self.encoding, errors="replace")
        except OSError as err:
            print(f"Unable to read {fname}: {err}")
            return

        if cmd:
            cmd = cmd.strip()
        if not cmd:
            lang = filename_to_lang(fname)
            if not lang:
                return                       # 该语言没有检查器 -> 跳过
            if self.all_lint_cmd:
                cmd = self.all_lint_cmd      # --lint-cmd 覆盖优先
            else:
                cmd = self.languages.get(lang)

        if callable(cmd):
            lintres = cmd(fname, rel_fname, code)   # py_lint 路径
        elif cmd:
            lintres = self.run_cmd(cmd, rel_fname, code)
        else:
            lintres = basic_lint(rel_fname, code)   # 内置 tree-sitter 检查

        if not lintres:
            return                            # 干净 -> None（循环看到"无错误"）

        res = "# Fix any errors below, if possible.\n\n"  # 我们逐字照抄的头
        res += lintres.text
        res += "\n"
        res += tree_context(rel_fname, code, lintres.lines)  # 源码美化；我们舍弃

        return res
```

**对照阅读要点**：

- **优先级相同，注册键不同。** 上游按 tree-sitter *语言*作键（`self.languages[lang]`）；我们按文件*扩展名*作键（`.go`）。两者在没有配置时都回落到内置检查。`all_lint_cmd` 覆盖（我们的 `allCmd`）凌驾于二者之上。
- **`return None` == 干净。** 整个循环就靠这个约定：检查器什么都没发现就返回 `None`，分发器返回 `None`，于是 `run_one` 看不到 `reflected_message` 而停下。我们 Go 版用 `("", true)` 表达同一含义。
- **头是承重的，美化不是。** `# Fix any errors below, if possible.` 告诉模型下面这段是问题报告；我们一字不差地保留。`tree_context` 会用 `█` 标记把出错的源码渲染出来——很好，但不是必需，所以我们舍弃它，保留原始的 `file:line` 错误。
- **上限在*另一个*文件里。** `lint` 只产出文本；边界在 `run_one` 的 `if self.num_reflections >= self.max_reflections`。我们把两者合进一个包（`linter.go` + `reflect.go`），但保留同样的职责划分。
- **一个我们故意保留的不完美简化：** 我们的内置检查器是 `gofmt -l -e`，能抓住解析错误和格式漂移，但抓不住类型错误。上游的 `py_lint` 还会额外跑 `compile()` 和 flake8。`-lint-cmd "go vet"` 这个 flag 就是你选择更深检查的方式——正确，但默认比上游更轻。

**想读更多**：从 `aider/linter.py` 的 `Linter.lint` 入手，跟着它返回的 `LintResult` 进 `aider/coders/base_coder.py` 的 `lint_edited`（它设置 `self.lint_outcome`），再进 `run_one` 的反思循环——那份 outcome 在那里变成 `reflected_message`。这条线就是 s09 → s10 的真实代码地图（s10 里 `send` 背后的模型调用获得重试与流式）。

---

**下一节预告**：s10 把 `send` 背后的 `Provider` 演化成一套可配置、有韧性的栈——模型注册表、带指数退避的重试、以及流式——好让反思循环在瞬时故障和众多模型家族之间都能继续工作。
