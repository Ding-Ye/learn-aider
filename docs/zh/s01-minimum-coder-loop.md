---
title: "s01 · 最小 Coder 循环"
chapter: 1
slug: s01-minimum-coder-loop
est_read_min: 11
---

# s01 · 最小 Coder 循环

> 教什么：**Coder 循环**——让 LLM 修改磁盘上一个文件的最小闭环（读取 → 构造提示 → 调用 → 提取 → 写回）。这是 aider 的脊椎，后面每一章都是它之上的一层，所以单独成章。

---

## Problem / 问题

aider 是一个 AI 结对程序员：你用自然语言描述改动，它就编辑你的文件并提交。但真实代码库约有 1.3 万行，分散在各种编辑格式解析器、tree-sitter 的 repo map、git 集成、检查器与反思循环、token 预算、以及一个支持 50 多种模型的 provider 层里。如果你从那里开始读，必然被淹没。

所以问一个尽可能小的问题：**让 LLM 改动一个文件，最少需要多少代码？** 不是"aider 怎么做到所有事"，而只是那一条承重的闭环。砍掉 git、repo map、多文件编辑、流式和反思。剩下的就是：一条用户指令、一次 LLM 调用、一次落盘编辑。这条不可再分的闭环就是 **Coder 循环**，一旦你把它看清楚，aider 后面加的每个功能都有了明确的挂载点。

## Solution / 解决方案

一个 Coder 循环就是五拍：**读取目标文件 → 构造一个钉死编辑格式的提示 → 调用 `Provider` → 从回复中提取一个编辑 → 写回文件。** 就这些。上游对应的是 `Coder.run → run_one → send_message → apply_updates`；我们把整条链路压缩进一个方法 `Coder.Run`。

让它变得极小的窍门是**编辑格式**。我们不教模型一套工具调用协议，而是用系统提示"引导"它把整个新文件放进一个围栏代码块里返回，然后解析这个块。这就是 aider 的 `whole` 格式——它最简单的格式——也是所有模型（哪怕不支持工具调用）都能产出的最低公分母。

三个值得点名的设计决策：

1. **整文件，而非 diff。** 落盘就是一次 `os.WriteFile`——不做文本匹配、不做合并。聪明之处全在解析器，所以*应用*这一步是平凡的。（diff 和 SEARCH/REPLACE 在 s03–s04。）
2. **`Provider` 从第一行就是接口。** 循环从不依赖具体 HTTP 客户端，于是测试可以注入一个假实现，s10 也能在同一接口后面包上重试与流式。
3. **一块，一文件。** s01 只解析第一个围栏块。多文件回复（在每个围栏上方扫描文件名）被刻意推迟到 s02——这样能诚实地体现基线到底有多小。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│                      Coder.Run(path, instruction)            │
│                                                              │
│  磁盘 ──读取──▶ [原文件] ──┐                                 │
│                            ▼                                 │
│  系统提示 ──────────▶ CreateMessageRequest ──▶ Provider      │
│  (整文件规则)              ▲                      │          │
│                            │                      ▼          │
│  指令 ──────────────────────┘             CreateMessageResp  │
│                                                   │          │
│                                                   ▼          │
│                            extractCodeBlock(reply) → Edit    │
│                                                   │          │
│                              applyWholeFile ──────┘          │
│                                   │                          │
│                                   ▼                          │
│                              磁盘 [重写后]                   │
└──────────────────────────────────────────────────────────────┘
```

核心约 40 行（节选自 [`agents/s01-minimum-coder-loop/coder.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s01-minimum-coder-loop/coder.go)）：

```go
func (c *Coder) Run(ctx context.Context, path, instruction string) error {
	// 1. 读取目标文件（模型需要看到当前内容）。
	original, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// 2. 构造提示。系统提示 = 编辑格式契约；用户消息 = 文件 + 指令。
	req := CreateMessageRequest{
		System: wholeFileSystemPrompt(path),
		Messages: []Message{{
			Role:    "user",
			Content: []ContentBlock{{Type: "text", Text: buildUserPrompt(path, string(original), instruction)}},
		}},
	}

	// 3. 调用 provider —— 唯一的网络往返。
	resp, err := c.Provider.CreateMessage(ctx, req)
	if err != nil {
		return fmt.Errorf("provider call: %w", err)
	}
	reply := firstText(resp.Content)

	// 4. 提取编辑：整文件 = 第一个围栏代码块。
	edit, err := c.extractEdit(path, reply)
	if err != nil {
		fmt.Fprintf(c.errw, "[s01] could not parse an edit from the reply:\n%s\n", reply)
		return err // s01 没有反思 —— 报错即停
	}

	// 5. 写回。一个编辑，一个文件。
	if err := c.applyWholeFile(edit); err != nil {
		return fmt.Errorf("apply edit: %w", err)
	}
	fmt.Fprintf(c.out, "Applied edit to %s (%d bytes)\n", edit.Path, len(edit.Replace))
	return nil
}
```

**四个非显然之处**：

1. **围栏是聊天语法，不是文件内容。** 模型写的是 ` ```go\n<代码>\n``` `，但磁盘上的文件只能包含 `<代码>`。`extractCodeBlock` 会剥掉两行围栏（以及开围栏上的语言标签）——忘了这一点，你就会把反引号写进用户的源码里。
2. **我们只取*第一个*块。** 整文件是"一条回复一个文件"。只取第一块（而不是把所有块拼起来）让 s01 的解析器保持约 10 行；s02 通过读取每个围栏上方那行的文件名，泛化到多个块。
3. **未闭合的围栏是畸形，不是半成品。** 如果只有开 ` ``` ` 而没有闭围栏，`extractCodeBlock` 返回 `false`。s01 没有流式，所以半个块就是一条坏回复。
4. **没有反思——故意的。** 上游一条畸形回复会变成 `reflected_message` 然后循环重试（受 `max_reflections` 限制）。s01 打印原始回复并以非零码退出。那个自我纠正循环是 s09；这里略去它，循环就只剩五拍。

## What Changed / 与上一节的变化

这是**基线章节**——没有上一节可以做 diff。后面所有内容都扩展这里引入的类型。核心结构是：

```go
// 通用 LLM 核心（Anthropic 线格式），每一章共享：
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

// 本章引入的 aider 专属部分：
type EditFormat string
const FormatWhole EditFormat = "whole" // s03 加 "diff"，s04 加 "udiff"

type Edit struct {
	Path    string
	Search  string // "" 表示整文件替换（s01 始终留空）
	Replace string // 整个新文件
}
```

语义上：s01 确立了 `Provider` 边界和 `读取 → 调用 → 提取 → 写回` 的骨架。s02 把内联的 `extractCodeBlock` 变成可复用的多文件 `WholeFileCoder`；s03 让 `Edit.Search` 非空（SEARCH/REPLACE）；s07 在写回后加 git 自动提交；s09 用反思循环把整件事包起来。

## Try It / 动手试一试

```bash
cd agents/s01-minimum-coder-loop

# 设置任意一个 provider 的 key
export ANTHROPIC_API_KEY=sk-ant-...

# 读取 hello.go，让模型重写它，再写回
go run . hello.go "add a doc comment to main"

# -v 在 stderr 打印请求/响应形态
go run . -v hello.go "rename foo to bar"

# 切换 provider —— 只换传输层
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek hello.go "fix the typo"

# 测试（无网络 —— fakeProvider 替代 LLM）
go test -v ./...
```

期望输出形态：

```
# 带 -v 的 stderr：
[s01] provider=anthropic model=claude-sonnet-4-6 url=
[s01] sending 62 bytes of hello.go + instruction "add a doc comment to main"
[s01] stop_reason=end_turn in=512 out=78 tokens

# stdout：
Applied edit to hello.go (96 bytes)
```

如果模型回复里没有围栏块，你会看到 `[s01] could not parse an edit from the reply:` 后跟原始文本并以非零码退出——文件保持不动。（完整的示例记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s01-minimum-coder-loop/testdata/expected.txt)。）

## Upstream Source Reading / 上游源码阅读

aider 的等价物是 `WholeFileCoder.get_edits`（解析器）和 `apply_edits`（写入器）。最大的差别：上游解析**多个**围栏块，并从每个围栏上方那行**恢复文件名**，还有"哪个名字优先"的规则。s01 已经知道路径（来自命令行参数）且只解析一个块，所以全部略过。

```upstream:aider/coders/wholefile_coder.py#L22-L128
class WholeFileCoder(Coder):
    edit_format = "whole"              # 选定提示 + 这个解析器

    def get_edits(self, mode="update"):
        content = self.get_multi_response_content_in_progress()  # 原始回复
        chat_files = self.get_inchat_relative_files()            # 可编辑文件
        lines = content.splitlines(keepends=True)
        edits = []

        fname = None          # None => 块外；有值 => 块内，正在累积
        fname_source = None
        new_lines = []
        for i, line in enumerate(lines):
            if line.startswith(self.fence[0]) or line.startswith(self.fence[1]):
                if fname is not None:                 # 闭围栏 -> 产出一个块
                    edits.append((fname, fname_source, new_lines))
                    fname, fname_source, new_lines = None, None, []
                    continue
                if i > 0:                             # 开围栏 -> 文件名在上一行
                    fname_source = "block"
                    fname = lines[i - 1].strip().strip("*").rstrip(":").strip("`").lstrip("#").strip()
                    if len(fname) > 250:              # Issue #1232：荒谬的名字 -> 丢弃
                        fname = ""
                    if fname and fname not in chat_files and Path(fname).name in chat_files:
                        fname = Path(fname).name      # 折叠掉伪造的 path/to/ 前缀
                if not fname:                         # 裸 ``` 且无可用文件名
                    if len(chat_files) == 1:
                        fname, fname_source = chat_files[0], "chat"
                    else:
                        raise ValueError("No filename provided before fence")
            elif fname is not None:
                new_lines.append(line)                # 块内：文件正文
        if fname:                                     # 回复在块中途结束
            edits.append((fname, fname_source, new_lines))
        return edits  # （上游随后按来源优先级去重：block > saw > chat）

    def apply_edits(self, edits):
        for path, fname_source, new_lines in edits:
            full_path = self.abs_root_path(path)
            self.io.write_text(full_path, "".join(new_lines))  # s01: os.WriteFile
```

**对照阅读要点**：

- **文件名恢复。** 上游从围栏上方那行提取文件名（剥掉 `**`、反引号、`#`、结尾 `:`）。我们在命令行传入路径，所以 s01 不需要这些——而这套机制正是 s02 要重建的。
- **多块 vs 一块。** 上游的状态机遍历每个围栏以支持多文件回复；s01 在第一块之后就返回。思路相同，范围更小。
- **去重优先级。** 上游把文件名来源按 `block > saw > chat` 排序，让最可靠的名字胜出。只有一个已知路径时没有歧义要解决。
- **`apply_edits` 平凡——出于设计。** 它就是 `join + write`。整文件之所以是基线格式，正因为一旦解析完成，*应用*编辑就是一个空操作；难点全在解析。
- **一个我们故意保留的"不完美"选择。** 和上游一样，如果回复在块中途结束我们仍然产出它（末尾的 `if fname:`）。s01 沿用这份宽容，但在*缺少开*围栏处划线，把它判为畸形。

**想读更多**：从 `aider/coders/base_coder.py` 的 `run_one`（L924）入手，跟着 `send_message`（L1419）进 `apply_updates`（L2296），它调用上面的 `get_edits` / `apply_edits`。这条链——`run → run_one → send_message → apply_updates → get_edits/apply_edits`——就是 s01 → s02（整文件解析器）→ s07（自动提交）→ s09（反思）的真实代码地图。

---

**下一节预告**：s02 把本节内联的 `extractCodeBlock` 变成可复用的 `WholeFileCoder`，处理多个文件并从每个围栏上方那行恢复文件名——也就是 aider 真正的整文件格式。
