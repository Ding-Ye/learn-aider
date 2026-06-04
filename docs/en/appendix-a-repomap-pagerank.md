---
title: "Appendix A · RepoMap PageRank intuition"
appendix: A
slug: appendix-a-repomap-pagerank
est_read_min: 12
---

# 附录 A · RepoMap 的 PageRank 直觉 / Appendix A · RepoMap PageRank intuition

> Companion chapter: [s08 RepoMap](./s08-repomap-pagerank.md). Upstream is `aider/repomap.py`, pinned at SHA `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`.

---

## 这份附录讲什么 / What this appendix is about

s08 teaches you to turn a repository into ranked, token-budgeted context: scan for symbols → build a graph → run PageRank → select under a token budget. To fit that into ~950 lines of Go, the chapter cuts a lot of corners — it extracts symbols with regex heuristics instead of tree-sitter, and it hand-rolls PageRank as a few dozen lines of power iteration instead of calling `networkx`.

But RepoMap is the most *algorithmically* interesting piece of aider, and the one most worth pulling out for a deep look. It stitches together two domains that look unrelated at first:

- **tree-sitter** — an incremental-parser framework that produces real syntax trees for 40+ languages, letting aider precisely distinguish a *definition* (`def`) from a *reference* (`ref`);
- **PageRank** — the eigenvector-centrality algorithm Google originally used to rank web pages, borrowed here to rank **code symbols**.

Glue those together, add a token-budget constraint on top, and you get aider's core trick: letting a model "see" the skeleton of a codebase that could never fit in a context window. This appendix exists to explain **why that combination is the right one**, **exactly where it lives upstream**, and **what our mini version actually simplifies**. It is reading material, not a coding chapter — by the end you should be able to go back to `aider/repomap.py` and read those 200 lines on your own.

## 心智模型 / The mental model

Picture the whole thing as a four-stage pipeline. Each stage's output is the next stage's input:

```
source files          tags (def/ref)          directed graph        PageRank             budget fill
┌────────┐  extract ┌──────────────┐  edges  ┌──────────────┐  power  ┌──────────┐  binary┌──────────┐
│ cart.go│ ───────▶ │ def Store    │ ──────▶ │ cart ──▶ store│ ──────▶ │store 0.41│ search │ map text │
│store.go│          │ ref Store ×3 │         │ report ─▶store│         │cart  0.22│ ─────▶ │ (≤budget)│
│report  │          │ def Checkout │         │ store ──▶...  │         │report0.18│        └──────────┘
└────────┘          └──────────────┘         └──────────────┘         └──────────┘
```

**Stage 1: tags (definitions and references).** Each file is parsed into a list of `Tag`s. Upstream models this as a namedtuple: `Tag = namedtuple("Tag", "rel_fname fname line name kind")` (`repomap.py:29`), where `kind` is `"def"` or `"ref"`. `def Store` means "this file defines the symbol Store"; `ref Store` means "this file uses Store".

**Stage 2: the graph.** Nodes are **files**, not symbols. For every identifier that is referenced in file A and defined in file B, add an edge `A → B` (referencer points at definer), weighted by how many times it is used. The intuition is simple: **a `referencer → definer` edge is exactly a hyperlink in the web world.** A file that everyone imports is like a web page that everyone links to.

**Stage 3: personalized PageRank.** Run PageRank over that graph. A file pointed at by many high-scoring files scores high; an isolated, unreferenced file scores low. The crucial twist is **personalization**: bias PageRank's "random teleport" vector toward "files currently in the chat", so those files' dependencies inherit a higher score — the map leans toward the code you are editing. Upstream also **spreads a file's score across the symbols it defines**, so the final ranking is per-*symbol*, not just per-file.

**Stage 4: the budget fill.** A ranking is an infinitely long list, but a prompt is finite. So walk from highest score down, packing symbols in one at a time while a "~4 chars = 1 token" estimate of the running size stays under the cap, and stop when the next one would overflow. Upstream uses a **binary search** to home in on the budget ceiling (`get_ranked_tags_map_uncached`, `repomap.py:666-706`: `middle = max_map_tokens // 25`, repeatedly halved) rather than a naive linear greedy fill — the goal is to hug the token cap more tightly.

Put together: **a codebase that wouldn't fit gets compressed into a single block of "the symbols the model most needs to see", sized to slot into the prompt.**

## 在上游里的位置 / How it shows up in upstream

Every bullet below can be read directly as `read aider/repomap.py:<lines>`, with line numbers pinned to SHA `5dc9490`:

- read `aider/repomap.py:29` — the `Tag` namedtuple (`rel_fname fname line name kind`), the atomic data structure for the whole pipeline.
- read `aider/repomap.py:103-167` — `get_repo_map()`, the public entry point. It handles token-budget padding (a wider view when no chat files are present), then delegates to `get_ranked_tags_map`.
- read `aider/repomap.py:233-278` — `get_tags()` and its caching layer; the real parsing happens in `get_tags_raw()` (`repomap.py:279`), which runs tree-sitter queries to extract def/ref tags.
- read `aider/repomap.py:365-372` — the head of `get_ranked_tags()`: the `defines`, `references`, and `definitions` dicts that feed graph construction.
- read `aider/repomap.py:374-445` — construction of the personalization vector: how chat files, mentioned filenames, and mentioned identifiers each get weighted (`personalize = 100 / len(fnames)`).
- read `aider/repomap.py:470-514` — building the graph with `nx.MultiDiGraph()`, plus the long list of weight multipliers (snake/camel names ×10, mentioned ×10, in-chat referencer ×50, `num_refs` square-rooted to dampen high-frequency mentions).
- read `aider/repomap.py:519-531` — the line `nx.pagerank(G, weight="weight", **pers_args)` is the core call; note it reuses `personalization` as the `dangling` vector and guards against `ZeroDivisionError`.
- read `aider/repomap.py:533-574` — after PageRank, how each source node's rank is spread along its out-edges onto `(target file, identifier)` pairs, then sorted into `ranked_tags`.
- read `aider/repomap.py:576-627` — `get_ranked_tags_map()`: the caching and refresh policy (auto/files/always/manual) around `get_ranked_tags`.
- read `aider/repomap.py:666-706` — the **binary-search budget fill** inside `get_ranked_tags_map_uncached()`: render the first `middle` tags with `to_tree()`, estimate with `token_count()`, then halve depending on whether it exceeds `max_map_tokens`.
- read `aider/repomap.py:710-746` — `render_tree()`, which uses `grep-ast`'s `TreeContext` to render the selected "lines of interest" into code snippets with surrounding context.

## 我们的 mini 为什么简化 / Why our mini simplifies it

s08 is the most algorithm-heavy chapter in the curriculum, yet it is still a **teaching version** that deliberately drops three classes of complexity, for these reasons:

**No tree-sitter.** Upstream uses tree-sitter + `grep-ast` to produce real syntax trees for 40+ languages, then extracts def/ref precisely with per-language `.scm` queries. Porting that to Go means either pulling in cgo (`smacker/go-tree-sitter`) or writing a parser by hand — both would steal the show and bury the real lesson, which is "graph + PageRank". So the mini version extracts symbols with **regex heuristics** and narrows scope to Go source (optionally the stdlib `go/parser`, zero cgo). This makes our def/ref less accurate (an identifier appearing in a comment might be misclassified), but the *shape* of the pipeline is faithful.

**Heuristic tags, drop the weight multipliers.** Upstream's stack of multipliers (`repomap.py:487-499`: long identifiers ×10, mentioned ×10, private `_`-prefixed ×0.1, defined in more than 5 places ×0.1) are empirical values from years of tuning. The mini version keeps only the two most central signals — reference count and chat personalization — and leaves the rest as a commented extension exercise: they improve quality but are not required to understand PageRank.

**Small PageRank, no networkx dependency.** Behind upstream's one-liner `nx.pagerank(...)` sits networkx's rigorously tested implementation (sparse matrices, dangling-node handling, a convergence criterion). The mini version hand-rolls ~40 lines of power iteration (damping 0.85, a fixed ~20 rounds, no convergence check), approximates `tiktoken` with "4 chars ≈ 1 token", and uses a naive linear greedy fill instead of binary search. These are **deliberately lossy simplifications**: for a teaching repo of a few dozen files the resulting ranking matches upstream; but for a real repo with thousands of files that needs caching and incremental parsing, go back to the upstream implementation.

In one sentence: **the mini version teaches the intuition for "why a def→ref graph plus PageRank surfaces central code", and leaves "doing it fast and correctly across 40 languages and thousands of files" to upstream.**

## 延伸阅读 / Further reading

- **The original PageRank paper**: S. Brin, L. Page, *The Anatomy of a Large-Scale Hypertextual Web Search Engine* (1998); and L. Page et al., *The PageRank Citation Ranking* (1999). For an algorithm overview see Wikipedia at <https://en.wikipedia.org/wiki/PageRank>. Understanding "eigenvector centrality + damped random walk" is enough.
- **NetworkX's pagerank docs**: <https://networkx.org/documentation/stable/reference/algorithms/generated/networkx.algorithms.link_analysis.pagerank_alg.pagerank.html> — upstream calls this directly at `repomap.py:525`, and the docs spell out the semantics of the `personalization` and `dangling` parameters.
- **tree-sitter homepage**: <https://tree-sitter.github.io/tree-sitter/> — incremental parsing, the query syntax (`.scm`), and the engine behind `grep-ast` (<https://github.com/Aider-AI/grep-ast>) that aider depends on.
- **Aider's official repo-map docs**: <https://aider.chat/docs/repomap.html> — the author's own walkthrough of RepoMap's design motivation and token-budget strategy, a good cross-check against this appendix.
- **Within this curriculum**: first read the Go implementation in [s08](./s08-repomap-pagerank.md), then read upstream `aider/repomap.py:365-574`, and finally return to the symbol cross-reference table in [Appendix B](./appendix-b-upstream-map.md) to line the two sides up one by one.
