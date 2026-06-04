package main

// repomap.go — the RepoMap: a ranked, token-budgeted summary of a whole repo.
//
// THE PROBLEM. A real codebase (tens of thousands of lines across hundreds of
// files) cannot be pasted into an LLM prompt. But the model still needs to
// "see" the shape of the repo — which files define which functions/types — to
// edit it sanely. We need a COMPACT, RANKED map that fits a token budget and
// surfaces the most important symbols first.
//
// THE MECHANISM (4 stages):
//
//  1. EXTRACT TAGS. For every file, find definitions (func/type/const/var NAME)
//     and references (every identifier that is used). Upstream uses tree-sitter
//     queries for 40+ languages (repomap.py get_tags L233). We use a heuristic
//     regex scanner — see the fidelity note in extractTags. Each hit is a Tag
//     {Path, Name, Kind:"def"|"ref", Line}.
//
//  2. BUILD A GRAPH. Make every FILE a node. For each identifier that is defined
//     in one file and referenced in another, add a directed edge
//     referencer -> definer. Edge weight = how many times the referencer used
//     that identifier. This is the "who depends on whom" graph (upstream
//     nx.MultiDiGraph, repomap.py L470-514).
//
//  3. PAGERANK. Run PageRank over that graph (repomap.py nx.pagerank L525). A
//     file pointed at by many other files — i.e. one whose symbols everyone
//     uses — gets a high score, exactly like a web page linked by many pages.
//     We implement PageRank by hand with power iteration (no graph library):
//     ~20 iterations, damping 0.85. See pageRank below.
//
//  4. RANK TAGS + FILL A BUDGET. Spread each file's PageRank across the
//     definitions it exports, sort definitions by score, then greedily emit the
//     top ones until the token budget (~chars/4) is hit (upstream
//     get_ranked_tags L533-558 + the binary-search budget fill in
//     get_ranked_tags_map L666-706). The result is the "repo map" string.
//
// FIDELITY GAP (read this). This is a SIMPLIFIED RepoMap. We do NOT use
// tree-sitter: extractTags is a regex/heuristic that only understands a few
// def keywords and a loose notion of "identifier". It will miss methods, treat
// keywords/builtins as refs, and has no scope awareness. The POINT of the
// chapter is the graph + PageRank + budgeting pipeline, which is identical in
// spirit to upstream; swapping the extractor for a real parser is the obvious
// extension (see docs).

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---- Stage 1: tag extraction -------------------------------------------------

// defRE matches a top-of-construct definition in a handful of languages:
// Go (func/type/const/var), Python (def/class), and the generic `class NAME`.
// Group 1 is the defined identifier. The optional `\([^)]*\)\s*` after `func`
// skips a Go method receiver (`func (g *graph) tagKey` -> captures `tagKey`)
// without swallowing the name of a plain function. This is deliberately loose:
// upstream gets precision from a tree-sitter grammar; we get breadth from a
// regex and accept the noise (see the fidelity note at the top of the file).
var defRE = regexp.MustCompile(`(?m)^\s*(?:func\s+(?:\([^)]*\)\s*)?|type\s+|class\s+|def\s+|const\s+|var\s+)([A-Za-z_][A-Za-z0-9_]*)`)

// identRE matches any identifier-shaped token. We treat each one as a potential
// reference; an identifier is only a meaningful ref if some OTHER file defines
// it (the graph build filters that out), so over-matching here is harmless.
var identRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// stopWords are language keywords/builtins we never want to count as references.
// Without this filter, `if`, `return`, `int`, etc. would dominate the graph.
var stopWords = map[string]bool{
	"func": true, "type": true, "class": true, "def": true, "const": true,
	"var": true, "package": true, "import": true, "return": true, "if": true,
	"else": true, "for": true, "range": true, "switch": true, "case": true,
	"default": true, "break": true, "continue": true, "go": true, "defer": true,
	"map": true, "struct": true, "interface": true, "chan": true, "select": true,
	"nil": true, "true": true, "false": true, "string": true, "int": true,
	"bool": true, "error": true, "byte": true, "rune": true, "float64": true,
	"self": true, "None": true, "True": true, "False": true, "in": true,
	"and": true, "or": true, "not": true, "from": true, "as": true, "with": true,
}

// extractTags scans one file's source and returns its definitions and
// references in file order. relPath is what becomes the graph node id; absPath
// is carried so a later step could read the file again to render line context.
//
// A line that contains a definition contributes BOTH a "def" Tag (for the
// defined name) and "ref" Tags for the other identifiers on that line (e.g. the
// types in a function signature) — upstream tree-sitter does the same.
func extractTags(src, relPath, absPath string) []Tag {
	var tags []Tag
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		lineNo := i + 1

		// Definitions first, so we can exclude the defined name from the refs
		// on the same line.
		defName := ""
		if m := defRE.FindStringSubmatch(line); m != nil {
			defName = m[1]
			tags = append(tags, Tag{
				RelFname: relPath, Fname: absPath, Line: lineNo,
				Name: defName, Kind: "def",
			})
		}

		// References: every other identifier on the line.
		for _, id := range identRE.FindAllString(line, -1) {
			if id == defName || stopWords[id] {
				continue
			}
			tags = append(tags, Tag{
				RelFname: relPath, Fname: absPath, Line: lineNo,
				Name: id, Kind: "ref",
			})
		}
	}
	return tags
}

// ---- Stage 2: build the definition->reference graph --------------------------

// graph is the weighted directed graph PageRank runs over. nodes is the sorted
// set of file ids (sorted so iteration — and therefore the result — is
// deterministic). edges[a][b] is the summed weight of edges a -> b, i.e. how
// strongly file a (a referencer) depends on file b (a definer).
type graph struct {
	nodes []string
	edges map[string]map[string]float64

	// personal is the set of files "in the chat"; it biases PageRank's teleport
	// vector toward this code (see pageRank). Carried on the graph so the rank
	// step has it without an extra parameter.
	personal map[string]bool

	// defs maps an identifier to the set of files that define it, and
	// defTags maps (file,ident) to the actual Tags so we can emit them later.
	defs    map[string]map[string]bool
	defTags map[string][]Tag // key: relPath + "\x00" + ident
}

func tagKey(relPath, ident string) string { return relPath + "\x00" + ident }

// buildGraph turns a flat list of tags (across all files) into the graph. Edge
// weight is purely the cross-file reference count (how many times a referencer
// uses a definer's symbol). personalize names the files "in the chat"; rather
// than fudging edge weights, we record the set and let PageRank bias its
// TELEPORT vector toward it — the faithful analog of upstream's
// nx.pagerank(G, personalization=...) (repomap.py L519-525).
func buildGraph(tags []Tag, personalize map[string]bool) *graph {
	g := &graph{
		edges:    map[string]map[string]float64{},
		personal: personalize,
		defs:     map[string]map[string]bool{},
		defTags:  map[string][]Tag{},
	}

	// Index definitions and references.
	defines := map[string]map[string]bool{}   // ident -> set of definer files
	references := map[string]map[string]int{} // ident -> referencer file -> count
	nodeSet := map[string]bool{}

	for _, t := range tags {
		nodeSet[t.RelFname] = true
		switch t.Kind {
		case "def":
			if defines[t.Name] == nil {
				defines[t.Name] = map[string]bool{}
			}
			defines[t.Name][t.RelFname] = true
			if g.defs[t.Name] == nil {
				g.defs[t.Name] = map[string]bool{}
			}
			g.defs[t.Name][t.RelFname] = true
			k := tagKey(t.RelFname, t.Name)
			g.defTags[k] = append(g.defTags[k], t)
		case "ref":
			if references[t.Name] == nil {
				references[t.Name] = map[string]int{}
			}
			references[t.Name][t.RelFname]++
		}
	}

	// One node per file, sorted for determinism.
	for n := range nodeSet {
		g.nodes = append(g.nodes, n)
	}
	sort.Strings(g.nodes)

	// An identifier connects files only if it is BOTH defined and referenced.
	for ident, definers := range defines {
		refs, ok := references[ident]
		if !ok {
			continue
		}
		for referencer, count := range refs {
			for definer := range definers {
				if referencer == definer {
					continue // a file referencing its own symbol is not a dependency
				}
				w := float64(count)
				if g.edges[referencer] == nil {
					g.edges[referencer] = map[string]float64{}
				}
				g.edges[referencer][definer] += w
			}
		}
	}
	return g
}

// ---- Stage 3: PageRank (hand-rolled power iteration) -------------------------

const (
	pageRankDamping = 0.85 // the classic 85% follow-a-link, 15% teleport split
	pageRankIters   = 20   // power iteration converges well within ~20 rounds
)

// pageRank computes a PageRank score for every node by POWER ITERATION — the
// same fixed-point that nx.pagerank computes, just written out so the math is
// visible. The recurrence is, for each node v:
//
//	rank(v) = (1-d)*p(v)  +  d * Σ_{u -> v}  rank(u) * weight(u,v) / outWeight(u)
//
// Every node hands its current score to its out-neighbors in proportion to edge
// weight (the "d" term); the remaining (1-d) mass TELEPORTS according to the
// personalization vector p. With no chat files, p is uniform (1/N) and this is
// plain PageRank. With chat files, p concentrates teleport mass on them, so the
// files THEY reference inherit more rank — this is how aider biases the map
// toward the code you are working on (upstream personalization=..., L519-525).
// Dangling nodes (no out-edges) leak their score; we redistribute it along p
// each round too, matching networkx's dangling=personalization default.
func pageRank(g *graph) map[string]float64 {
	n := len(g.nodes)
	rank := make(map[string]float64, n)
	if n == 0 {
		return rank
	}

	// Build the personalization (teleport) vector p, summing to 1. If any files
	// are "in the chat", put all the mass on them; otherwise spread uniformly.
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

	// Precompute each node's total out-weight once.
	outWeight := make(map[string]float64, n)
	for u := range g.edges {
		for _, w := range g.edges[u] {
			outWeight[u] += w
		}
	}

	// Start from the personalization distribution.
	for _, v := range g.nodes {
		rank[v] = p[v]
	}

	for iter := 0; iter < pageRankIters; iter++ {
		next := make(map[string]float64, n)

		// Score that "leaks" from dangling nodes this round, spread along p.
		dangling := 0.0
		for _, v := range g.nodes {
			if outWeight[v] == 0 {
				dangling += rank[v]
			}
		}
		for _, v := range g.nodes {
			next[v] = (1.0-pageRankDamping)*p[v] + pageRankDamping*dangling*p[v]
		}
		// Push each node's rank along its out-edges.
		for u, outs := range g.edges {
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

// ---- Stage 4: rank tags + fill a token budget --------------------------------

// rankedDef is one exported definition with the PageRank-derived score it
// inherited from its file.
type rankedDef struct {
	relPath string
	ident   string
	score   float64
	tag     Tag
}

// rankTags spreads each file's PageRank across the definitions it exports and
// returns them sorted best-first. A file's rank is split evenly across its defs
// (a coarse stand-in for upstream's per-edge distribution, repomap.py
// L533-545). Ties break on (relPath, ident) so the order is fully deterministic.
func rankTags(g *graph, rank map[string]float64) []rankedDef {
	// Collect, per file, the set of identifiers it defines.
	defsByFile := map[string][]string{}
	for ident, files := range g.defs {
		for f := range files {
			defsByFile[f] = append(defsByFile[f], ident)
		}
	}

	var out []rankedDef
	for _, relPath := range g.nodes {
		idents := defsByFile[relPath]
		if len(idents) == 0 {
			continue
		}
		sort.Strings(idents)
		per := rank[relPath] / float64(len(idents))
		for _, ident := range idents {
			tagsForDef := g.defTags[tagKey(relPath, ident)]
			tag := Tag{RelFname: relPath, Name: ident, Kind: "def"}
			if len(tagsForDef) > 0 {
				tag = tagsForDef[0] // first definition wins (lowest line)
			}
			out = append(out, rankedDef{relPath: relPath, ident: ident, score: per, tag: tag})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score // higher rank first
		}
		if out[i].relPath != out[j].relPath {
			return out[i].relPath < out[j].relPath
		}
		return out[i].ident < out[j].ident
	})
	return out
}

// approxTokens estimates token count the way aider's budget loop does for a
// rough pass: ~4 characters per token. Upstream uses tiktoken for the real
// count; we flag this approximation as deliberately lossy (see docs).
func approxTokens(s string) int { return (len(s) + 3) / 4 }

// renderMap turns ranked definitions into the final repo-map STRING, grouping
// defs under their file and stopping once the token budget would be exceeded.
// This is the greedy budget fill: keep the highest-ranked symbols that fit.
func renderMap(ranked []rankedDef, budgetTokens int) string {
	// Group the chosen defs by file, walking best-first and tracking the budget.
	type fileBlock struct {
		path  string
		lines []string // "  symbol  (line N)" entries, in file order later
		order int      // first-seen rank position, for stable file ordering
	}
	blocks := map[string]*fileBlock{}
	var fileOrder []string

	used := 0
	for _, rd := range ranked {
		entry := fmt.Sprintf("  %s", rd.ident)
		if rd.tag.Line > 0 {
			entry = fmt.Sprintf("  %s  (line %d)", rd.ident, rd.tag.Line)
		}
		// Cost of adding this entry (plus, if new, the file header line).
		cost := approxTokens(entry + "\n")
		fb := blocks[rd.relPath]
		if fb == nil {
			cost += approxTokens(rd.relPath + ":\n")
		}
		if budgetTokens > 0 && used+cost > budgetTokens && len(blocks) > 0 {
			break // budget hit; stop emitting lower-ranked symbols
		}
		if fb == nil {
			fb = &fileBlock{path: rd.relPath, order: len(fileOrder)}
			blocks[rd.relPath] = fb
			fileOrder = append(fileOrder, rd.relPath)
		}
		fb.lines = append(fb.lines, entry)
		used += cost
	}

	// Emit files in first-seen (rank) order; within a file, sort entries by the
	// line number embedded in them so the block reads top-to-bottom.
	var sb strings.Builder
	for _, path := range fileOrder {
		fb := blocks[path]
		sort.SliceStable(fb.lines, func(i, j int) bool { return fb.lines[i] < fb.lines[j] })
		sb.WriteString(path)
		sb.WriteString(":\n")
		for _, ln := range fb.lines {
			sb.WriteString(ln)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

// ---- Public entry point ------------------------------------------------------

// RepoMap is the one-call API: given a set of files and a token budget, produce
// the ranked repo-map string. personalize names the files "in the chat" whose
// dependencies should be weighted up (pass nil for none). This mirrors upstream
// RepoMap.get_repo_map's contract, collapsed to the essentials.
func RepoMap(files map[string]string, budgetTokens int, personalize map[string]bool) string {
	if len(files) == 0 {
		return ""
	}

	// Stage 1: extract tags from every file (sorted for determinism).
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var tags []Tag
	for _, p := range paths {
		tags = append(tags, extractTags(files[p], p, p)...)
	}

	// Stages 2-4.
	g := buildGraph(tags, personalize)
	rank := pageRank(g)
	ranked := rankTags(g, rank)
	return renderMap(ranked, budgetTokens)
}

// readRepoFiles walks a directory and returns {relPath: source} for source
// files we know how to scan. Used by main.go; kept here so RepoMap's input
// contract (a path->source map) is visible alongside it.
func readRepoFiles(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go", ".py", ".js", ".ts", ".java", ".rb":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	return out, err
}
