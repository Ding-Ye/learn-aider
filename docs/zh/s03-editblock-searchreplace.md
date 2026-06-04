---
title: "s03 · 搜索/替换编辑块"
chapter: 3
slug: s03-editblock-searchreplace
est_read_min: 14
---

# s03 · 搜索/替换编辑块

> 教什么：*SEARCH/REPLACE 编辑格式*及其**模糊匹配器**——aider 的核心机制。模型只输出"要查找的几行"和"要替换成的几行"，我们在文件里定位 SEARCH 文本（容忍空白漂移）并替换。这是 aider 默认的 `diff` 格式，单独成章是因为这个分层匹配器是整个项目里最吃重的一处巧思。

---

## Problem / 问题

s02 教的是**整文件**格式：要改一行，模型就把整个文件重新放进围栏块里，我们整体覆盖文件。它的应用很简单（`os.WriteFile`），但有两个真实代价。一是**费 token**——在一个 500 行的文件里修一行，意味着模型每一轮都要读完并重写全部 500 行；二是**破坏性**——模型在重打文件时只要写错一行（丢个 import、改坏一条注释），就会悄无声息地覆盖掉本来正确的代码。

aider 的答案、也是它最具辨识度的机制，就是 **SEARCH/REPLACE 块**：模型只写出它想查找的那几行，以及要替换成的那几行。这更省、也更安全——但它引入了一个硬问题。模型是凭对所读内容的*记忆*生成 SEARCH 文本的，而不是逐字节复制，所以它会不停地重新缩进、把 tab 换成空格、或丢掉一个空行。如果"应用"要求逐字节精确匹配，相当大一部分本来正确的编辑都会失败。本章真正的工作量，就在那个让 SEARCH/REPLACE 变得可用的**模糊匹配器**上。

## Solution / 解决方案

一个 SEARCH/REPLACE 编辑就是一个三元组 `(path, search, replace)`。模型对每处改动输出：单独一行的文件路径，然后是 `<<<<<<< SEARCH`、旧的几行、`=======`、新的几行、`>>>>>>> REPLACE`。**解析**逐行扫描回复，把这些三元组抽出来。**应用**在文件里定位 `search` 那几行，把 `replace` 拼接进去顶替它们。

让它跑起来的关键是一个**分层匹配器**：先试最便宜、最严格的匹配，只有失败时才退到更宽容的匹配。具体是：

1. **精确匹配**（`perfectReplace`）——在文件里找到一段与 SEARCH 逐字节相等的连续行。顺风路径。
2. **空白灵活**（`replacePartWithMissingLeadingWhitespace`）——同样的几行，但模型用错了缩进（通常是整体偏移）。把两侧都向左缩，让 SEARCH 在文件上滑动，逐处问"除了一个*一致的*前导空白前缀以外，它们是否相等？"，若是，就把文件真正的缩进重新加回 REPLACE。这一层正是用来容忍模型最常犯的错误。
3. **省略**（`tryDotDotDots`）——模型用 `...` 代替了没改动的中间代码；把未省略的片段逐段拼接。

失败是**软失败**：匹配不上的 SEARCH 保持文件原样并被报告，绝不会被悄悄地只贴一半。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│                  reply text from the model                   │
│                          │                                   │
│                          ▼                                   │
│   GetEdits ── scan lines, on <<<<<<< SEARCH pull:            │
│       filename (line above) · SEARCH body · REPLACE body     │
│                          │                                   │
│                          ▼   []Edit{Path, Search, Replace}   │
│              ┌───────────────────────────┐                  │
│   ApplyEdits │ per edit: locate Search in │                  │
│              │ the file via TIERED match: │                  │
│              │  1 exact (perfectReplace)  │── hit ──▶ splice │
│              │  2 whitespace-flexible     │── hit ──▶ splice │
│              │  3 "..." elision           │── hit ──▶ splice │
│              │  else ─────────────────────┼── miss ─▶ failed │
│              └───────────────────────────┘     (file kept)   │
└──────────────────────────────────────────────────────────────┘
```

吃重的匹配器（节选自 [`agents/s03-editblock-searchreplace/editblock.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s03-editblock-searchreplace/editblock.go)）：

```go
// replaceMostSimilarChunk is the TIERED matcher — the load-bearing fuzzy logic.
func replaceMostSimilarChunk(whole, part, replace string) (string, bool) {
	wholeLines := splitKeepNL(whole)
	partLines := splitKeepNL(part)
	replaceLines := splitKeepNL(replace)

	// Tier 1+2: exact match, then whitespace-flexible match.
	if res, ok := perfectOrWhitespace(wholeLines, partLines, replaceLines); ok {
		return res, true
	}

	// GPT sometimes prepends a spurious blank line to SEARCH; retry without it.
	if len(partLines) > 2 && strings.TrimSpace(partLines[0]) == "" {
		if res, ok := perfectOrWhitespace(wholeLines, partLines[1:], replaceLines); ok {
			return res, true
		}
	}

	// Tier 3: the model elided unchanged code with "..." lines.
	if res, ok := tryDotDotDots(whole, part, replace); ok {
		return res, true
	}
	return "", false
}

// matchButForLeadingWhitespace: do these lines match once you ignore leading
// whitespace AND the added indent is the SAME single prefix on every non-blank
// line? If so, return that prefix — the file's true indentation for this block.
func matchButForLeadingWhitespace(wholeLines, partLines []string) (string, bool) {
	if len(wholeLines) != len(partLines) {
		return "", false
	}
	for i := range wholeLines { // non-whitespace content must agree
		if strings.TrimLeft(wholeLines[i], " \t") != strings.TrimLeft(partLines[i], " \t") {
			return "", false
		}
	}
	adds := map[string]struct{}{} // the added prefix must be ONE consistent value
	for i := range wholeLines {
		if strings.TrimSpace(wholeLines[i]) == "" {
			continue
		}
		adds[wholeLines[i][:len(wholeLines[i])-len(partLines[i])]] = struct{}{}
	}
	if len(adds) != 1 {
		return "", false
	}
	for p := range adds {
		return p, true
	}
	return "", false
}
```

**四个非显然之处**：

1. **空白层要求偏移是*一致的*。** 当缩进差不是单一一致的前缀时（`len(adds) != 1`），`matchButForLeadingWhitespace` 会拒绝该候选。正是这个守卫，阻止了匹配器把编辑"成功"贴到那些只是*看起来*相似的行上——它只原谅模型真正会犯的错（把整块按同一量平移）。
2. **行保留各自的换行符。** `splitKeepNL` 对应 Python 的 `splitlines(keepends=True)`：每行都带着自己的 `\n`，于是用 `strings.Join(..., "")` 把切片拼回去时，不会意外合并或丢掉行边界。
3. **空 SEARCH 是创建/追加，不是匹配。** 当 `Search` 为空时，`applyOne` 直接短路：追加到已有文件或新建一个，正如上游 `do_replace` 的 `not before_text.strip()` 分支。此时不做任何定位。
4. **我们停在 dotdotdots 这一层——这是有意的。** 上游 `replace_most_similar_chunk` 后面还有一个编辑距离层，但它在到达之前就 `return` 了（L183）；那段代码在上游也是死代码。真正干活的是空白层，所以我们移植存活的几层，跳过那段死的。

## What Changed (vs. s02) / 与 s02 的变化

格式从"重写整个文件"翻转成"找到这几行、替换它们"。`Edit.Search` 变为非空，而*应用*一处编辑也不再是一行 `os.WriteFile`，而成了那个分层匹配器。

```diff
 // Edit is one parsed file change.
 type Edit struct {
 	Path    string
-	Search  string // s02: always "" — whole-file replace
-	Replace string // s02: the ENTIRE new file
+	Search  string // s03: the lines to LOCATE in the file ("" => create/append)
+	Replace string // s03: the lines to splice in their place
 }

-// s02 apply: overwrite the whole file. All intelligence is in the parser.
-func (c *Coder) applyWholeFile(e Edit) error {
-	return os.WriteFile(e.Path, []byte(e.Replace), 0o644)
-}
+// s03 apply: LOCATE e.Search (tolerating whitespace drift), then splice.
+func (c *Coder) applyOne(e Edit) (bool, error) {
+	content, _ := os.ReadFile(c.absPath(e.Path))
+	updated, ok := replaceMostSimilarChunk(string(content), e.Search, e.Replace)
+	if !ok {
+		return false, nil // soft failure: file left untouched, edit -> `failed`
+	}
+	return true, os.WriteFile(c.absPath(e.Path), []byte(updated), 0o644)
+}
```

语义上：s02 里所有难点都在*解析器*（恢复文件名、处理多个块），应用是空操作。s03 里解析器同样简单，但应用变成了难的那一半——因为模型的 SEARCH 文本和文件的字节不会精确相等。本章的重心从"解析"移到了"匹配"。

## Try It / 动手试一试

```bash
cd agents/s03-editblock-searchreplace

# set ONE provider's key
export ANTHROPIC_API_KEY=sk-ant-...

# read greet.go, ask for a surgical change, apply the SEARCH/REPLACE blocks
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
[s03] provider=anthropic model=claude-sonnet-4-6 url=
[s03] sending 118 bytes of greet.go + instruction "change the greeting to 'hi there'"
[s03] stop_reason=end_turn in=611 out=64 tokens

# stdout:
Applied edit to greet.go
```

如果 SEARCH 文本什么都没匹配上，你会看到 `FAILED to match SEARCH in greet.go (file left untouched)` 以及非零退出——文件永远不会被改一半。（完整的示例运行记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s03-editblock-searchreplace/testdata/expected.txt)。）

## Upstream Source Reading / 上游源码阅读

aider 的解析器是 `find_original_update_blocks`（一个生成器），匹配器是 `replace_most_similar_chunk` 加上那几个空白辅助函数。与 s03 的主要差别：上游还会特判 ```` ```bash ```` 这类 shell 块、用 `difflib.get_close_matches` 把文件名对在场文件集合做模糊匹配、并在解析出错时带上"已处理前缀"重新抛出异常，让模型看清到底错在哪。s03 保留吃重的解析 + 匹配，舍弃这些附加项。

```upstream:aider/coders/editblock_coder.py#L439-L535
def find_original_update_blocks(content, fence=DEFAULT_FENCE, valid_fnames=None):
    lines = content.splitlines(keepends=True)
    i = 0
    current_filename = None  # sticky: a 2nd block can omit the filename

    head_pattern = re.compile(HEAD)        # ^<{5,9} SEARCH>?\s*$  (lenient fence)
    divider_pattern = re.compile(DIVIDER)  # ^={5,9}\s*$
    updated_pattern = re.compile(UPDATED)  # ^>{5,9} REPLACE\s*$

    while i < len(lines):
        line = lines[i]
        # (shell ```bash handling omitted — s03 doesn't run shell commands)

        if head_pattern.match(line.strip()):              # a SEARCH marker
            try:
                # filename is on one of the up-to-3 lines ABOVE the marker;
                # if the next line is already the DIVIDER, SEARCH is empty =>
                # "create a new file", so don't require a known name.
                if i + 1 < len(lines) and divider_pattern.match(lines[i + 1].strip()):
                    filename = find_filename(lines[max(0, i - 3) : i], fence, None)
                else:
                    filename = find_filename(lines[max(0, i - 3) : i], fence, valid_fnames)
                if not filename:
                    filename = current_filename or _raise_missing_filename()
                current_filename = filename

                original_text = []                        # SEARCH body up to =======
                i += 1
                while i < len(lines) and not divider_pattern.match(lines[i].strip()):
                    original_text.append(lines[i]); i += 1
                if i >= len(lines) or not divider_pattern.match(lines[i].strip()):
                    raise ValueError(f"Expected `{DIVIDER_ERR}`")

                updated_text = []                          # REPLACE body up to >>>>>>>
                i += 1
                while i < len(lines) and not (
                    updated_pattern.match(lines[i].strip())
                    or divider_pattern.match(lines[i].strip())
                ):
                    updated_text.append(lines[i]); i += 1
                if i >= len(lines) or not (...):
                    raise ValueError(f"Expected `{UPDATED_ERR}` or `{DIVIDER_ERR}`")

                yield filename, "".join(original_text), "".join(updated_text)
            except ValueError as e:
                processed = "".join(lines[: i + 1])
                raise ValueError(f"{processed}\n^^^ {e.args[0]}")
        i += 1
```

**对照阅读要点**：

- **宽容的围栏。** 上游的 `HEAD`/`DIVIDER`/`UPDATED` 正则接受 5-9 个尖括号/等号，所以略有偏差的围栏也能解析。s03 用 trim + 前缀检查（`<<<<<<<`、`=======`、`>>>>>>>`）获得同样的宽容——不依赖正则，覆盖同样的真实情况。
- **文件名恢复。** 上游 `find_filename`（L538）用 `difflib.get_close_matches(cutoff=0.8)` 把恢复出的名字对 `valid_fnames` 做模糊匹配。s03 直接相信写出来的名字（带扩展名或带斜杠的启发式），因为我们还没有携带在场文件集合——那要等 s06 的命令层。
- **空 SEARCH = 创建文件。** 两边都检测"HEAD 后紧跟 DIVIDER"并走创建路径（上游 `do_replace` 会 touch 文件；s03 的 `applyOne` 写入/追加）。这就是 `Edit.Search == ""` 之所以有意义的原因。
- **巧思在匹配器里。** 上面的节选只是解析。模糊那部分是 `replace_most_similar_chunk`（L157-188）→ `perfect_or_whitespace`（L134-144）→ `replace_part_with_missing_leading_whitespace`（L243-273）→ `match_but_for_leading_whitespace`（L276-293）。s03 把这四个都移植了。
- **一个我们故意保留的"正确但不完美"设计。** 上游 `replace_most_similar_chunk` 在它的编辑距离层*之前*就 `return` 了（L183）——那一层是死代码。s03 与之一致：我们停在 dotdotdots 层，因为真正物有所值的是空白层。

**想读更多**：从 `aider/coders/editblock_coder.py` 的 `find_original_update_blocks`（上面的解析）入手，跟着 `do_replace`（L364）进 `replace_most_similar_chunk`（L157），再读 `match_but_for_leading_whitespace`（L276）。这条线——解析 → do_replace → 分层匹配——就是 s03 → s04（统一 diff 复用同一个匹配器）→ s05（按格式定制提示）的真实代码地图。

---

**下一节预告**：s04 把本节的格式进一步演化：解析标准的 `@@` 块统一 diff，从 hunk 推导出 before/after 文本，并复用 s03 的搜索/替换引擎，即使行号有偏移也能贴上去。
