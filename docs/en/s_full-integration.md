---
title: "s_full · Integration: end-to-end mini-aider"
chapter: 99
slug: s_full-integration
est_read_min: 18
---

# s_full · Integration: end-to-end mini-aider

> What this teaches: **wiring the ten mechanisms from s01–s10 into one complete pipeline.** This chapter introduces no new mechanism — it does exactly what upstream `aider/main.py` does: parse the command line, assemble a `Provider` (s10) + `RetryProvider`, a `GitRepo` (s07), a `RepoMap` (s08), the `Coder` selected by `--edit-format` (s02–s05), `Commands` + `InputOutput` (s06), then run the interactive loop with reflection (s09). By the end you should be able to trace a single "user edits a file via chat" request in your head and point at which `agents/sNN` file owns each step.

---

## 架构总览 / Architecture Overview

mini-aider is a stack of **parts composed around three stable contracts**: `Provider` (how we call the model), `Coder` (how we parse and apply edits), and `EditFormat` (which syntax the model is told to emit). `main.go` is the only place that knows every concrete type — it wires the parts together from the command line, and from then on each layer sees the others only through interfaces. The diagram below is the whole mini stack, with nodes mapping to real chapters:

```text
                    ┌──────────────────────────────────────────────┐
                    │        main.go  (s_full wiring/orchestration)  │
                    │  parse --model/--edit-format/--yes, build all  │
                    └───────────────┬──────────────────────────────┘
                                    │ construct + inject
        ┌───────────────┬──────────┼───────────┬──────────────┬─────────────┐
        ▼               ▼          ▼           ▼              ▼             ▼
 ┌────────────┐  ┌────────────┐ ┌────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐
 │ IO+Commands│  │  RepoMap   │ │GitRepo │ │ Provider │ │  Prompts │ │  Linter  │
 │   (s06)    │  │   (s08)    │ │ (s07)  │ │+Retry s10│ │  (s05)   │ │  (s09)   │
 │ /add /drop │  │tags+ranked │ │autocommit│ alias/backoff│per-format │ │gofmt/vet │
 └─────┬──────┘  └─────┬──────┘ └───┬────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘
       │ file set        │ read-only   │ commit    │ CreateMsg  │ system     │ error text
       └────────────────┴───────┬────┴───────────┴────────────┘            │
                                ▼                                          │
                       ┌──────────────────┐   select (--edit-format)       │
                       │   Coder loop      │◀──────────────────────────────┘
                       │  (s01 spine)     │   GetEdits → ApplyEdits
                       └────────┬─────────┘   reflection (s09) feeds errors back
                                ▼
                    ┌───────────────────────────────┐
                    │  EditFormat impls (dynamic)     │
                    │  whole(s02) / diff(s03) / udiff(s04) │
                    └───────────────────────────────┘
```

One sentence tying the chapters together: **s01 stands up the loop skeleton; s02–s04 give it three interchangeable edit formats; s05 lets the format drive the system prompt; s06 wraps the loop in an interactive shell with `/`-commands; s07 turns every edit into a reversible git commit; s08 compresses the whole repo into ranked, budgeted read-only context; s09 makes the loop self-correct when a lint fails; s10 swaps the hardcoded client for a configurable, retrying, streaming provider stack.** s_full is just the wiring.

## 执行轨迹 / Execution Trace

Scenario: a user runs `mini-aider --model sonnet --edit-format diff` inside a git repo, adds `calc.go` to the chat, then types `add a doc comment to the Add function`. The 14 steps below each land on a real file (the function is in parentheses).

1. **Parse the command line, assemble the stack.** `agents/s10-provider-config-retries/main.go` (`main`) parses `--model`/`--edit-format` and hands the alias `sonnet` to `LookupModel` to resolve a `ModelConfig` — the mini version of upstream `aider/main.py`'s orchestration.

2. **Resolve the model alias.** `agents/s10-provider-config-retries/models.go` (`LookupModel`) maps `sonnet` to a canonical model id, backend, context window, and default format; an unknown name returns `UnknownModelError` and fails fast rather than crashing mid-run.

3. **Build the concrete Provider.** `agents/s10-provider-config-retries/provider.go` (`NewAnthropicProvider`) constructs the one concrete client for the backend — upstream this step is litellm's routing.

4. **Wrap it in the retry decorator.** `agents/s10-provider-config-retries/retry.go` (`NewRetryProvider`) wraps the concrete client in a decorator that is *also* a `Provider`, with exponential backoff for 429/5xx; the loop above now sees only the `Provider` interface (upstream `simple_send_with_retries`).

5. **Open the git repo.** `agents/s07-gitrepo-autocommit/gitrepo.go` (`OpenGitRepo`) walks up from the cwd to the working-tree root via `git rev-parse --show-toplevel` and builds a `GitRepo` — the entry point of the git-as-undo philosophy.

6. **Build the interactive shell and command dispatcher.** `agents/s06-inputoutput-commands/commands.go` (`NewCommands`) registers `/add /drop /ls /diff /help` into a name→handler table bound to a `Session` (the in-chat file set).

7. **`/add calc.go` changes the file scope.** `agents/s06-inputoutput-commands/commands.go` (`IsCommand` then `cmdAdd`) recognizes this as a command (starts with `/`), does **not** send it to the model, and inserts `calc.go` into `Session.InChat` — this is upstream's `preproc_user_input` boundary.

8. **Plain input flows to the coder.** Same file (`IsCommand` returns false) lets "add a doc comment to the Add function" fall through to the loop as a plain chat line — the command-vs-chat split is the heart of s06.

9. **Build the repo map (read-only context).** `agents/s08-repomap-pagerank/repomap.go` (`RepoMap`) extracts def/ref tags, builds a def→ref graph, runs `pageRank`, then greedily renders within a token budget — letting the model "see" structure without packing in every file's full contents.

10. **Select the per-format system prompt.** `agents/s05-prompt-system/prompts.go` (`SystemPrompt` → `editBlockPrompts`) returns, for `--edit-format=diff`, a system string containing the `<<<<<<< SEARCH` literal; swapping the format swaps the prompt with zero changes to loop code.

11. **Assemble messages and call the model.** `agents/s09-linter-reflection/reflect.go` (`ReflectLoop.Run`, via its `SendFunc`) packs system prompt + repo map + file contents + user instruction into messages and sends them through the step-4 `Provider` — the one network round-trip.

12. **Parse the SEARCH/REPLACE edit blocks.** `agents/s03-editblock-searchreplace/editblock.go` (`GetEdits`, using `isSearch`/`isDivider`/`isReplace`) scans the reply into `(path, search, replace)` triples — `Edit.Search` is non-empty here.

13. **Apply the edits with tiered fuzzy matching.** Same file (`ApplyEdits` → `applyOne` → `replaceMostSimilarChunk` → `perfectOrWhitespace`) tries an exact match first, then tolerates uniformly-shifted indentation, then `tryDotDotDots`, writing `replace` into `calc.go` — aider's single most load-bearing piece of cleverness.

14. **Lint, reflect, auto-commit, then loop.** Back in `agents/s09-linter-reflection/reflect.go` (`ReflectLoop.Run`): it calls `agents/s09-linter-reflection/linter.go` (`Linter.Lint` running `gofmt -e`/`go vet`); if clean it finishes, otherwise it feeds the errors back as a synthetic *user* message and re-enters steps 11–13 (capped at `DefaultMaxReflections = 3`); once clean, `agents/s07-gitrepo-autocommit/gitrepo.go` (`GitRepo.Commit`, with a `Co-authored-by` attribution) commits the edit as one atomic commit, then prompts the user for the next line.

> When reflection doesn't fire, steps 11→14 run exactly once: send → parse → apply → lint clean → commit. Reflection only re-runs 11–13 when the lint reports errors.

## 跨章交互图 / Cross-chapter Interaction

The sequence diagram below shows "who calls whom" during one request. The vertical lines are components contributed by each chapter; `==>` crosses a **stable type contract** (the three interfaces `Provider`/`Coder`/`EditFormat`, fixed at compile time), and `-->` is **dynamic dispatch** (command lookup, coder selection by `--edit-format` — decided at runtime).

```text
 user  main(s_full)  Commands(s06)  ReflectLoop(s09)  RepoMap(s08)  Coder(s03)  RetryProvider(s10)  Linter(s09)  GitRepo(s07)
  │          │             │               │               │            │              │              │            │
  │ /add ────┼────────────>│ (dynamic: name→handler)        │            │              │              │            │
  │          │             │ cmdAdd mutates Session.InChat   │            │              │              │            │
  │ "comment"┼────────────>│ IsCommand=false                │            │              │              │            │
  │          │             │----(plain line)-->│           │            │              │              │            │
  │          │             │               │ GetRepoMap --->│            │              │              │            │
  │          │             │               │<--read-only ctx│            │              │              │            │
  │          │             │               │ SystemPrompt(s05) pick prompt by format     │              │            │
  │          │             │               │ CreateMessage ============================>│ (stable: Provider)│      │
  │          │             │               │<==========================================│ (backoff/retry) │       │
  │          │             │               │ GetEdits ====>│ (stable: Coder)│            │              │            │
  │          │             │               │ ApplyEdits ==>│ tiered fuzzy │              │              │            │
  │          │             │               │ Lint ----------------------------------------->│           │            │
  │          │             │               │<--errors? clean? ----------------------------│            │            │
  │          │             │               │ (if dirty: feed errors as synthetic user msg, back to CreateMessage; cap 3) │
  │          │             │               │ Commit ------------------------------------------------------->│       │
  │<--result-│             │               │                                                              │ atomic   │
```

Key point: **the interface contracts stay fixed; the implementations swap.** `ReflectLoop` only knows `Provider` and `Coder` as interfaces — switch `--edit-format` from `diff` to `udiff` (`agents/s04-unified-diff-format/udiff.go`) or `whole` (`agents/s02-wholefile-format/wholefile.go`) and the only thing that changes on the diagram is the concrete type behind the "Coder" lifeline; not a line outside `main` needs to change. Command dispatch (s06) and format selection (s05) are the two *dynamic* decision points; everything else is a compile-time-fixed stable call.

## 故意省略 / Deliberate Omissions

mini-aider teaches the load-bearing mechanisms; upstream aider piles production-grade machinery around each one. The table below lists what we **deliberately** cut and why.

| Upstream feature | Upstream path | Why we skip it |
|---|---|---|
| tree-sitter multi-language repomap | `aider/repomap.py` (`get_tags` via `grep_ast`/`tree_sitter`, L233+) | s08 uses Go's `go/parser` over Go source only, to teach PageRank as the core; the 40+ language parsing stack needs cgo and would steal the spotlight. |
| Streaming output (live tokens) | `aider/coders/base_coder.py` (`run_stream` L859, `self.stream` L1442) | s10 *implements* SSE decoding (`StreamingProvider`), but s_full defaults to a one-shot call to avoid wiring live markdown rendering into the integration layer. |
| architect/editor dual coder | `aider/coders/architect_coder.py` (`ArchitectCoder`, `editor_model` L22) | "one model proposes, another applies the edit" is advanced orchestration; we implement only the single-coder path to keep the loop one main line. |
| Chat history summarization | `aider/history.py` (`ChatSummary.summarize` L27) | Background summarization when multi-turn history exceeds budget is a context-management optimization, orthogonal to running one edit end to end. |
| Voice input `/voice` | `aider/voice.py` (`Voice` L33) + `aider/commands.py` (`cmd_voice` L1252) | Whisper speech-to-text is an input-method side branch; it touches no step of the coder loop. |
| Browser GUI | `aider/gui.py` (streamlit) + `aider/main.py` (`check_streamlit_install` L208) | The streamlit web UI is an alternative front end to the CLI; we ship terminal only. |
| Web scraping `/web` | `aider/scrape.py` (`Scraper`, playwright L19) | Scraping a URL into context needs a playwright headless browser, a content-fetch side branch. |
| File watch + AI comments | `aider/watch.py` (`FileWatcher` L65, `get_ai_comments` L257) | Watching `# ai` comments for in-IDE iteration is another input trigger, orthogonal to the interactive loop. |
| Telemetry / analytics | `aider/analytics.py` (`Analytics`, posthog/mixpanel L8-9) | PostHog/Mixpanel instrumentation is pure ops; a teaching build wants none of it. |
| Full `.aider.conf.yml` surface | `aider/args.py` (config-file search L794) + `sample.aider.conf.yml` | Upstream has 50+ CLI/YAML/env config knobs across three sources; we parse only the load-bearing `--model`/`--edit-format`/`--yes`. |

## 延伸阅读 / Read Further

- **Start from the loop skeleton:** read this chapter's 14-step trace against upstream `aider/coders/base_coder.py`'s `run` → `run_one` (L876-944) → `send_message` (L1419) — you'll see our pipeline is its backbone minus streaming, token budgeting, and dry-run.
- **The real multi-provider layer:** s10 reproduces the *shape* of litellm with a hand-written registry plus a retry decorator. Read upstream `aider/models.py`'s `send_completion` (L985) and `simple_send_with_retries` (L1039-1079) to see how the real thing unifies exception classification and stream chunking across 50+ backends.
- **The algorithmic heart of RepoMap:** this repo's Appendix A hand-works `aider/repomap.py`'s `get_ranked_tags` (L365-575) — the def→ref graph + PageRank + personalization vector + greedy budget fill. To see how tree-sitter extracts tags for 40+ languages, read down from `get_tags` (L233).
- **The full spectrum of edit formats:** we implement whole/diff/udiff; upstream `aider/coders/` has 16 more `*_coder.py` variants (patch, ask, architect, func, etc.). Pick `editblock_coder.py`'s `find_original_update_blocks` (L439-560) against s03 to feel the edge-case handling of a production parser.
- **The upstream file map:** this repo's Appendix B indexes the whole `aider/` tree by chapter — from any `agents/sNN` Go file you can trace back to its Python ancestor and the exact line ranges read.
