package main

// main.go — CLI for the RepoMap chapter.
//
// Point it at a directory; it scans the source files, builds the
// definition->reference graph, ranks symbols with PageRank, and prints the
// highest-ranked symbols that fit a token budget — the "repo map" that aider
// injects (read-only) into the model's context so it can understand a codebase
// far too large to paste in full.
//
//	go run . -dir .                       # map the current module
//	go run . -dir ../s05-prompt-system    # map another chapter
//	go run . -dir . -tokens 80            # tighter budget -> fewer symbols
//	go run . -dir . -chat repomap.go      # boost files "in the chat"
//	go run . -dir . -v                    # also print the graph + ranks
//
// There is no LLM call here: the repo map is computed entirely locally. The
// connection to the Provider layer is shown by -as-message, which prints the
// exact read-only Message that the loop (s01..s07) would prepend before calling
// the model.

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	var (
		dir       = flag.String("dir", ".", "directory to map")
		tokens    = flag.Int("tokens", 256, "token budget for the map (~chars/4)")
		chat      = flag.String("chat", "", "comma-separated files 'in the chat' to boost (relative paths)")
		verbose   = flag.Bool("v", false, "also print the dependency graph and PageRank scores")
		asMessage = flag.Bool("as-message", false, "print the repo map wrapped as the read-only Message the loop injects")
	)
	flag.Parse()

	files, err := readRepoFiles(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error reading repo:", err)
		os.Exit(1)
	}
	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "[s08] no source files under %s\n", *dir)
		return
	}

	personalize := map[string]bool{}
	for _, f := range strings.Split(*chat, ",") {
		if f = strings.TrimSpace(f); f != "" {
			personalize[f] = true
		}
	}

	fmt.Fprintf(os.Stderr, "[s08] scanned %d files under %s, budget=%d tokens\n",
		len(files), *dir, *tokens)

	if *verbose {
		printGraphAndRanks(files, personalize)
	}

	repoMap := RepoMap(files, *tokens, personalize)

	if *asMessage {
		// This is exactly how s01..s07's loop would inject the map: a read-only
		// user turn, prepended before the real instruction.
		msg := Message{
			Role: "user",
			Content: []ContentBlock{{
				Type: "text",
				Text: "Here are summaries of files in my git repository.\n" +
					"Do not propose changes to these files, treat them as *read-only*.\n\n" +
					repoMap,
			}},
		}
		fmt.Printf("===== READ-ONLY repo-map Message (role=%s) =====\n", msg.Role)
		fmt.Print(msg.Content[0].Text)
		return
	}

	fmt.Printf("===== REPO MAP (budget=%d tokens, ~%d used) =====\n",
		*tokens, approxTokens(repoMap))
	fmt.Print(repoMap)
}

// printGraphAndRanks dumps the intermediate state so a learner can SEE why a
// symbol ranked where it did: the def->ref edges and the resulting PageRank.
func printGraphAndRanks(files map[string]string, personalize map[string]bool) {
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var tags []Tag
	for _, p := range paths {
		tags = append(tags, extractTags(files[p], p, p)...)
	}
	g := buildGraph(tags, personalize)
	rank := pageRank(g)

	fmt.Fprintln(os.Stderr, "[s08] dependency edges (referencer -> definer  weight):")
	for _, u := range g.nodes {
		outs := g.edges[u]
		if len(outs) == 0 {
			continue
		}
		var dsts []string
		for d := range outs {
			dsts = append(dsts, d)
		}
		sort.Strings(dsts)
		for _, d := range dsts {
			fmt.Fprintf(os.Stderr, "    %s -> %s  %.1f\n", u, d, outs[d])
		}
	}

	type nr struct {
		n string
		r float64
	}
	var ranks []nr
	for _, n := range g.nodes {
		ranks = append(ranks, nr{n, rank[n]})
	}
	sort.SliceStable(ranks, func(i, j int) bool {
		if ranks[i].r != ranks[j].r {
			return ranks[i].r > ranks[j].r
		}
		return ranks[i].n < ranks[j].n
	})
	fmt.Fprintln(os.Stderr, "[s08] PageRank (higher = more central):")
	for _, x := range ranks {
		fmt.Fprintf(os.Stderr, "    %.4f  %s\n", x.r, x.n)
	}
}
