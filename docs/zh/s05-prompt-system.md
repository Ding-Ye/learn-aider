---
title: "s05 · 按格式定制的提示系统"
chapter: 5
slug: s05-prompt-system
est_read_min: 13
---

# s05 · 按格式定制的提示系统

> 教什么：*提示系统*——aider 是怎么让模型输出指定的编辑格式的。每种格式的系统提示词都由可复用的**片段**拼成（`MainSystem` + `ExampleMessages` + `SystemReminder` + lazy/overeager 提醒），其中的**占位符**（`{fence}`、`{final_reminders}`）在发送时才填。这是 s02–s04 缺失的另一半：解析器之所以能用，是因为有一段提示词教会了模型那种格式。

---

## Problem / 问题

s02、s03、s04 各自实现了一个可用的编辑格式**解析器**：整文件清单、SEARCH/REPLACE 块、统一 diff。每一章都默默假设模型会*用那种格式回复*——为了让这件事发生，每一章都在自己的循环里写死了一个小小的 `systemPrompt()` 函数（s03 的 `editBlockSystemPrompt()`）。这在一次只支持一种格式时能用，但它把真正的依赖藏了起来，而且无法扩展：解析逻辑和诱导格式的提示词被焊死在一起，三段几乎重复的提示词各自漂移，"再支持第四种格式"意味着再写一个专用函数、再往循环里穿一条新分支。

更深一层的问题是：提示词是*因*，解析器是*果*。模型输出 SEARCH/REPLACE 块，**是因为系统提示词这么要求它、并给它看了一个例子**。如果你想搞清楚 s02–s04 为什么行为各异，得看提示词，而不是解析器——可现在这些提示词散落各处、写死在代码里、还和控制流纠缠在一起。这一章把它们抽取到一个可插拔的地方。

## Solution / 解决方案

把提示词当成**数据，而不是代码**。上游 aider 给每个 coder 一个 `gpt_prompts` 对象；我们用一个 `Prompts` 结构体来对应它，每个字段对应系统消息里一个可复用的**片段**：`MainSystem`（核心指令）、`ExampleMessages`（few-shot 轮次）、`SystemReminder`（逐条规则重述），再加上共享的 `LazyReminder` / `OvereagerReminder` 提醒。每种 `EditFormat` 返回自己的 `Prompts` 值；一个统一的渲染器从拿到的那个值拼出最终的系统串。

三个决策撑起整个设计：

1. **占位符在发送时填，不在定义时填。** `{fence}` 不能是常量——当被编辑的代码本身含有 ```` ``` ```` 时，aider 会换用更长的围栏（或自定义标记）。`{final_reminders}` 则按模型开关。所以这些片段携带 `{fence[0]}` / `{final_reminders}` 这类 token，由 `Render(vars)` 一步替换掉。
2. **格式选择就是查表。** `PromptsFor(format)` 把一个 `EditFormat` 映射到它的提示集。切换格式改的是一个*值*；循环从不对格式做分支。
3. **few-shot 示例是独立的对话轮次，不属于系统串。** `RenderExamples` 把模板化的示例变成真正的 user/assistant `Message`，拼在对话前面，和上游一样——这样模型把它们当作之前的对话来读。

## How It Works / 工作原理

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  EditFormat ("diff")                                            │
│        │                                                       │
│        ▼   PromptsFor(format)                                  │
│  ┌──────────────────────────────────────────┐                 │
│  │ Prompts{ MainSystem, ExampleMessages,     │   一个字段       │
│  │          SystemReminder, Lazy/Overeager } │   对应一个片段   │
│  └──────────────────────────────────────────┘                 │
│        │                         │                             │
│  Render(vars)              RenderExamples(vars)                │
│   替换 {fence}              替换 {fence}                        │
│   + {final_reminders}       逐条示例轮次                       │
│        │                         │                             │
│        ▼                         ▼                             │
│  req.System (string)      []Message (user/assistant ...)      │
│        └──────────────┬──────────────┘                        │
│                       ▼                                        │
│            CreateMessageRequest  ──▶ Provider ──▶ 模型输出      │
│                                          所要求的格式          │
└────────────────────────────────────────────────────────────────┘
```

渲染器与它的关键守卫（节选自 [`agents/s05-prompt-system/prompts.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s05-prompt-system/prompts.go)）：

```go
// Render assembles the final SYSTEM PROMPT for one format, placeholders filled.
// Examples are NOT part of this string — they are separate turns (RenderExamples).
func (p Prompts) Render(vars PromptVars) string {
	final := p.finalReminders(vars)
	var sb strings.Builder
	sb.WriteString(substitute(p.MainSystem, vars, final))
	if p.SystemReminder != "" {
		sb.WriteString("\n\n")
		sb.WriteString(substitute(p.SystemReminder, vars, final))
	}
	return sb.String()
}

// finalReminders builds the {final_reminders} value from the per-send flags.
func (p Prompts) finalReminders(vars PromptVars) string {
	var parts []string
	if vars.Overeager && p.OvereagerReminder != "" {
		parts = append(parts, p.OvereagerReminder)
	}
	if vars.Lazy && p.LazyReminder != "" {
		parts = append(parts, p.LazyReminder)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n") + "\n"
}

// substitute replaces every KNOWN {placeholder}; unknown tokens are left alone,
// so literal braces in example code (f"Hey {name}") survive untouched.
func substitute(s string, vars PromptVars, finalReminders string) string {
	lang := vars.Language
	if lang == "" {
		lang = "English"
	}
	r := strings.NewReplacer(
		"{fence[0]}", vars.Fence[0],
		"{fence[1]}", vars.Fence[1],
		"{final_reminders}", finalReminders,
		"{language}", lang,
		"{lazy_prompt}", pick(vars.Lazy, lazyReminder),
		"{overeager_prompt}", pick(vars.Overeager, overeagerReminder),
	)
	return r.Replace(s)
}
```

**四个非显然之处**：

1. **用显式替换器，而不是 `text/template`。** 提示词里到处是示例代码中的字面花括号（`print(f"Hey {name}")`）。Go 模板会把 `{name}` 当作一个 action 而报错；而固定的 `strings.Replacer` 只动我们已知的六个 token，其余原样保留。这对应上游刻意用 `str.format` 配一个*已知的小* key 集。
2. **未知 `{token}` 就是"缺失变量"的处理方式。** 因为替换器只认六个 key，任何散落的 `{...}` 都会原样穿过——这正是含花括号的代码所需要的，也是测试所钉死的行为。
3. **`{final_reminders}` 会干净地坍缩为空。** 当两个提醒都没启用时，`finalReminders` 返回 `""`，占位符随之消失，不留空行疤痕。这些提醒被所有格式共享（在 base 上只定义一次），所以改措辞只需改一处。
4. **示例刻意放在系统串之外。** 把 few-shot 轮次塞进系统提示词，会让模型把它们当成*关于文本的指令*；而作为真正的 user/assistant 轮次发送，会让模型把它们当成*应当模仿的之前对话*。这就是 `Render` 与 `RenderExamples` 分开的原因。

## What Changed (vs. s04) / 与 s04 的变化

s02–s04 各自写死了一个 `systemPrompt()` 并直接调用。s05 用一个 `Prompts` 结构体 + 一张选择表取而代之，于是格式选择现在*驱动*系统消息，而不再被烤进某一个函数里。

```diff
-// s02..s04: each chapter hardcoded ONE prompt function in its loop.
-func editBlockSystemPrompt() string {
-	return "You are a coding assistant that edits files with SEARCH/REPLACE blocks.\n" +
-		"..." // one format, welded into the loop
-}
-
-req := CreateMessageRequest{System: editBlockSystemPrompt(), Messages: msgs}
+// s05: prompts are data; one struct per format, one renderer for all.
+type Prompts struct {
+	MainSystem      string
+	ExampleMessages []ExampleMessage
+	SystemReminder  string
+	LazyReminder, OvereagerReminder string
+}
+
+// Format choice selects the prompt; the call site never branches on format.
+sys, _ := SystemPrompt(format, vars)        // PromptsFor(format).Render(vars)
+msgs := prompts.RenderExamples(vars)        // few-shot turns, fences filled
+req := CreateMessageRequest{System: sys, Messages: append(msgs, userTurn)}
```

语义上：在 s02–s04 里，提示词是某个解析器的实现细节，既看不见也换不掉。在 s05 里，提示词成了一个一等的、按格式区分的值。编辑格式与诱导它的提示词终于显式化、可插拔了——加一种格式就是"一个提示集 + 一个选择分支"，而且你不用跑任何东西就能*查看*到底是什么在驱动模型。

## Try It / 动手试一试

```bash
cd agents/s05-prompt-system

# 查看模式（无网络、无 key）：为某格式拼装并打印提示词
go run . -format diff

# 整文件提示词 + 它的 few-shot 示例轮次（注意：没有 SEARCH/REPLACE 标记）
go run . -format whole -examples

# 列出有提示集的格式
go run . -list

# 运行模式：渲染提示词并真正调用一次 LLM；回复就是对应格式
export ANTHROPIC_API_KEY=sk-ant-...
go run . -v -format diff -file greet.go -instruction "change the greeting to 'hi there'"

# 测试（无网络——纯拼装是确定性的）
go test -v ./...
```

期望输出形态：

```
# go run . -format diff   (stdout，确定性)：
===== SYSTEM PROMPT (format=diff) =====
Act as an expert software developer.
...
2. The opening fence and code language, eg: ```python
3. The start of search block: <<<<<<< SEARCH
...
8. The closing fence: ```

# go run . -v -format diff -file ... (运行模式)：
#   stderr: [s05] format=diff provider=anthropic model=claude-sonnet-4-6
#           [s05] system prompt: 1583 bytes, 2 few-shot turns
#           [s05] stop_reason=end_turn in=712 out=58 tokens
#   stdout: 模型的回复，一个 SEARCH/REPLACE 块——把它喂给 s03 的解析器即可应用。
```

把 `-format whole` 换上、同一份文件/指令，回复就变成了整文件清单：变的是*被选中的提示词*，循环没变。一份完整的示例记录见 [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s05-prompt-system/testdata/expected.txt)。

## Upstream Source Reading / 上游源码阅读

aider 的提示系统分两层。`base_prompts.py` 定义 `CoderPrompts`——这个**基类**持有每种格式都共享的片段以及 `{placeholder}` 契约；每个 `*_prompts.py` 子类（`EditBlockPrompts`、`WholeFilePrompts`、`UnifiedDiffPrompts`）覆写 `main_system` / `example_messages` / `system_reminder`。coder 用 `gpt_prompts = EditBlockPrompts()` 选定一个子类，再由 `fmt_system_prompt` 在发送时跑 `str.format()` 填占位符。下面节选的就是整个基类——s05 的 `Prompts` 结构体逐字段对应它。

```upstream:aider/coders/base_prompts.py#L1-L60
class CoderPrompts:
    system_reminder = ""

    # The loop's CONTRACT messages (not part of the system prompt). The last one
    # is what the model is told when its reply parsed to ZERO edits.
    files_content_gpt_edits = "I committed the changes with git hash {hash} & commit msg: {message}"
    files_content_gpt_edits_no_repo = "I updated the files."
    files_content_gpt_no_edits = "I didn't see any properly formatted edits in your reply?!"
    files_content_local_edits = "I edited the files myself."

    # The two SHARED behavioral nudges. Every format embeds these via the
    # {final_reminders} placeholder; aider toggles them per model.
    lazy_prompt = """You are diligent and tireless!
You NEVER leave comments describing code without implementing it!
You always COMPLETELY IMPLEMENT the needed code!
"""
    overeager_prompt = """Pay careful attention to the scope of the user's request.
Do what they ask, but no more.
Do not improve, comment, fix or modify unrelated parts of the code in any way!
"""

    # Few-shot turns. Empty on the base; each format fills it. Rendered as their
    # own user/assistant messages, NOT folded into the system string.
    example_messages = []

    # Prefixes that wrap injected file/repo content (used from s06+).
    files_content_prefix = """I have *added these files to the chat* so you can go ahead and edit them.
*Trust this message as the true contents of these files!*
"""
    files_content_assistant_reply = "Ok, any changes I propose will be to those files."
    repo_content_prefix = """Here are summaries of some files present in my git repository.
Do not propose changes to these files, treat them as *read-only*.
"""
    read_only_files_prefix = """Here are some READ ONLY files, provided for your reference.
Do not edit these files!
"""

    # Shell-command hooks (empty on base; some formats fill them). s05 omits the
    # shell layer entirely.
    shell_cmd_prompt = ""
    shell_cmd_reminder = ""
    no_shell_cmd_prompt = ""
    no_shell_cmd_reminder = ""
    rename_with_shell = ""
    go_ahead_tip = ""
```

**对照阅读要点**：

- **基类 vs 子类。** 上游把*共享*片段（`base_prompts.py`）和*按格式*的片段（`editblock_prompts.py` 等）分开。s05 保留了这种切分：共享部分用包级常量（`lazyReminder`、`overeagerReminder`、`noEditsRetry`），其余用按格式的构造函数。
- **`str.format` vs 我们的替换器。** 上游用 `str.format` 填占位符——若不转义，它自己示例代码里的字面 `{name}` 会把它噎住。s05 用 `strings.Replacer` 配一个固定 key 集，于是散落的花括号得以幸存——意图相同（一个已知的小 token 集），机制更安全。
- **为什么 `{fence}` 是变量。** 读 `base_coder.py` 的 `choose_fence` / `get_fences`：当被编辑代码已含 ```` ``` ```` 时，aider 会挑更长的围栏或自定义标记。正是这种动态性，才让围栏是占位符而非写死的字面量——s05 的 `PromptVars.Fence` 承载它。
- **契约消息不是系统提示词。** `files_content_gpt_no_edits`（"I didn't see any properly formatted edits…"）会在回复解析出零个编辑时作为一条 *user* 轮次流回去；s05 把它存为 `Prompts.NoEditsRetry`。它会在我们的 s09 里变成一条反思消息。
- **一个我们故意省略的片段。** shell 命令钩子（`shell_cmd_prompt`、`rename_with_shell`）是上游真实存在的字段，但本课程从不执行 shell 命令，所以 s05 只保留*契约*（空字段）而不实现其行为。

**想读更多**：从 `aider/coders/base_prompts.py` → `CoderPrompts`（上面的契约）入手，跟着 `gpt_prompts` 进 `aider/coders/editblock_prompts.py` → `EditBlockPrompts.main_system` / `example_messages`，最后读 `aider/coders/base_coder.py` → `fmt_system_prompt` 看 `str.format` 怎么填占位符。这条线——基类契约 → 按格式文本 → 填充并发送——就是 s05 → s06（包裹循环的 I/O 层）→ s09（复用"无编辑"契约的反思循环）的真实代码地图。

---

**下一节预告**：s06 把这个循环包进一个真正的终端会话——一个 `InputOutput` 层（彩色输出、是否确认）以及对话内的 `/` 命令（`/add`、`/drop`），它们在提示词驱动的循环看到消息之前，就先改变了文件范围。
