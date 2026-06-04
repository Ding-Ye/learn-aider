---
title: "s02 · 整文件编辑格式"
chapter: 2
slug: s02-wholefile-format
est_read_min: 12
---

# s02 · 整文件编辑格式

> 教什么：aider 最简单的*编辑格式*——`whole`（整文件）格式：LLM 返回完整重写后的文件，每个文件一个围栏代码块，路径写在围栏上一行。这一节把 s01 那个只解析一个块的玩具解析器升级成真正的多文件 `WholeFileCoder`，并把 `Coder` 接口正式化，所以它单独成章。

---

## Problem / 问题

s01 证明了 coder 循环能跑通，但它的解析器是个玩具。它假设只有一个文件（路径来自 `argv`），抓第一个围栏代码块、去掉围栏就完事。这足够演示"重写这一个文件"，但仅此而已。

真实的整文件格式要做三件 s01 做不到的事。第一，一条回复可以**同时重写多个文件**——把代码从 `main.go` 搬到 `util.go` 的重构，就是一次回复里的两个文件。第二，**文件名**唯一出现的地方是每个围栏的*上一行*，而且模型会给它套各种装饰：`**foo.go**`、`` `foo.go` ``、`# foo.go`、`foo.go:`。第三，模型有时给出一个完全没有文件名的裸围栏，或者照抄提示里示例的 `path/to/` 假前缀。忽略这一切的玩具会写错文件、把围栏写进你的源码，或者悄悄丢掉编辑。s02 的任务就是像 aider 真正那样处理这个格式。

## Solution / 解决方案

把 s01 内联的 `extractCodeBlock` 提升为可复用的 `WholeFileCoder`，让它满足课程的 `Coder` 接口（`Format`、`SystemPrompt`、`GetEdits`、`ApplyEdits`）。解析逻辑放在 `GetEdits` 里，一个小小的逐行**状态机**：遍历回复时，围栏行在"块外"和"块内、正在累积文件内容"之间切换，每个*闭*围栏产出一个解析出的文件。落盘仍然是逐文件平凡写入，因为整文件格式里解析出的 `Replace` 本身就*是*整个新文件。

三个值得点名的设计决策：

1. **文件名来自围栏的上一行。** 在每个*开*围栏处读取上一行并清理（`cleanFilename` 去掉 `**`、反引号、`#`、结尾 `:`）。这一条规则就是整个机制——把它做对，多文件就"自然能用"。
2. **按可靠度回退。** 没有可用文件名的裸围栏依次回退到：散文里提到的文件名（`saw`）→ 唯一的 chat 文件（`chat`）→ 否则报错。当两个块指向同一文件时，最可靠的*来源*胜出（`block` > `saw` > `chat`）。与上游完全一致。
3. **解析难，落盘易。** 聪明之处全在 `GetEdits`；`ApplyEdits` 就是逐文件 `os.WriteFile`。这种不对称是整文件格式的本质，也是它先于 SEARCH/REPLACE（s03）和统一 diff（s04）的原因。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│  GetEdits(reply)  — a line-by-line state machine             │
│                                                              │
│   reply lines                                                │
│      │                                                       │
│      ▼      fence? ──no──▶ inside block? ──yes──▶ body += ln │
│   ┌──────┐                      │ no                         │
│   │ scan │                      ▼                            │
│   └──────┘            prose: note `chat-file` → saw_fname    │
│      │ fence?                                                │
│      ▼ yes                                                   │
│   inside? ──yes──▶ EMIT (fname, body); reset ───┐           │
│      │ no                                         │           │
│      ▼                                            │           │
│   open: fname = cleanFilename(prev line)          │           │
│         └─ empty? → saw_fname / sole chat / ERR   │           │
│                                                   ▼           │
│                                  refine: block > saw > chat   │
│                                          → []Edit{Path,Replace}│
└──────────────────────────────────────────────────────────────┘
```

核心约 45 行（节选自 [`agents/s02-wholefile-format/wholefile.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s02-wholefile-format/wholefile.go)）：

```go
for i, raw := range lines {
	line := raw
	isFence := strings.HasPrefix(line, c.fence[0]) || strings.HasPrefix(line, c.fence[1])

	switch {
	case isFence && inBlock:
		// 闭围栏：产出正在累积的块并重置。
		edits = append(edits, blockEdit{fname: fname, source: source, body: strings.Join(body, "\n")})
		fname, source, body, inBlock = "", "", nil, false

	case isFence && !inBlock:
		// 开围栏：文件名在上一行。
		if i > 0 {
			fname = cleanFilename(lines[i-1])
			source = "block"
			if len(fname) > 250 { // Issue #1232：荒唐的名字 -> 丢弃
				fname = ""
			}
			// 把假的 "path/to/" 前缀塌缩成 basename。
			if fname != "" && !c.isChatFile(fname) && c.isChatFile(filepath.Base(fname)) {
				fname = filepath.Base(fname)
			}
		}
		if fname == "" { // 裸围栏：按可靠度回退
			switch {
			case sawFname != "":
				fname, source = sawFname, "saw"
			case len(c.chatFiles) == 1:
				fname, source = c.chatFiles[0], "chat"
			default:
				return nil, fmt.Errorf("no filename provided before %s in reply", c.fence[0])
			}
		}
		inBlock = true

	case inBlock:
		body = append(body, line) // 块内：文件内容

	default:
		// 散文：记下反引号包裹的 chat 文件，供后面的裸围栏回退使用。
		for _, word := range strings.Fields(line) {
			word = strings.TrimRight(word, ".:,;!")
			for _, cf := range c.chatFiles {
				if word == "`"+cf+"`" {
					sawFname = cf
				}
			}
		}
	}
}
```

**四个非显然之处**：

1. **文件名是围栏的*前*一行，不是后一行。** markdown 习惯把路径当作标题/标签写在代码块上方。我们在开围栏处读 `lines[i-1]`——并清理它，因为模型会用 `**`、反引号、`#` 或结尾 `:` 包裹它。漏了清理，`**foo.go**` 就会被当成跟 `foo.go` 不同的文件。
2. **`saw_fname` 来自*散文*，不是块。** 在任何围栏之前，我们扫描纯文本里反引号包裹的 chat 文件（`update `foo.go`:`）。这是后面回复里的*裸*围栏唯一还能找到目标的途径。它是回退，所以会输给围栏上方的真实文件名。
3. **回复中途结束的块也会产出。** 如果模型 token 耗尽、闭围栏缺失，我们保留这个不完整文件（循环后的 `if inBlock && fname != ""`）。上游也这么做——不完整的文件好过丢掉编辑。这是刻意的宽容。
4. **按来源优先级去重，不是按顺序。** `refine` 按 `block > saw > chat` 处理来源，所以对同一路径，有明确文件名的块总是赢过回退。最后出现的块*不会*自动胜出；可靠度才决定。

## What Changed / 与上一节的变化

s01 把解析器内联在 `Coder.Run` 里，返回一个 `Edit`，写一个文件。s02 把它抽成 `Coder` 接口背后可复用的 `WholeFileCoder`，并端到端走向多文件：

```diff
-// s01: inline, single block, path already known from argv
-func (c *Coder) extractEdit(path, reply string) (Edit, error) {
-	body, ok := extractCodeBlock(reply) // FIRST fenced block only
-	if !ok {
-		return Edit{}, fmt.Errorf("no fenced code block found in reply")
-	}
-	return Edit{Path: path, Search: "", Replace: body}, nil
-}
+// s02: a Coder implementation that parses MANY files and recovers each filename
+type WholeFileCoder struct {
+	root      string
+	chatFiles []string
+	fence     [2]string
+}
+func (c *WholeFileCoder) GetEdits(response string) ([]Edit, error) { /* state machine */ }
+func (c *WholeFileCoder) ApplyEdits(edits []Edit) (applied, failed []Edit, err error)
```

语义上：解析器不再是一次性的辅助函数，而成了一个*策略对象*。`main.go` 里的循环现在调用 `GetEdits`（多个编辑）并遍历 `ApplyEdits`，而不是写单个文件。`ApplyEdits` 还返回 `applied`/`failed` 切片，方便后面章节（s09 的反思）报告部分成功。`Edit` 结构没变——整文件仍然把 `Search` 留空；要到 s03，`Search` 才终于变成非空。

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s02-wholefile-format

# rewrite a single file (same shape as s01)
go run . hello.go "add a doc comment to main"

# the new trick: rewrite TWO files from one reply; -v shows token usage
go run . -v main.go util.go "move the parser into util.go"

# tests (no network — a fakeProvider stands in for the LLM)
go test -v ./...
```

期望输出形态：

```
# stderr, with -v:
[s02] provider=anthropic model=claude-sonnet-4-6 files=[main.go util.go]
[s02] stop_reason=end_turn in=412 out=96 tokens

# stdout:
Applied edit to main.go (32 bytes)
Applied edit to util.go (33 bytes)
```

一条回复写出两个文件——这就是关键。如果回复里有一个没有文件名的裸 ```` ``` ````，而你 chat 里有不止一个文件，你会看到 `no filename provided before ``` in reply`，且什么都不写。完整的示例记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s02-wholefile-format/testdata/expected.txt)。

## Upstream Source Reading / 上游源码阅读

aider 的对应物是整整 144 行的 `wholefile_coder.py`。`WholeFileCoder.get_edits`（状态机）和 `apply_edits`（写入器）就是 `wholefile.go` 逐行对照的对象；我们只去掉了 `do_live_diff`（流式 diff 展示，属于我们 s10 的范畴），去重逻辑则完全一致。

```upstream:aider/coders/wholefile_coder.py#L22-L128
class WholeFileCoder(Coder):
    edit_format = "whole"              # selects the prompt + this parser

    def get_edits(self, mode="update"):
        content = self.get_multi_response_content_in_progress()  # the raw reply
        chat_files = self.get_inchat_relative_files()            # editable files
        lines = content.splitlines(keepends=True)
        edits = []

        saw_fname = None      # a filename mentioned in prose
        fname = None          # None => outside a block; set => inside, accumulating
        fname_source = None
        new_lines = []
        for i, line in enumerate(lines):
            if line.startswith(self.fence[0]) or line.startswith(self.fence[1]):
                if fname is not None:                 # closing fence -> emit block
                    edits.append((fname, fname_source, new_lines))
                    fname, fname_source, new_lines = None, None, []
                    continue
                if i > 0:                             # opening fence -> name is line above
                    fname_source = "block"
                    fname = lines[i - 1].strip().strip("*").rstrip(":").strip("`").lstrip("#").strip()
                    if len(fname) > 250:              # Issue #1232: absurd name -> drop
                        fname = ""
                    if fname and fname not in chat_files and Path(fname).name in chat_files:
                        fname = Path(fname).name      # collapse bogus path/to/ prefix
                if not fname:                         # bare ``` with no usable name
                    if saw_fname:
                        fname, fname_source = saw_fname, "saw"
                    elif len(chat_files) == 1:
                        fname, fname_source = chat_files[0], "chat"
                    else:
                        raise ValueError("No filename provided before fence")
            elif fname is not None:
                new_lines.append(line)                # inside a block: file body
            else:
                for word in line.strip().split():     # prose: note backtick-quoted chat files
                    word = word.rstrip(".:,;!")
                    for chat_file in chat_files:
                        if word == f"`{chat_file}`":
                            saw_fname = chat_file
        if fname:                                     # reply ended mid-block -> still emit
            edits.append((fname, fname_source, new_lines))

        seen = set()                                  # de-dupe by source priority
        refined_edits = []
        for source in ("block", "saw", "chat"):
            for fname, fname_source, new_lines in edits:
                if fname_source != source or fname in seen:
                    continue
                seen.add(fname)
                refined_edits.append((fname, fname_source, new_lines))
        return refined_edits

    def apply_edits(self, edits):
        for path, fname_source, new_lines in edits:
            full_path = self.abs_root_path(path)
            self.io.write_text(full_path, "".join(new_lines))  # our s02: os.WriteFile
```

**对照阅读要点**：

- **文件名清理逐字节一致。** 上游链式调用 `.strip("*").rstrip(":").strip("`").lstrip("#")`；我们的 `cleanFilename` 以相同顺序做相同裁剪。把它拆成一个具名函数只是为了可读性。
- **`keepends=True` vs. 我们按 `\n` split。** 上游保留行终止符，于是 `"".join(new_lines)` 能精确复原文件。我们用 `strings.Split` 按 `\n` 切、再用 `\n` join 回去，对 `\n` 结尾的文件（常见情况）等价；真正的移植应像上游那样保留 `\r\n`。
- **省略了 `do_live_diff`。** 上游的 `get_edits(mode="diff")` 在响应流式返回时渲染增量 diff。那属于流式输出（我们的 s10），所以 s02 只实现 `mode="update"`。
- **错误分支被保留，没有抹平。** 多个 chat 文件 + 无名围栏时，上游 `raise ValueError`，我们 `return ...err`。我们本可以按 diff 大小猜（上游自己在 L81 的 TODO），但镜像这个诚实的失败能让课程更清晰。
- **`apply_edits` 刻意是"近乎空操作"级别的写入。** 上游和我们都只是 join + write。这不是偷懒——这是整文件格式的论点，而到 s03 它就不再成立了。

**想读更多**：从 `aider/coders/wholefile_coder.py` 的 `get_edits` 入手，再对比 `aider/coders/editblock_coder.py` 的 `find_original_update_blocks`（L439-560），跟着它进 `do_replace`（L364）和 `replace_most_similar_chunk`（L157）。这条线就是 s02 → s03（SEARCH/REPLACE）→ s04（统一 diff）的真实代码地图，那里解析变难、"落盘"也不再是平凡写入。

---

**下一节预告**：s03 把整文件重写换成 SEARCH/REPLACE 块，让模型只动会变的那几行——`Edit.Search` 由此变成非空，逼出一个模糊匹配器，好在空白漂移时也能应用编辑。
