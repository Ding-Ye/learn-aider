---
title: "s08 · RepoMap (tags + PageRank under a token budget)"
chapter: 8
slug: s08-repomap-pagerank
est_read_min: 16
---

# s08 · RepoMap (tags + PageRank under a token budget)

> What this teaches: the *RepoMap* — how aider lets a model "see" a codebase too large to paste. Scan every file for **definitions** and **references**, wire them into a directed graph (referencer → definer), run **PageRank** so the symbols everyone depends on float to the top, then greedily emit the highest-ranked ones until a **token budget** is hit. This is the most algorithmically rich chapter in the curriculum: a real graph algorithm, hand-rolled, in the service of context selection.

---

## Problem / 问题

Every chapter so far assumed the relevant code was already *in the chat* — pasted in full so the model could read it. That breaks the moment the repo is bigger than the context window. A 50-file project can't be dumped into a prompt, but the model still needs to know the *shape* of the codebase: which file defines `Store`, where `Checkout` lives, what depends on what. Without that, it hallucinates symbols, edits the wrong file, or asks for files it can't see.

The naive fix — "list every file and its functions" — doesn't scale either: the listing itself blows the budget, and an unranked dump buries the three symbols that matter under a thousand that don't. What we actually need is a way to *rank* symbols by importance and emit only the top slice that fits a fixed token budget. The hard part is "importance": importance of a symbol is not how often it appears, but how *central* it is — how much the rest of the code leans on it. That is a graph question, and PageRank is the classic answer.

## Solution / 解决方案

Model the repo as a graph and let centrality fall out of it. Three ideas carry the design:

1. **A repo is a directed graph; an edge means "depends on".** Each file is a node. For every identifier defined in one file and used in another, add an edge `referencer → definer`, weighted by how many times it's used. A file whose symbols everyone imports is exactly like a web page everyone links to.
2. **PageRank turns "linked-by-many" into a score.** Run PageRank over that graph (power iteration, ~20 rounds, damping 0.85). A file pointed at by many high-ranking files scores high; an unreferenced file scores low. We split a file's score across the symbols it defines so the ranking is *per-symbol*, not just per-file. **Personalization** biases the teleport vector toward files "in the chat", so their dependencies inherit rank — the map leans toward the code you're working on.
3. **A ranking becomes a fixed-size map via a greedy budget fill.** Walk symbols best-first, add each one while the running `~chars/4` token estimate stays under the cap, stop when the next would overflow. An infinite ranked list becomes a block that fits the prompt.

The honest caveat: upstream extracts tags with **tree-sitter** (40+ languages, real grammars). s08 uses a **heuristic regex** extractor instead. The extractor is the simplified part; the graph + PageRank + budget pipeline is faithful.

## How It Works / 工作原理

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  files ──▶ extractTags ──▶  Tag{Path,Name,Kind:def|ref,Line}    │
│                                                                │
│  buildGraph:   one node per FILE, edge referencer ─▶ definer    │
│                                                                │
│      cart.go ──3──▶ store.go ◀──3── report.go     unused.go     │
│         │              ▲                              (no edges)│
│         └── uses Store, Count, Add ──┘                          │
│                                                                │
│  pageRank (power iteration, d=0.85, ~20 iters):                 │
│      store.go 0.47   cart.go 0.18   report.go 0.18  unused 0.18 │
│                                                                │
│  rankTags ─▶ split each file's rank over its defs ─▶ sort       │
│  renderMap ─▶ emit best-first until ~chars/4 hits the budget    │
└────────────────────────────────────────────────────────────────┘
```

The hand-rolled PageRank — the algorithmic core (excerpt from [`agents/s08-repomap-pagerank/repomap.go`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s08-repomap-pagerank/repomap.go)):

```go
// rank(v) = (1-d)*p(v) + d * Σ_{u -> v} rank(u) * weight(u,v) / outWeight(u)
func pageRank(g *graph) map[string]float64 {
	n := len(g.nodes)
	rank := make(map[string]float64, n)
	if n == 0 {
		return rank
	}

	// Personalization (teleport) vector p, summing to 1: all mass on chat
	// files if any, else uniform. This is what biases the map toward your code.
	p := make(map[string]float64, n)
	chatCount := 0
	for _, v := range g.nodes {
		if g.personal[v] {
			chatCount++
		}
	}
	if chatCount > 0 {
		for _, v := range g.nodes {
			if g.personal[v] {
				p[v] = 1.0 / float64(chatCount)
			}
		}
	} else {
		for _, v := range g.nodes {
			p[v] = 1.0 / float64(n)
		}
	}

	outWeight := make(map[string]float64, n)
	for u := range g.edges {
		for _, w := range g.edges[u] {
			outWeight[u] += w
		}
	}
	for _, v := range g.nodes {
		rank[v] = p[v]
	}

	for iter := 0; iter < pageRankIters; iter++ {
		next := make(map[string]float64, n)
		dangling := 0.0 // nodes with no out-edges leak their score; spread via p
		for _, v := range g.nodes {
			if outWeight[v] == 0 {
				dangling += rank[v]
			}
		}
		for _, v := range g.nodes {
			next[v] = (1.0-pageRankDamping)*p[v] + pageRankDamping*dangling*p[v]
		}
		for u, outs := range g.edges { // push rank along out-edges
			ow := outWeight[u]
			if ow == 0 {
				continue
			}
			share := pageRankDamping * rank[u] / ow
			for v, w := range outs {
				next[v] += share * w
			}
		}
		rank = next
	}
	return rank
}
```

**Four non-obvious points**:

1. **Edges point *toward* the dependency, not away.** `referencer → definer` means "the rank flows to the thing being used." PageRank rewards nodes that *receive* edges, so the most-used definitions win — which is exactly what you want at the top of a repo map.
2. **Personalization is a teleport vector, not an edge fudge.** An earlier draft boosted edge *weights* from chat files; that does nothing when a chat file has a single out-edge (the proportion is unchanged). The faithful mechanism — and what fixes it — is concentrating the `(1-d)` teleport mass on chat files, so the rank they radiate genuinely lifts their dependencies. This mirrors `nx.pagerank(personalization=...)`.
3. **Dangling nodes would leak rank.** A file that defines symbols but references nothing else has no out-edges; its score has nowhere to go. Each round we collect that "dangling" mass and redistribute it along `p`, matching networkx's `dangling=personalization` default — otherwise total rank drains away and the scores stop summing to 1.
4. **Per-symbol rank ≠ per-file rank.** `store.go` has the highest *file* rank, but it defines four symbols, so each inherits a quarter — landing *below* a file with one heavily-used symbol. We rank symbols, so a lone, central definition (Test 3's `Core`) outranks four siblings that split a bigger pie. That's a feature: the map favors focused, depended-upon symbols.

## What Changed (vs. s07) / 与 s07 的变化

s07 ended the "make one edit safe" arc: every AI change became an atomic git commit (`autoCommit` after `ApplyEdits`). The loop knew about *files in the chat* but had no notion of the *rest of the repo*. s08 adds that missing half — a structural, ranked view of everything else — and a brand-new graph algorithm to produce it.

```diff
-// s07: the loop only ever saw files explicitly in the chat.
-edited, _, _ := coder.ApplyEdits(edits)
-repo.Commit(edited)                     // persist the change; that's the whole world
+// s08: before the loop runs, build a ranked map of the REST of the repo and
+// inject it read-only, so the model can "see" code that was never pasted in.
+files, _ := readRepoFiles(root)
+repoMap := RepoMap(files, budgetTokens, chatFiles) // tags -> graph -> PageRank -> budget
+msg := Message{Role: "user", Content: []ContentBlock{{
+	Type: "text",
+	Text: "Here are summaries of files in my git repository.\n" +
+		"Do not propose changes to these files, treat them as *read-only*.\n\n" + repoMap,
+}}
```

Semantically: s01–s07 grew the *write* path (parse an edit, apply it, commit it). s08 is the first *read* path that scales — it decides *what the model gets to see* when the codebase is too big to show in full. Context selection stops being "whatever the user pasted" and becomes an algorithm.

## Try It / 动手试一试

```bash
cd agents/s08-repomap-pagerank

# Map a directory under a token budget (no network, no API key).
go run . -dir .

# See the dependency graph + PageRank scores that drive the map.
go run . -dir ../s05-prompt-system -v

# A tighter budget keeps fewer (higher-ranked) symbols.
go run . -dir . -tokens 80

# Boost files "in the chat" — biases the ranking toward their dependencies.
go run . -dir . -chat repomap.go

# Print the exact read-only Message the loop (s01..s07) would inject.
go run . -dir . -as-message

# tests (no network — the whole pipeline is deterministic)
go test -v ./...
```

Expected output shape:

```
# go run . -dir <small repo> -v   (deterministic; no LLM):
[s08] scanned 4 files under ..., budget=256 tokens
[s08] dependency edges (referencer -> definer  weight):
    cart.go -> store.go  3.0
    report.go -> store.go  3.0
[s08] PageRank (higher = more central):
    0.4737  store.go
    0.1754  cart.go
    ...
===== REPO MAP (budget=256 tokens, ~45 used) =====
cart.go:
  Checkout  (line 3)
store.go:
  Add  (line 8)
  Count  (line 10)
  ...
```

Tighten `-tokens` and lower-ranked symbols drop off the bottom while the central ones survive; add `-chat cart.go` and the map reorders toward `cart.go`'s dependencies. A full illustrative transcript lives in [`testdata/expected.txt`](https://github.com/Ding-Ye/learn-aider/blob/main/agents/s08-repomap-pagerank/testdata/expected.txt).

## Upstream Source Reading / 上游源码阅读

aider's ranking lives in `repomap.py`'s `get_ranked_tags` (L365-574). After extracting tags with tree-sitter, it builds an `nx.MultiDiGraph` of `referencer → definer` edges, weights them with a stack of heuristics (mentioned idents ×10, "interesting" names ×10, private ×0.1, chat files ×50, `sqrt(count)` damping), then calls `nx.pagerank` with a personalization vector. The excerpt below is the graph-build-through-PageRank core — the exact stretch s08 reimplements in stdlib.

```upstream:aider/repomap.py#L470-525
        G = nx.MultiDiGraph()

        # Add a small self-edge for every definition that has no references
        # Helps with tree-sitter 0.23.2 with ruby, where "def greet(name)"
        # isn't counted as a def AND a ref. tree-sitter 0.24.0 does.
        for ident in defines.keys():
            if ident in references:
                continue
            for definer in defines[ident]:
                G.add_edge(definer, definer, weight=0.1, ident=ident)

        for ident in idents:
            definers = defines[ident]

            mul = 1.0
            is_snake = ("_" in ident) and any(c.isalpha() for c in ident)
            is_kebab = ("-" in ident) and any(c.isalpha() for c in ident)
            is_camel = any(c.isupper() for c in ident) and any(c.islower() for c in ident)
            if ident in mentioned_idents:
                mul *= 10
            if (is_snake or is_kebab or is_camel) and len(ident) >= 8:
                mul *= 10
            if ident.startswith("_"):
                mul *= 0.1
            if len(defines[ident]) > 5:
                mul *= 0.1

            for referencer, num_refs in Counter(references[ident]).items():
                for definer in definers:
                    use_mul = mul
                    if referencer in chat_rel_fnames:
                        use_mul *= 50

                    # scale down so high freq (low value) mentions don't dominate
                    num_refs = math.sqrt(num_refs)

                    G.add_edge(referencer, definer, weight=use_mul * num_refs, ident=ident)

        if not references:
            pass

        if personalization:
            pers_args = dict(personalization=personalization, dangling=personalization)
        else:
            pers_args = dict()

        try:
            ranked = nx.pagerank(G, weight="weight", **pers_args)
        except ZeroDivisionError:
            ...
```

**Reading notes**:

- **`nx.pagerank` vs. our power iteration.** Upstream calls networkx; s08 writes the fixed-point out by hand (~20 iterations, damping 0.85 — networkx's default `alpha`). The math is identical; we trade a dependency for visibility into the loop.
- **The weight heuristics we omit.** `mul *= 10` for mentioned/"interesting" names, `*= 0.1` for private or ubiquitous idents, `sqrt(num_refs)` damping — these are real upstream tuning. s08 uses the raw cross-file reference count so the graph stays legible; the heuristics are an obvious extension, not a different algorithm.
- **Personalization, faithfully.** Upstream passes `personalization=...` *and* `dangling=...`. s08 builds the same teleport vector `p` and reuses it for dangling redistribution — that pairing is exactly why a chat file lifts its dependencies rather than just itself.
- **The self-edge corner case.** Upstream adds a `weight=0.1` self-edge for defs with no refs (a tree-sitter version quirk for Ruby). s08 has no tree-sitter, so it skips this; a def with no cross-file refs simply becomes a dangling node, handled by the dangling redistribution.
- **A simplification we keep on purpose.** Upstream's budget fill is a *binary search* over how many tags to include, re-rendering and token-counting each probe (`get_ranked_tags_map` L666-706). s08 uses a single greedy pass that stops before the `~chars/4` estimate exceeds the budget — correct and `O(n)`, just not size-optimal.

**Read further**: start at `aider/repomap.py` → `get_ranked_tags` (the excerpt above), follow `ranked_tags` into `to_tree` (L748) and `render_tree` (L710) to see how symbols become a rendered string, then up into `aider/coders/base_coder.py` → `get_repo_map` to see that string injected as a read-only message. That trace — extract → graph → rank → render → inject — is the real-source map for s08 → s09 (which lints the edits the model makes against this map) → the s_full integration.

---

**Next**: s09 closes the loop — after an edit is applied and committed, a **Linter** runs; if it errors, the failure is fed back as a reflected message and the loop re-enters, so the model can fix its own mistakes without a human round-trip.
