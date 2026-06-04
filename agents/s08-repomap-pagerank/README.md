# s08 · RepoMap (tags + PageRank under a token budget) / RepoMap：标签 + PageRank + token 预算

A whole repo won't fit in the prompt — so rank its symbols and emit the most central ones that fit. / 整个仓库塞不进提示词——于是给符号排名，挑出最核心的、能装下的那些。

A multi-file codebase is far too big to paste into an LLM call, but the model still needs to *see* its structure to edit it. s08 builds a **RepoMap**: it scans every file for **definitions** (`func`/`type`/`class`/`def NAME`) and **references** (identifiers in use), wires them into a directed graph (referencer → definer), runs **PageRank** over that graph so symbols everyone depends on float to the top, then greedily emits the highest-ranked symbols until a **token budget** is hit. The result is a compact, read-only "map" of the repo injected into the model's context. PageRank is hand-rolled (power iteration, 20 rounds, damping 0.85) — no graph library.
多文件仓库远大于一次 LLM 调用能容纳的量，但模型仍需*看见*它的结构才能改它。s08 构建一张 **RepoMap**：扫描每个文件的**定义**（`func`/`type`/`class`/`def 名字`）与**引用**（被使用的标识符），连成一张有向图（引用方 → 定义方），在图上跑 **PageRank**，让被大家依赖的符号浮到顶端，再贪心地输出排名最高的符号，直到撞上 **token 预算**。结果是一张紧凑的、只读的仓库"地图"，注入模型上下文。PageRank 是手写的（幂迭代，20 轮，阻尼 0.85）——不依赖任何图库。

> **Fidelity note.** Upstream aider uses tree-sitter (40+ languages) to extract tags precisely. s08 uses a **heuristic regex** extractor instead — it understands a few `def` keywords and a loose notion of "identifier", so it will miss methods and let some keywords/locals slip through. The *graph + PageRank + budgeting* pipeline is faithful; only the extractor is simplified.
> **保真度说明。** 上游 aider 用 tree-sitter（40+ 语言）精确抽取标签。s08 用**启发式正则**抽取——只认识几个 `def` 关键字和一个宽松的"标识符"概念，所以会漏掉方法、放过一些关键字/局部变量。*图 + PageRank + 预算*这条主线是忠实的；只有抽取器被简化了。

## Run / 运行

```bash
cd agents/s08-repomap-pagerank

# Map a directory under a token budget (no network, no API key).
# 在 token 预算下给一个目录建图（无网络、无 key）。
go run . -dir .                       # map this module / 给本模块建图
go run . -dir ../s05-prompt-system    # map another chapter / 给另一章建图

# See the dependency graph + PageRank scores that drive the map.
# 查看驱动这张地图的依赖图与 PageRank 分数。
go run . -dir . -v

# A tighter budget keeps fewer (higher-ranked) symbols.
# 更紧的预算只保留更少（排名更高）的符号。
go run . -dir . -tokens 80

# Boost files "in the chat" — biases the ranking toward their dependencies.
# 把文件标记为"在对话中"——让排名偏向它们的依赖。
go run . -dir . -chat repomap.go

# Print the exact read-only Message the loop (s01..s07) would inject.
# 打印 loop（s01..s07）会注入的那条只读 Message。
go run . -dir . -as-message

# tests (no network) / 测试（无网络）
go test ./...
```

## Files / 文件

| File | What it is / 是什么 |
|------|--------------------|
| `repomap.go` | **The heart of the chapter.** `extractTags` (heuristic def/ref scanner), `buildGraph` (def→ref graph), `pageRank` (hand-rolled power iteration), `rankTags` + `renderMap` (budget fill), and the `RepoMap` entry point. **Start here.** / **本章核心。** 抽取器、建图、手写 PageRank、排名与预算填充、`RepoMap` 入口。**从这里读起。** |
| `provider.go` | The generic LLM core (Anthropic wire shape) + the shared `Tag` type. Unchanged transport vs s01..s07. / 通用 LLM 核心 + 共享的 `Tag` 类型。传输层与 s01..s07 一致。 |
| `main.go` | CLI: point at a dir, print the ranked map under a `-tokens` budget; `-v` dumps the graph + scores. / 命令行：指向目录，在 `-tokens` 预算下打印排名地图；`-v` 打印图与分数。 |
| `repomap_test.go` | Tests: extract a def, extract refs, PageRank ranks a referenced-by-many symbol higher, budget truncates output, deterministic ordering, personalization boost, empty repo. / 测试：抽取定义、抽取引用、PageRank、预算截断、确定性、个性化加权、空仓库。 |
| `testdata/expected.txt` | An illustrative (deterministic) map of a tiny sample repo. / 一个小示例仓库的（确定性）地图记录。 |

## Key teaching points / 关键教学点

1. **A repo is a graph; centrality is rank.** Treat each file as a node and each cross-file symbol use as an edge `referencer → definer`. A file whose symbols everyone imports is like a web page everyone links to — PageRank surfaces it. See [`repomap.go` `buildGraph` / `pageRank`](./repomap.go).
   **仓库是一张图，中心性就是排名。** 每个文件是节点，每次跨文件的符号使用是一条 `引用方 → 定义方` 的边。被大家导入的文件就像被大家链接的网页——PageRank 把它顶出来。

2. **PageRank by power iteration, no library.** `rank(v) = (1-d)·p(v) + d·Σ rank(u)·w(u,v)/out(u)`, iterated ~20 times with damping `d=0.85`. Writing it out makes the "everyone hands their score to who they point at, plus a teleport" intuition visible.
   **用幂迭代实现 PageRank，不用库。** 上面那个递推式迭代约 20 次、阻尼 `d=0.85`。把它写出来，"每个节点把自己的分数交给它指向的人，再加一份传送"的直觉就一目了然。

3. **Personalization biases the map toward your code.** Files "in the chat" concentrate the teleport vector `p`; what they depend on inherits more rank. This is upstream's `nx.pagerank(personalization=...)`.
   **个性化让地图偏向你正在改的代码。** "在对话中"的文件把传送向量 `p` 集中过去，它们依赖的东西就分到更多排名。这正是上游的 `nx.pagerank(personalization=...)`。

4. **A ranking becomes a fixed-size map via a greedy budget fill.** Walk symbols best-first, add each (and its file header) while the running `~chars/4` token estimate stays under the cap, stop when the next one would overflow. That turns "infinite ranked list" into "fits in the prompt".
   **靠贪心预算填充，把排名变成定长地图。** 按排名从高到低遍历符号，在 `~字符数/4` 的 token 估算不超预算时逐个加入（含文件头），下一个会溢出时就停。于是"无限长的排名表"变成"装得进提示词"。

See the full chapter write-up: [`docs/en/s08-repomap-pagerank.md`](../../docs/en/s08-repomap-pagerank.md) · [`docs/zh/s08-repomap-pagerank.md`](../../docs/zh/s08-repomap-pagerank.md).
完整章节讲解见上面两个文档。
