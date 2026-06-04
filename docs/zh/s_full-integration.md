---
title: "s_full · 整合：端到端 mini-aider"
chapter: 99
slug: s_full-integration
est_read_min: 18
---

# s_full · 整合：端到端 mini-aider

> 教什么：把 s01–s10 学到的十个机制**接成一根完整的管道**。本章不引入任何新机制——它做的全是上游 `aider/main.py` 干的事：解析命令行、装配 `Provider`（s10）+ `RetryProvider`、`GitRepo`（s07）、`RepoMap`（s08）、按 `--edit-format` 选出的 `Coder`（s02–s05）、`Commands` + `InputOutput`（s06），然后跑带反思的交互循环（s09）。读完你应当能在脑子里追完一次"用户在对话里改一个文件"的请求，并指出每一步落在哪个 `agents/sNN` 文件里。

---

## 架构总览 / Architecture Overview

mini-aider 是一摞**围绕三个稳定契约组合起来的零件**：`Provider`（怎么调模型）、`Coder`（怎么解析与落地编辑）、`EditFormat`（模型被要求吐出哪种语法）。`main.go` 是唯一知道全部具体类型的地方——它按命令行把零件拼起来，此后每一层只通过接口互相看见对方。下面这张图是整个 mini 栈，节点对应真实章节：

```text
                    ┌──────────────────────────────────────────────┐
                    │            main.go  (s_full 装配/编排)          │
                    │   解析 --model/--edit-format/--yes，组装下面所有  │
                    └───────────────┬──────────────────────────────┘
                                    │ 构造 + 注入
        ┌───────────────┬──────────┼───────────┬──────────────┬─────────────┐
        ▼               ▼          ▼           ▼              ▼             ▼
 ┌────────────┐  ┌────────────┐ ┌────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
 │ IO+Commands│  │  RepoMap   │ │GitRepo │ │ Provider │ │  Prompts │ │  Linter  │
 │   (s06)    │  │   (s08)    │ │ (s07)  │ │+Retry s10│ │  (s05)   │ │  (s09)   │
 │ /add /drop │  │tags+排名预算│ │自动提交 │ │别名/退避  │ │按格式system│ │gofmt/vet │
 └─────┬──────┘  └─────┬──────┘ └───┬────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘
       │ 文件集          │ 只读上下文   │ commit    │ CreateMsg  │ system     │ 错误文本
       └────────────────┴───────┬────┴───────────┴────────────┘            │
                                ▼                                          │
                       ┌──────────────────┐   选择 (--edit-format)          │
                       │   Coder 循环      │◀──────────────────────────────┘
                       │  (s01 spine)     │   GetEdits → ApplyEdits
                       └────────┬─────────┘   反思循环 (s09) 回灌错误
                                ▼
                    ┌───────────────────────────────┐
                    │  EditFormat 实现 (动态分派)      │
                    │  whole(s02) / diff(s03) / udiff(s04) │
                    └───────────────────────────────┘
```

一句话串起所有章节：**s01 把循环骨架立起来；s02–s04 给它三种可换的编辑格式；s05 让格式驱动 system prompt；s06 用一个交互式 shell（含 `/`-命令）包住循环；s07 把每次编辑变成可回滚的 git 提交；s08 把整个仓库压成排名预算后的只读上下文；s09 让循环在 lint 失败时自我纠错；s10 把那个写死的客户端换成一摞可配置、可重试、可流式的 provider 栈。** s_full 只负责接线。

## 执行轨迹 / Execution Trace

场景：用户在一个 git 仓库里跑 `mini-aider --model sonnet --edit-format diff`，把 `calc.go` 加进对话，然后输入 `给 Add 函数加一句文档注释`。下面 14 步逐一落到真实文件（每步括号里是函数）。

1. **解析命令行、装配栈。** `agents/s10-provider-config-retries/main.go`（`main`）解析 `--model`/`--edit-format`，把别名 `sonnet` 交给 `LookupModel` 解析成 `ModelConfig`——这一步是上游 `aider/main.py` 编排逻辑的 mini 版。

2. **解析模型别名。** `agents/s10-provider-config-retries/models.go`（`LookupModel`）把 `sonnet` 映射到规范模型 id、后端、上下文窗口与默认格式；未知名字返回 `UnknownModelError`，快速失败而非半路崩。

3. **建具体 Provider。** `agents/s10-provider-config-retries/provider.go`（`NewAnthropicProvider`）按后端造出唯一的具体客户端——上游这一步是 litellm 选路由。

4. **裹上重试装饰器。** `agents/s10-provider-config-retries/retry.go`（`NewRetryProvider`）把具体 client 包进一个*也是* `Provider` 的装饰器，指数退避扛 429/5xx；上层循环从此只看见 `Provider` 接口（对应上游 `simple_send_with_retries`）。

5. **打开 git 仓库。** `agents/s07-gitrepo-autocommit/gitrepo.go`（`OpenGitRepo`）用 `git rev-parse --show-toplevel` 从当前目录向上找到工作树根，建立 `GitRepo`——这是 git-as-undo 哲学的入口。

6. **建交互 shell 与命令分发。** `agents/s06-inputoutput-commands/commands.go`（`NewCommands`）把 `/add /drop /ls /diff /help` 注册进 name→handler 表，绑定到一个 `Session`（在对话内的文件集）。

7. **`/add calc.go` 改变文件作用域。** `agents/s06-inputoutput-commands/commands.go`（`IsCommand` 然后 `cmdAdd`）识别出这是命令（以 `/` 开头）、**不**发给模型，把 `calc.go` 塞进 `Session.InChat`——这就是上游 `preproc_user_input` 的边界。

8. **普通输入流向 coder。** 同文件（`IsCommand` 返回 false）让"给 Add 函数加一句文档注释"作为普通对话行落到循环——命令与对话的分流是 s06 的核心。

9. **构建仓库地图（只读上下文）。** `agents/s08-repomap-pagerank/repomap.go`（`RepoMap`）抽取 def/ref 标签、建 def→ref 图、跑 `pageRank`，再在 token 预算内贪心渲染——让模型"看见"结构而无需塞进全部文件内容。

10. **选出按格式定制的 system prompt。** `agents/s05-prompt-system/prompts.go`（`SystemPrompt` → `editBlockPrompts`）按 `--edit-format=diff` 返回含 `<<<<<<< SEARCH` 字面量的 system 串；换格式即换 prompt，循环代码一行不动。

11. **组装消息并调用模型。** `agents/s09-linter-reflection/reflect.go`（`ReflectLoop.Run`，内部 `SendFunc`）把 system prompt + repo map + 文件内容 + 用户指令拼成消息，经第 4 步的 `Provider` 发出——这是唯一的网络往返。

12. **解析 SEARCH/REPLACE 编辑块。** `agents/s03-editblock-searchreplace/editblock.go`（`GetEdits`，借 `isSearch`/`isDivider`/`isReplace`）把回复扫成 `(path, search, replace)` 三元组——`Edit.Search` 在此非空。

13. **用分层模糊匹配落地编辑。** 同文件（`ApplyEdits` → `applyOne` → `replaceMostSimilarChunk` → `perfectOrWhitespace`）先精确匹配、再容忍整体缩进偏移、最后 `tryDotDotDots`，把 `replace` 写进 `calc.go`——这是 aider 最承重的一处巧劲。

14. **lint、反思、自动提交、再循环。** 回到 `agents/s09-linter-reflection/reflect.go`（`ReflectLoop.Run`）：调 `agents/s09-linter-reflection/linter.go`（`Linter.Lint` 跑 `gofmt -e`/`go vet`）；clean 则结束，否则把错误当成合成的*用户*消息回灌、重进 11–13（上限 `DefaultMaxReflections = 3`）；clean 后由 `agents/s07-gitrepo-autocommit/gitrepo.go`（`GitRepo.Commit`，带 `Co-authored-by` 归属）把这次编辑提交成一个原子 commit，再向用户要下一行输入。

> 没有反思失败时，第 11→14 步只走一遍：发送→解析→落地→lint clean→提交。反思只在 lint 报错时把 11–13 再跑一遍。

## 跨章交互图 / Cross-chapter Interaction

下面的时序图展示一次请求里"谁调用了谁"。竖线是各章贡献的组件；`==>` 表示穿过**稳定类型契约**（`Provider`/`Coder`/`EditFormat` 三个接口，编译期定死），`-->` 表示**动态分派**（命令查表、按 `--edit-format` 选 coder——运行期才定）。

```text
 用户   main(s_full)  Commands(s06)  ReflectLoop(s09)  RepoMap(s08)  Coder(s03)  RetryProvider(s10)  Linter(s09)  GitRepo(s07)
  │          │             │               │               │            │              │              │            │
  │ /add ────┼────────────>│ (动态分派: name→handler)        │            │              │              │            │
  │          │             │ cmdAdd 改 Session.InChat        │            │              │              │            │
  │ "加注释" ─┼────────────>│ IsCommand=false                │            │              │              │            │
  │          │             │----(普通行)-->│               │            │              │              │            │
  │          │             │               │ GetRepoMap --->│            │              │              │            │
  │          │             │               │<--只读上下文----│            │              │              │            │
  │          │             │               │ SystemPrompt(s05) 按格式选 prompt           │              │            │
  │          │             │               │ CreateMessage ============================>│ (稳定: Provider)│         │
  │          │             │               │<==========================================│ (退避/重试)     │         │
  │          │             │               │ GetEdits ====>│ (稳定: Coder)│              │              │            │
  │          │             │               │ ApplyEdits ==>│ 分层模糊匹配  │              │              │            │
  │          │             │               │ Lint ----------------------------------------->│           │            │
  │          │             │               │<--错误? clean? ------------------------------│            │            │
  │          │             │               │ (若脏: 回灌错误为合成 user 消息, 回到 CreateMessage; 上限 3) │            │
  │          │             │               │ Commit ------------------------------------------------------->│       │
  │<--结果---│             │               │                                                              │ 原子提交 │
```

要点：**接口契约不变，实现可换。** `ReflectLoop` 对 `Provider`、`Coder` 只认接口——把 `--edit-format` 从 `diff` 换成 `udiff`（`agents/s04-unified-diff-format/udiff.go`）或 `whole`（`agents/s02-wholefile-format/wholefile.go`），时序图上唯一变的是"Coder"那条竖线背后的具体类型；`main` 之外没有一行代码需要改。命令分发（s06）与格式选择（s05）是两处*动态*决策点，其余都是编译期定死的稳定调用。

## 故意省略 / Deliberate Omissions

mini-aider 教的是承重机制；上游 aider 在每个机制周围还堆了大量生产级的料。下面这张表列出我们**有意**砍掉的部分及理由。

| 上游特性 | 上游路径 | 我们为何省略 |
|---|---|---|
| tree-sitter 多语言 repomap | `aider/repomap.py`（`get_tags` 用 `grep_ast`/`tree_sitter`，L233+） | s08 用 Go 的 `go/parser` 只覆盖 Go 源码，把 PageRank 这个核心讲清楚；40+ 语言的解析栈需要 cgo，喧宾夺主。 |
| 流式输出（实时 token） | `aider/coders/base_coder.py`（`run_stream` L859、`self.stream` L1442） | s10 *实现*了 SSE 解码（`StreamingProvider`），但 s_full 默认走一次性调用，避免把活的 markdown 渲染塞进集成层。 |
| architect/editor 双 coder | `aider/coders/architect_coder.py`（`ArchitectCoder`、`editor_model` L22） | "一个模型出方案、另一个模型落编辑"是高级编排；我们只实现单 coder 路径，保持循环只有一条主线。 |
| 聊天历史摘要 | `aider/history.py`（`ChatSummary.summarize` L27） | 多轮历史超预算时的后台摘要是上下文管理的优化项，与"端到端跑通一次编辑"正交。 |
| 语音输入 `/voice` | `aider/voice.py`（`Voice` L33）+ `aider/commands.py`（`cmd_voice` L1252） | Whisper 语音转文字是输入法的旁支，不影响 coder 循环的任何一步。 |
| 浏览器 GUI | `aider/gui.py`（streamlit）+ `aider/main.py`（`check_streamlit_install` L208） | 基于 streamlit 的网页界面是 CLI 的替代前端；我们只做终端。 |
| 网页抓取 `/web` | `aider/scrape.py`（`Scraper`、playwright L19） | 把 URL 抓成上下文需要 playwright 无头浏览器，属于内容获取旁支。 |
| 文件监听 + AI 注释 | `aider/watch.py`（`FileWatcher` L65、`get_ai_comments` L257） | 监听 `# ai` 注释做 IDE 内迭代是另一种输入触发方式，与交互循环正交。 |
| 遥测/分析 | `aider/analytics.py`（`Analytics`，posthog/mixpanel L8-9） | PostHog/Mixpanel 埋点纯属运营，教学版一概不要。 |
| `.aider.conf.yml` 全配置面 | `aider/args.py`（配置文件搜索 L794）+ `sample.aider.conf.yml` | 上游有 50+ 个 CLI/YAML/env 三路配置项；我们只解析 `--model`/`--edit-format`/`--yes` 这几个承重旋钮。 |

## 延伸阅读 / Read Further

- **从循环骨架读起：** 把本章的 14 步执行轨迹对着上游 `aider/coders/base_coder.py` 的 `run` → `run_one`（L876-944）→ `send_message`（L1419）读一遍——你会发现我们这条管道就是它去掉流式、token 预算、dry-run 后的主干。
- **真正的多 provider 层：** s10 用一张手写注册表 + 一个重试装饰器复刻了 litellm 的*形状*。读上游 `aider/models.py` 的 `send_completion`（L985）与 `simple_send_with_retries`（L1039-1079），看真实版本如何统一 50+ 家后端的异常分类与流式分块。
- **RepoMap 的算法心脏：** 本仓库 Appendix A 手推了 `aider/repomap.py` 的 `get_ranked_tags`（L365-575）——def→ref 图 + PageRank + 个性化向量 + 贪心预算填充。想看 tree-sitter 如何为 40+ 语言抽标签，从 `get_tags`（L233）往下读。
- **编辑格式的全谱系：** 我们实现了 whole/diff/udiff 三种；上游 `aider/coders/` 下还有 16 个 `*_coder.py` 变体（patch、ask、architect、func 等）。挑 `editblock_coder.py` 的 `find_original_update_blocks`（L439-560）对照 s03，体会生产级解析器的边界处理。
- **上游文件地图：** 本仓库 Appendix B 把 `aider/` 整棵树按章节索引——从任一 `agents/sNN` 的 Go 文件出发，都能找回它对应的 Python 祖先与精确读过的行号范围。
