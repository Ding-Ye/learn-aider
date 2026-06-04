---
title: "附录 B · 上游文件地图"
appendix: B
slug: appendix-b-upstream-map
est_read_min: 11
---

# 附录 B · 上游文件地图 / Appendix B · Upstream file map

> 把 `agents/sNN/*.go` 指回它的 Python 祖先。所有行号钉死在上游 SHA `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`，克隆于 `.learn/upstream/`。

---

## 阅读顺序 / Reading order

如果你想顺着课程读上游源码，按下面的顺序走最省力——从「一个文件如何被改写」开始，逐步走到「整个会话如何编排」：

1. **`aider/coders/wholefile_coder.py`**（144 行）→ 配 **s02**。最简单的编辑格式：解析整文件、整文件写回。先看它，建立「get_edits / apply_edits」的心智模型。
2. **`aider/coders/editblock_coder.py`**（657 行）→ 配 **s03**。SEARCH/REPLACE 块 + 模糊匹配，是 aider 最常用、最可靠的格式。
3. **`aider/coders/udiff_coder.py`**（429 行）→ 配 **s04**。标准 `@@` 补丁格式，第三种 Coder。
4. **`aider/coders/base_prompts.py`**（60 行）→ 配 **s05**。理解 `gpt_prompts` 这个解耦点：同一个循环如何为不同格式注入不同系统提示。
5. **`aider/coders/base_coder.py`**（2485 行，重点 L876-1567）→ 配 **s01/s09**。主循环 `run` / `run_one` / `send_message`，以及反思循环。这是整座建筑的承重墙，建议放在中间读——前面看懂了「编辑格式」，这里才看得懂它如何被驱动。
6. **`aider/repo.py`**（622 行）→ 配 **s07**。GitRepo 封装与自动提交。
7. **`aider/repomap.py`**（867 行，重点 L365-574）→ 配 **s08**。tree-sitter 标签 + PageRank，详见[附录 A](./appendix-a-repomap-pagerank.md)。
8. **`aider/linter.py`**（304 行）→ 配 **s09**。检查器与「编辑→检查→修复」反思闭环的另一半。
9. **`aider/io.py`**（1191 行，重点 L230/523/807）+ **`aider/commands.py`**（1712 行）→ 配 **s06**。终端 I/O 与 `/` 命令。
10. **`aider/models.py`**（1338 行，重点 L985-1079）+ **`aider/llm.py`**（47 行）→ 配 **s10**。litellm 封装、重试与流式——课程的架构收尾。

读完这十步，再用下面两张表把每个 Go 文件、每个 Go 类型逐一对回上游。

## 文件-章节映射 / File-to-session map

| 上游文件 | 行数 | 作用 | 我们的章节 |
|----------|------|------|------------|
| `aider/coders/base_coder.py` | 2485 | Coder 基类、主循环（run/run_one/send_message）、消息组装、反思、auto_commit | s01, s09 |
| `aider/coders/wholefile_coder.py` | 144 | 整文件编辑格式（get_edits L22 / apply_edits L124） | s02 |
| `aider/coders/editblock_coder.py` | 657 | SEARCH/REPLACE 块解析与应用、模糊匹配入口 | s03 |
| `aider/coders/search_replace.py` | 757 | 模糊匹配引擎（RelativeIndenter、flexible_search_and_replace） | s03 |
| `aider/coders/udiff_coder.py` | 429 | 统一 diff 格式（find_diffs / hunk_to_before_after / apply_hunk） | s04 |
| `aider/coders/base_prompts.py` | 60 | CoderPrompts 基类、`{fence}` 等占位符契约 | s05 |
| `aider/coders/editblock_prompts.py` | 172 | editblock 格式的系统提示与少样本示例 | s05 |
| `aider/coders/wholefile_prompts.py` | 64 | 整文件格式的系统提示 | s05 |
| `aider/coders/udiff_prompts.py` | 113 | 统一 diff 格式的系统提示 | s05 |
| `aider/coders/chat_chunks.py` | 64 | ChatChunks：消息分段与 token 预算 | s05, s08 |
| `aider/io.py` | 1191 | InputOutput：终端输入/输出、确认、自动补全 | s06 |
| `aider/commands.py` | 1712 | Commands：`/add`、`/drop`、`/commit` 等对话内命令 | s06 |
| `aider/repo.py` | 622 | GitRepo：dirty 检测、提交、diff、tracked files | s07 |
| `aider/repomap.py` | 867 | RepoMap：tree-sitter 标签 + PageRank + token 预算 | s08 |
| `aider/linter.py` | 304 | Linter：运行检查器、提取错误与出错行 | s09 |
| `aider/models.py` | 1338 | ModelSettings/Model：send_completion、重试、流式、模型注册 | s10 |
| `aider/llm.py` | 47 | LazyLiteLLM：延迟 import litellm 的封装 | s10 |
| `aider/sendchat.py` | 61 | sanity_check_messages / ensure_alternating_roles：消息合法性 | s10 |
| `aider/main.py` | 1274 | CLI 入口、会话编排（get_git_root L60、Coder.create） | s_full |
| `aider/diffs.py` | 128 | 增量 diff 显示（流式输出时的实时 diff） | s10（参考） |

## 符号对照表 / Symbol cross-reference

| 我们的类型/函数 | 上游对应 | 文件:行 |
|-----------------|----------|---------|
| `Provider`（接口）/ `provider_openai.go` | `Model.send_completion` → `litellm.completion(**kwargs)` | `aider/models.py:985`, `:1036` |
| `RetryProvider` | `Model.simple_send_with_retries` | `aider/models.py:1039` |
| `models.go` 的别名解析 | `class ModelSettings` / `class Model` | `aider/models.py:128`, `aider/models.py:329` |
| `Coder`（接口） | `class Coder` | `aider/coders/base_coder.py:88` |
| `main.go` 的 run 循环（s01） | `Coder.run` / `Coder.run_one` | `aider/coders/base_coder.py:876`, `:924` |
| `WholeFileCoder` | `class WholeFileCoder` | `aider/coders/wholefile_coder.py:10` |
| `wholefile.go` 的 GetEdits | `WholeFileCoder.get_edits` | `aider/coders/wholefile_coder.py:22` |
| `EditBlockCoder` | `class EditBlockCoder` | `aider/coders/editblock_coder.py:15` |
| `editblock.go` 的块解析 | `find_original_update_blocks` | `aider/coders/editblock_coder.py:439` |
| `editblock.go` 的 perfectOrWhitespace | `perfect_or_whitespace` / `replace_most_similar_chunk` | `aider/coders/editblock_coder.py:134`, `:157` |
| `udiff.go` 的 findDiffs | `find_diffs` | `aider/coders/udiff_coder.py:312` |
| `udiff.go` 的 hunkToBeforeAfter | `hunk_to_before_after` | `aider/coders/udiff_coder.py:403` |
| `CoderPrompts`（prompts.go） | `class CoderPrompts` | `aider/coders/base_prompts.py:1` |
| `io.go` 的 ConfirmAsk / GetInput | `InputOutput.confirm_ask` / `.get_input` | `aider/io.py:807`, `:523` |
| `commands.go` 的 IsCommand / Run | `Commands.is_command` / `.run` | `aider/commands.py:255`, `:312` |
| `GitRepo`（gitrepo.go） | `class GitRepo` / `GitRepo.commit` | `aider/repo.py:52`, `:131` |
| `RepoMap`（repomap.go） | `class RepoMap` / `get_ranked_tags` | `aider/repomap.py:42`, `:365` |
| `Tag`（结构体） | `Tag = namedtuple(...)` | `aider/repomap.py:29` |
| `Linter`（linter.go） | `class Linter` / `Linter.lint` | `aider/linter.py:21`, `:82` |
| `reflect.go` 的反思计数器 | `run_one` 反思分支 + `max_reflections` | `aider/coders/base_coder.py:924-944`, `:101` |

## 建议练手 / Suggested exercises

课程刻意留白了不少上游能力，下面几个最适合拿来扩展（每个都自带上游参照）：

1. **加一个 `architect` 多步格式。** 上游 `aider/coders/architect_coder.py`（48 行）让模型先「规划」再交给 editor 模型「落地」。在 s05 的 prompt 表 + s10 的 multi-model 之上实现一个两阶段 Coder。
2. **实现 dotdotdots 与编辑距离匹配层。** s03 只做了 perfect + 空白容错两档；上游 `search_replace.py`（757 行）还有 `try_dotdotdots` 与基于 `diff_match_patch` 的模糊层。把后两档补上，并写测试覆盖缩进漂移之外的真实场景。
3. **给 RepoMap 接上真正的 tree-sitter。** mini 版用正则启发式抽符号；改用 `smacker/go-tree-sitter` 或 `go/parser`，对照上游 `repomap.py:279` 的 `get_tags_raw`，让多语言抽取更准。
4. **补上 RepoMap 的权重乘子。** 照 `repomap.py:487-499` 把「长标识符 ×10、被提及 ×10、私有前缀 ×0.1、定义过多 ×0.1」加回 s08 的 PageRank，并用测试验证排序变化。
5. **实现历史摘要（history summarization）。** 上游在对话超预算时会后台摘要旧消息（`chat_chunks.py` + `ChatSummary`）。在 s06 之上加一个「超过 N token 就摘要」的步骤。

## 注意事项 / Caveats

- **行号会漂移。** 本附录所有 `文件:行` 都钉死在上游 SHA `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`。aider 是高频迭代项目（README 自称「最近一个版本 88% 代码由 aider 自己写」），主分支的行号几乎肯定已经变了。要复现，请 `git checkout 5dc9490` 或在 GitHub 上用该 SHA 的 permalink，例如 `https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/repomap.py#L365`。
- **行号对的是「定义起始行」。** 表格里给的是类/函数的 `def`/`class` 那一行，不是整个块的范围；阅读时往下读到下一个同级定义为止。
- **课程刻意省略的部分。** analytics（遥测）、voice（语音输入）、watch（文件监听 / AI 注释）、web UI（`--gui`）、以及 `aider/coders/` 下 30+ 个其它格式变体（patch、fenced、func、ask 等）都不在课程范围内——它们是工程外围，不是核心机制。
- **`base_coder.py` 一个文件横跨多章。** 它 2485 行里同时藏着 s01 的主循环、s07 的 auto_commit、s09 的反思。看表时注意「我们的章节」一列可能指向同一个上游文件的不同行段。
