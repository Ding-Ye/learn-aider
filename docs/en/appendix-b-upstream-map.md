---
title: "Appendix B · Upstream file map"
appendix: B
slug: appendix-b-upstream-map
est_read_min: 11
---

# 附录 B · 上游文件地图 / Appendix B · Upstream file map

> Points `agents/sNN/*.go` back to its Python ancestor. All line numbers are pinned to upstream SHA `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`, cloned under `.learn/upstream/`.

---

## 阅读顺序 / Reading order

If you want to read the upstream source along with the curriculum, this order is the path of least resistance — start from "how a single file gets edited" and work up to "how a whole session is orchestrated":

1. **`aider/coders/wholefile_coder.py`** (144 lines) → pairs with **s02**. The simplest edit format: parse a whole file, write the whole file back. Read it first to build the "get_edits / apply_edits" mental model.
2. **`aider/coders/editblock_coder.py`** (657 lines) → pairs with **s03**. SEARCH/REPLACE blocks plus fuzzy matching — aider's most-used and most reliable format.
3. **`aider/coders/udiff_coder.py`** (429 lines) → pairs with **s04**. The standard `@@` patch format, the third Coder.
4. **`aider/coders/base_prompts.py`** (60 lines) → pairs with **s05**. Understand the `gpt_prompts` decoupling point: how one loop injects a different system prompt per format.
5. **`aider/coders/base_coder.py`** (2485 lines, focus L876-1567) → pairs with **s01/s09**. The main loop `run` / `run_one` / `send_message`, plus the reflection loop. This is the load-bearing wall of the whole building; read it in the middle — once you understand "edit formats", you can see how they're driven.
6. **`aider/repo.py`** (622 lines) → pairs with **s07**. The GitRepo wrapper and auto-commit.
7. **`aider/repomap.py`** (867 lines, focus L365-574) → pairs with **s08**. tree-sitter tags + PageRank; see [Appendix A](./appendix-a-repomap-pagerank.md).
8. **`aider/linter.py`** (304 lines) → pairs with **s09**. The linter and the other half of the "edit → lint → fix" reflection loop.
9. **`aider/io.py`** (1191 lines, focus L230/523/807) + **`aider/commands.py`** (1712 lines) → pairs with **s06**. Terminal I/O and `/` commands.
10. **`aider/models.py`** (1338 lines, focus L985-1079) + **`aider/llm.py`** (47 lines) → pairs with **s10**. The litellm wrapper, retries, and streaming — the architectural capstone of the curriculum.

After those ten steps, use the two tables below to line up each Go file and each Go type with upstream.

## 文件-章节映射 / File-to-session map

| Upstream file | Lines | What it does | Our session |
|---------------|-------|--------------|-------------|
| `aider/coders/base_coder.py` | 2485 | Coder base class, main loop (run/run_one/send_message), message assembly, reflection, auto_commit | s01, s09 |
| `aider/coders/wholefile_coder.py` | 144 | Whole-file edit format (get_edits L22 / apply_edits L124) | s02 |
| `aider/coders/editblock_coder.py` | 657 | SEARCH/REPLACE block parsing and application, fuzzy-match entry | s03 |
| `aider/coders/search_replace.py` | 757 | Fuzzy-match engine (RelativeIndenter, flexible_search_and_replace) | s03 |
| `aider/coders/udiff_coder.py` | 429 | Unified diff format (find_diffs / hunk_to_before_after / apply_hunk) | s04 |
| `aider/coders/base_prompts.py` | 60 | CoderPrompts base class, `{fence}` placeholder contract | s05 |
| `aider/coders/editblock_prompts.py` | 172 | System prompt and few-shot examples for the editblock format | s05 |
| `aider/coders/wholefile_prompts.py` | 64 | System prompt for the whole-file format | s05 |
| `aider/coders/udiff_prompts.py` | 113 | System prompt for the unified diff format | s05 |
| `aider/coders/chat_chunks.py` | 64 | ChatChunks: message segmentation and token budgeting | s05, s08 |
| `aider/io.py` | 1191 | InputOutput: terminal input/output, confirmation, autocomplete | s06 |
| `aider/commands.py` | 1712 | Commands: in-chat commands like `/add`, `/drop`, `/commit` | s06 |
| `aider/repo.py` | 622 | GitRepo: dirty detection, commit, diffs, tracked files | s07 |
| `aider/repomap.py` | 867 | RepoMap: tree-sitter tags + PageRank + token budget | s08 |
| `aider/linter.py` | 304 | Linter: run linters, extract errors and offending lines | s09 |
| `aider/models.py` | 1338 | ModelSettings/Model: send_completion, retries, streaming, model registry | s10 |
| `aider/llm.py` | 47 | LazyLiteLLM: wrapper that defers `import litellm` | s10 |
| `aider/sendchat.py` | 61 | sanity_check_messages / ensure_alternating_roles: message validity | s10 |
| `aider/main.py` | 1274 | CLI entry point, session orchestration (get_git_root L60, Coder.create) | s_full |
| `aider/diffs.py` | 128 | Incremental diff display (live diff during streaming output) | s10 (reference) |

## 符号对照表 / Symbol cross-reference

| Our type/func | Upstream equivalent | File:line |
|---------------|---------------------|-----------|
| `Provider` (interface) / `provider_openai.go` | `Model.send_completion` → `litellm.completion(**kwargs)` | `aider/models.py:985`, `:1036` |
| `RetryProvider` | `Model.simple_send_with_retries` | `aider/models.py:1039` |
| alias resolution in `models.go` | `class ModelSettings` / `class Model` | `aider/models.py:128`, `aider/models.py:329` |
| `Coder` (interface) | `class Coder` | `aider/coders/base_coder.py:88` |
| run loop in `main.go` (s01) | `Coder.run` / `Coder.run_one` | `aider/coders/base_coder.py:876`, `:924` |
| `WholeFileCoder` | `class WholeFileCoder` | `aider/coders/wholefile_coder.py:10` |
| `GetEdits` in `wholefile.go` | `WholeFileCoder.get_edits` | `aider/coders/wholefile_coder.py:22` |
| `EditBlockCoder` | `class EditBlockCoder` | `aider/coders/editblock_coder.py:15` |
| block parsing in `editblock.go` | `find_original_update_blocks` | `aider/coders/editblock_coder.py:439` |
| `perfectOrWhitespace` in `editblock.go` | `perfect_or_whitespace` / `replace_most_similar_chunk` | `aider/coders/editblock_coder.py:134`, `:157` |
| `findDiffs` in `udiff.go` | `find_diffs` | `aider/coders/udiff_coder.py:312` |
| `hunkToBeforeAfter` in `udiff.go` | `hunk_to_before_after` | `aider/coders/udiff_coder.py:403` |
| `CoderPrompts` (prompts.go) | `class CoderPrompts` | `aider/coders/base_prompts.py:1` |
| `ConfirmAsk` / `GetInput` in `io.go` | `InputOutput.confirm_ask` / `.get_input` | `aider/io.py:807`, `:523` |
| `IsCommand` / `Run` in `commands.go` | `Commands.is_command` / `.run` | `aider/commands.py:255`, `:312` |
| `GitRepo` (gitrepo.go) | `class GitRepo` / `GitRepo.commit` | `aider/repo.py:52`, `:131` |
| `RepoMap` (repomap.go) | `class RepoMap` / `get_ranked_tags` | `aider/repomap.py:42`, `:365` |
| `Tag` (struct) | `Tag = namedtuple(...)` | `aider/repomap.py:29` |
| `Linter` (linter.go) | `class Linter` / `Linter.lint` | `aider/linter.py:21`, `:82` |
| reflection counter in `reflect.go` | `run_one` reflection branch + `max_reflections` | `aider/coders/base_coder.py:924-944`, `:101` |

## 建议练手 / Suggested exercises

The curriculum deliberately leaves out a lot of upstream capability. These are the best candidates to extend (each comes with an upstream reference):

1. **Add an `architect` multi-step format.** Upstream `aider/coders/architect_coder.py` (48 lines) has the model "plan" first, then hand off to an editor model to "land" the change. Build a two-stage Coder on top of s05's prompt table and s10's multi-model setup.
2. **Implement the dotdotdots and edit-distance match tiers.** s03 only does perfect + whitespace-tolerant matching; upstream `search_replace.py` (757 lines) also has `try_dotdotdots` and a `diff_match_patch`-based fuzzy tier. Add the latter two and write tests covering real-world drift beyond indentation.
3. **Wire real tree-sitter into RepoMap.** The mini version extracts symbols with regex heuristics; switch to `smacker/go-tree-sitter` or `go/parser`, cross-checking upstream `repomap.py:279`'s `get_tags_raw`, for more accurate multi-language extraction.
4. **Add RepoMap's weight multipliers.** Following `repomap.py:487-499`, add back "long identifier ×10, mentioned ×10, private prefix ×0.1, over-defined ×0.1" to s08's PageRank, and verify the ranking changes with tests.
5. **Implement history summarization.** Upstream summarizes old messages in the background when the conversation exceeds the budget (`chat_chunks.py` + `ChatSummary`). Add a "summarize once over N tokens" step on top of s06.

## 注意事项 / Caveats

- **Line numbers will drift.** Every `file:line` in this appendix is pinned to upstream SHA `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`. aider is a high-velocity project (its README claims "88% of the code in the last release was written by aider itself"), so main-branch line numbers have almost certainly moved. To reproduce, `git checkout 5dc9490` or use a GitHub permalink at that SHA, e.g. `https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/repomap.py#L365`.
- **Line numbers point at the "definition start line".** The table gives the `def`/`class` line of a class or function, not the range of the whole block; read downward to the next sibling definition.
- **What the curriculum deliberately omits.** analytics (telemetry), voice (speech input), watch (file watching / AI comments), the web UI (`--gui`), and the 30+ other format variants under `aider/coders/` (patch, fenced, func, ask, etc.) are all out of scope — they are engineering periphery, not core mechanism.
- **`base_coder.py` spans several chapters by itself.** Its 2485 lines hold s01's main loop, s07's auto_commit, and s09's reflection all at once. When reading the table, note that the "Our session" column may point to different line ranges within the same upstream file.
