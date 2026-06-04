---
title: "s04 · 统一 diff 编辑格式"
chapter: 4
slug: s04-unified-diff-format
est_read_min: 14
---

# s04 · 统一 diff 编辑格式

> 教什么：*统一 diff 编辑格式*——也就是 `git diff` 的样子（`--- / +++ / @@` 加 `-`/`+` 行），作为模型表达编辑的第三种方式。我们解析 hunk，推导出每个 hunk 的 before/after 文本，再复用 s03 的模糊匹配器来应用它——**靠 hunk 的上下文行来定位，并刻意忽略 `@@` 行号**，因为那正是 LLM 会算错的部分。

---

## Problem / 问题

s03 给了我们 SEARCH/REPLACE 块，以及让它们贴得上去的模糊匹配器。但 SEARCH/REPLACE 是 aider *自创*的围栏语法（`<<<<<<< SEARCH` … `>>>>>>> REPLACE`）。许多模型——尤其是大量在 GitHub 上训练过的——对 **统一 diff** 格式要熟练得多，那正是 `git diff` 打印、`patch(1)` 消费的东西。让这些模型用它们本就"会说"的格式，能得到更干净、更可靠的编辑。所以 aider 也内置了一个 `udiff` coder。

难点恰恰在统一 diff 里看起来最权威的那部分：hunk 头 `@@ -12,4 +13,5 @@`，它声称改动从第 12 行开始、跨 4 行旧 / 5 行新。LLM 是*凭记忆*靠数行算出这些数字的——而数数恰恰是语言模型最不擅长的。这些行号几乎总是略有出入。像 `patch(1)` 这样严格的打补丁器会直接拒绝该 hunk。本章真正的工作量，就在于*无视*这些错误行号也能把模型生成的 diff 贴上去——做法是把数字扔掉，转而锚定在模型真正能正确引用的东西上：周围的上下文行。

## Solution / 解决方案

一个统一 diff hunk 就是一串行，每行靠首字符打标：前导空格表示未改动的**上下文**行，`-` 表示删除行，`+` 表示新增行。**解析**在回复里扫描 ```diff 围栏，把围栏体在 `@@` 边界处切成 `(path, hunk)` 对。路径来自 `--- a/x` / `+++ b/x` 头（剥掉 git 的 `a/`、`b/` 前缀）。

关键洞察是：一个 hunk 退化成了 s03 的问题。**推导** before/after 文本（`hunkToBeforeAfter`）会给你：`before` = 上下文 + `-` 行（文件现在的样子），`after` = 上下文 + `+` 行（它应该的样子）。到这一步，应用一个 hunk *就是*搜索/替换——定位 `before`，拼接 `after`——于是 s03 的分层匹配器原样复用。由此引出三个决策：

1. **完全忽略 `@@` 行号。** 我们从不读 `-a,b +c,d`。hunk 靠在文件里匹配它的 `before` 文本（上下文 + 删除）来定位，那段文本实际落在哪儿就在哪儿。
2. **让上下文匹配保持空白灵活。** 模型会重新缩进它引用的上下文，正是 s03 那个错误——所以同样的 精确 → 容忍空白 的分层适用。
3. **丢弃纯上下文 hunk；失败保持软失败。** 没有 `-`/`+` 的 hunk 什么都不改，直接丢掉。上下文找不到的 hunk 保持文件不动，并被报告进 `failed`。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│              reply text  ──►  ```diff … ``` fence             │
│                          │                                   │
│         findDiffs / processFencedBlock                       │
│                          │   path from --- a/x +++ b/x        │
│                          ▼   hunks split at @@ ... @@         │
│            []Hunk{Path, Lines: " ctx" "-old" "+new"}         │
│                          │                                   │
│         hunkToBeforeAfter │  ' '→both  '-'→before  '+'→after  │
│                          ▼   before = ctx+removed             │
│                              after  = ctx+added               │
│              ┌───────────────────────────┐                  │
│   ApplyEdits │ locate `before` by CONTEXT │  (@@ nums ignored)│
│              │ via s03 tiered matcher:    │                  │
│              │  exact → whitespace-flex   │── hit ──▶ splice │
│              │  else ─────────────────────┼── miss ─▶ failed │
│              └───────────────────────────┘     (file kept)   │
└──────────────────────────────────────────────────────────────┘
```

"推导后复用" 这一步（节选自 [`agents/s04-unified-diff-format/udiff.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s04-unified-diff-format/udiff.go)）：

```go
// hunkToBeforeAfter splits a hunk's diff lines into BEFORE (context + removed)
// and AFTER (context + added). This turns a unified-diff hunk into the s03
// search/replace problem. Upstream: hunk_to_before_after (L403-429).
func hunkToBeforeAfter(h Hunk) (before, after string) {
	var b, a strings.Builder
	for _, line := range h.Lines {
		op := byte(' ')
		body := line
		if len(line) >= 2 {
			op, body = line[0], line[1:]
		}
		switch op {
		case ' ': // context: an anchor present on BOTH sides
			b.WriteString(body)
			a.WriteString(body)
		case '-': // removed: BEFORE only
			b.WriteString(body)
		case '+': // added: AFTER only
			a.WriteString(body)
		}
	}
	return b.String(), a.String()
}

// applyOne locates the hunk by its context (NOT the @@ numbers) and splices.
func (c *Coder) applyOne(h Hunk) (bool, error) {
	full := c.absPath(h.Path)
	before, after := hunkToBeforeAfter(h)

	// A hunk with only `+` lines has empty before-text: create / append.
	if strings.TrimSpace(before) == "" {
		existing, _ := os.ReadFile(full)
		return true, os.WriteFile(full, []byte(string(existing)+after), 0o644)
	}
	content, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // referenced a file we don't have -> soft failure
		}
		return false, err
	}
	// THE KEY STEP: reuse s03's tiered, whitespace-flexible matcher on the
	// hunk's context. The @@ -a,b +c,d numbers are never consulted.
	updated, ok := replaceMostSimilarChunk(string(content), before, after)
	if !ok {
		return false, nil // context not found -> soft failure, file untouched
	}
	return true, os.WriteFile(full, []byte(updated), 0o644)
}
```

**四个非显然之处**：

1. **`@@` 行号从不被读取。** `applyOne` 调用 `replaceMostSimilarChunk(content, before, after)`——它在整个文件里搜 `before` 文本。头里的行号（`-a,b +c,d`）在 `processFencedBlock` 里被跳过并丢弃。这正是全部要点：模型的行号算术不可靠，它引用的上下文则可靠。
2. **上下文行是吃重的，所以模型必须输出一些。** 一个*只有* `-`/`+`、周围没有 ` ` 行的 hunk，其 `before` 文本很单薄；如果它又短又不唯一，匹配就会有歧义而失败。这*就是为什么*系统提示要求带几行上下文——它们是锚点，不是装饰。
3. **纯 `+` 的 hunk 表示创建/追加。** 当 `before` 为空（无上下文、无删除）时，没有东西可定位，于是 `applyOne` 短路到写入/追加——与 s03 的空 SEARCH、上游 `do_replace` 是同一条创建文件路径。
4. **我们复用 s03 的匹配器，而非上游的 partial-hunk 机制。** 上游 `apply_hunk` 有一套精巧的回退（`apply_partial_hunk`），会逐步丢上下文行再重试。s04 刻意停在 s03 的 精确 → 空白 两层：吃重的思想（靠上下文定位、忽略数字、容忍缩进漂移）完全一致，而那套额外机制对讲清这一点并不必要。

## What Changed (vs. s03) / 与 s03 的变化

s03 从 SEARCH/REPLACE 围栏里解析出明确的 `(path, search, replace)`。s04 从一个 `git diff` 里解析出 `(path, hunk)`，再从 hunk 的 `-`/`+`/上下文行里**推导**出 search/replace。*应用器*——那个分层模糊匹配器——是同一份代码；变的只是 before/after 怎么得来。

```diff
-// s03: the model writes SEARCH and REPLACE explicitly; we parse them directly.
-func (c *Coder) GetEdits(reply string) ([]Edit, error) {
-	// scan for <<<<<<< SEARCH ... ======= ... >>>>>>> REPLACE
-	// each block IS an Edit{Path, Search, Replace}
-}
+// s04: the model writes a unified diff; we parse hunks, then DERIVE before/after.
+func (c *Coder) GetEdits(reply string) ([]Hunk, error) {
+	hunks := findDiffs(reply)          // ```diff fences -> []Hunk{Path, Lines}
+	// (filename made sticky across hunks)
+	return hunks, nil
+}
+
+// before = context + '-' lines ; after = context + '+' lines
+func hunkToBeforeAfter(h Hunk) (before, after string) { /* ... */ }

 // UNCHANGED from s03: locate `before` in the file, splice `after`.
 updated, ok := replaceMostSimilarChunk(string(content), before, after)
```

语义上：s03 的 SEARCH 段*就是* before 文本，直接写明了。s04 里 before 文本是隐含的——它是上下文行和 `-` 行加起来的结果——所以多了一步推导，但下游的应用完全相同。本章说明了"编辑格式"只是叠在同一个共享匹配引擎之上的一层薄薄的解析问题。

## Try It / 动手试一试

```bash
cd agents/s04-unified-diff-format

# set ONE provider's key
export ANTHROPIC_API_KEY=sk-ant-...

# read greet.go, ask for a change, apply the ```diff hunks
go run . greet.go "change the greeting to 'hi there'"

# -v prints the request/response shape on stderr
go run . -v greet.go "add error handling to readConfig"

# -root resolves parsed file paths under a directory
go run . -root /tmp greet.go "rename foo to bar"

# tests (no network — a fakeProvider stands in for the LLM)
go test -v ./...
```

期望输出形态：

```
# stderr, with -v:
[s04] provider=anthropic model=claude-sonnet-4-6 url=
[s04] sending 118 bytes of greet.go + instruction "change the greeting to 'hi there'"
[s04] stop_reason=end_turn in=611 out=72 tokens

# stdout:
Applied hunk to greet.go
```

如果某个 hunk 的上下文什么都没匹配上，你会看到 `FAILED to apply hunk to greet.go — its context did not match (file left untouched)` 以及非零退出——文件永远不会被改一半，而一个错误的 `@@` 数字也永远无所谓，因为它被忽略了。（完整的示例运行记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s04-unified-diff-format/testdata/expected.txt)。）

## Upstream Source Reading / 上游源码阅读

aider 的解析器是 `find_diffs` → `process_fenced_block`，推导步骤是 `hunk_to_before_after`。应用步骤（`do_replace` → `apply_hunk`）把推导出的 before/after 喂给 editblock coder 也在用的同一个 `flexible_search_and_replace` 引擎。与 s04 的主要差别：上游 `apply_hunk` 多了一个 `apply_partial_hunk` 回退，会逐步丢上下文行来重试顽固的 hunk，并对相同 hunk 去重、发出为模型量身定做的 `UnifiedDiffNoMatch` / `UnifiedDiffNotUnique` 消息。s04 保留吃重的 解析 + 推导 + 上下文匹配，舍弃 partial-hunk 重试。

```upstream:aider/coders/udiff_coder.py#L337-L400
def process_fenced_block(lines, start_line_num):
    # Find the closing ``` fence.
    for line_num in range(start_line_num, len(lines)):
        if lines[line_num].startswith("```"):
            break

    block = lines[start_line_num:line_num]
    block.append("@@ @@")              # sentinel so the LAST hunk gets flushed

    # A leading `--- a/x` / `+++ b/x` pair names the file. Strip git's a//b/ (or
    # /dev/null) prefixes to recover the real path.
    if block[0].startswith("--- ") and block[1].startswith("+++ "):
        a_fname = block[0][4:].strip()
        b_fname = block[1][4:].strip()
        if (a_fname.startswith("a/") or a_fname == "/dev/null") and b_fname.startswith("b/"):
            fname = b_fname[2:]
        else:
            fname = b_fname
        block = block[2:]
    else:
        fname = None                   # sticky filename, filled in by get_edits

    edits = []
    keeper = False                     # does this hunk contain a -/+ change?
    hunk = []
    for line in block:
        hunk.append(line)
        if len(line) < 2:
            continue

        # A fresh `--- / +++` header mid-block: flush the hunk, switch file.
        if line.startswith("+++ ") and hunk[-2].startswith("--- "):
            hunk = hunk[:-3] if hunk[-3] == "\n" else hunk[:-2]
            edits.append((fname, hunk))
            hunk = []
            keeper = False
            fname = line[4:].strip()
            continue

        op = line[0]
        if op in "-+":
            keeper = True              # this hunk actually changes something
            continue
        if op != "@":
            continue                   # a normal context line — accumulate
        if not keeper:
            hunk = []                  # `@@` but no change yet: drop pure context
            continue

        hunk = hunk[:-1]               # drop the `@@` line, flush the hunk
        edits.append((fname, hunk))
        hunk = []
        keeper = False

    return line_num + 1, edits
```

**对照阅读要点**：

- **`@@` 行是*分隔符*，不是数据。** 上游只把 `@@` 当作"flush 当前 hunk"的信号（`hunk = hunk[:-1]` 把 `@@` 行本身丢掉）。里面的 `-a,b +c,d` 数字从不被解析。s04 同理——这正是为什么错误的头不会毁掉一处编辑。
- **`"@@ @@"` 哨兵。** 两边都在 block 末尾追加一个假的 `@@ @@`，好让最后一个 hunk 走与中间 hunk 相同的 flush 代码路径，而无需为"块结束"特判。s04 把这个小技巧原样移植。
- **`keeper` 丢弃纯上下文 hunk。** 当一个 hunk 走到 `@@` 时 `keeper == False`（没见过 `-`/`+`），说明它只有上下文；上游会 `hunk = []` 跳过它。s04 的 `hunkChanges` 加 `processFencedBlock` 里的 `keeper` 检查与此对应——一个空操作 hunk 永远到不了文件。
- **巧思在应用路径里。** 这段节选只是解析。应用路径是 `do_replace`（L121-149）→ `apply_hunk`（L151-199）→ `flexible_search_and_replace`（search_replace.py）。s04 把最后那一步换成我们已经移植的 s03 `replaceMostSimilarChunk`——同样的活（上下文匹配、容忍空白），代码更少。
- **一个我们故意保留的简化。** 上游 `apply_partial_hunk`（L282-309）通过一行行甩掉上下文来重试失败的 hunk。s04 略去它：精确 → 空白 两层已覆盖本章存在的目的所要讲的情形，而加上那个重试循环只会模糊核心思想（靠上下文定位、忽略数字）。

**想读更多**：从 `aider/coders/udiff_coder.py` 的 `find_diffs`（扫描）入手，跟着 `process_fenced_block`（上面的节选）进 `hunk_to_before_after`（L403），再到 `do_replace`（L121）→ `apply_hunk`（L151）。这条线——解析 → 推导 → 上下文匹配——就是 s03（共享匹配器）→ s04 → s05（按格式定制提示，`udiff_prompts.py` 在那里定义模型被告知什么）的真实代码地图。

---

**下一节预告**：s05 不再把每种格式的指令内联硬编码，而是把它们抽进一张按格式索引的提示表里，于是同一个循环只要换系统提示，就能驱动整文件、SEARCH/REPLACE 和统一 diff 三种格式。
