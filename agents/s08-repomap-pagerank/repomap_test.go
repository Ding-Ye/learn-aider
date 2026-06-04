package main

// repomap_test.go — tests for the RepoMap pipeline. No network: everything runs
// over small in-memory repos so the graph, PageRank, and budget are fully
// deterministic and the assertions are exact.

import (
	"strings"
	"testing"
)

// hasDef reports whether tags contains a definition Tag for name.
func hasDef(tags []Tag, name string) bool {
	for _, t := range tags {
		if t.Kind == "def" && t.Name == name {
			return true
		}
	}
	return false
}

// countRef returns how many "ref" Tags name name.
func countRef(tags []Tag, name string) int {
	n := 0
	for _, t := range tags {
		if t.Kind == "ref" && t.Name == name {
			n++
		}
	}
	return n
}

// Test 1: extractTags finds definitions (func/type) with the right kind+line.
func TestExtractTagsFindsDefs(t *testing.T) {
	src := "package main\n" + // line 1
		"\n" + // line 2
		"type Widget struct{}\n" + // line 3
		"\n" + // line 4
		"func Build() Widget { return Widget{} }\n" // line 5
	tags := extractTags(src, "a.go", "/abs/a.go")

	if !hasDef(tags, "Widget") {
		t.Fatalf("expected a def for Widget; got %v", tags)
	}
	if !hasDef(tags, "Build") {
		t.Fatalf("expected a def for Build; got %v", tags)
	}
	// The Widget def must be on line 3 with the right paths.
	var wd *Tag
	for i := range tags {
		if tags[i].Kind == "def" && tags[i].Name == "Widget" {
			wd = &tags[i]
			break
		}
	}
	if wd == nil || wd.Line != 3 || wd.RelFname != "a.go" || wd.Fname != "/abs/a.go" {
		t.Fatalf("Widget def tag wrong: %+v", wd)
	}
	// "package" is a keyword and must NOT be a def or a ref.
	if hasDef(tags, "package") || countRef(tags, "package") != 0 {
		t.Fatalf("keyword 'package' leaked into tags: %v", tags)
	}
}

// Test 2: extractTags finds references and counts repeats; the defined name on a
// line is not also counted as a ref of itself.
func TestExtractTagsFindsRefs(t *testing.T) {
	src := "func Use() {\n" + // line 1: def Use
		"\thelper()\n" + // line 2: ref helper
		"\thelper()\n" + // line 3: ref helper again
		"}\n"
	tags := extractTags(src, "b.go", "/abs/b.go")

	if got := countRef(tags, "helper"); got != 2 {
		t.Fatalf("expected helper referenced 2x, got %d (%v)", got, tags)
	}
	// Use is a def; it should not be double-counted as a ref on its own line.
	if got := countRef(tags, "Use"); got != 0 {
		t.Fatalf("def name Use should not be a ref, got %d", got)
	}
}

// Test 3: PageRank ranks a symbol referenced by MANY files above an
// unreferenced one. core.go defines Core, used by three other files; lonely.go
// defines Lonely, used by nobody. Core's file must outrank Lonely's.
func TestPageRankRanksReferencedByManyHigher(t *testing.T) {
	files := map[string]string{
		"core.go":   "package p\nfunc Core() {}\n",
		"lonely.go": "package p\nfunc Lonely() {}\n",
		"u1.go":     "package p\nfunc A() { Core() }\n",
		"u2.go":     "package p\nfunc B() { Core() }\n",
		"u3.go":     "package p\nfunc C() { Core() }\n",
	}
	var tags []Tag
	for p, src := range files {
		tags = append(tags, extractTags(src, p, p)...)
	}
	g := buildGraph(tags, nil)
	rank := pageRank(g)

	if rank["core.go"] <= rank["lonely.go"] {
		t.Fatalf("core.go (referenced by 3) should outrank lonely.go (referenced by 0): core=%.4f lonely=%.4f",
			rank["core.go"], rank["lonely.go"])
	}

	// And the rendered map must list Core before Lonely.
	out := RepoMap(files, 0, nil)
	ci := strings.Index(out, "Core")
	li := strings.Index(out, "Lonely")
	if ci == -1 || li == -1 || ci > li {
		t.Fatalf("expected Core to appear before Lonely in map:\n%s", out)
	}
}

// Test 4: a token budget truncates the output below the cap. A generous map is
// strictly larger than a tightly-budgeted one, and the small one stays under
// budget while still being non-empty.
func TestBudgetTruncatesOutput(t *testing.T) {
	files := map[string]string{
		"core.go": "package p\nfunc Core() {}\n",
		"a.go":    "package p\nfunc Alpha() { Core() }\nfunc Beta() {}\nfunc Gamma() {}\n",
		"b.go":    "package p\nfunc Delta() { Core() }\nfunc Epsilon() {}\nfunc Zeta() {}\n",
	}
	full := RepoMap(files, 0, nil)   // 0 = unlimited
	small := RepoMap(files, 12, nil) // tight budget

	if len(small) >= len(full) {
		t.Fatalf("budgeted map should be smaller: small=%d full=%d", len(small), len(full))
	}
	if approxTokens(small) > 12 {
		// The budget is a soft cap: we stop before EXCEEDING it, so the result
		// must not blow well past the cap. Allow the one-entry minimum.
		t.Fatalf("budgeted map exceeded cap: ~%d tokens > 12\n%s", approxTokens(small), small)
	}
	if strings.TrimSpace(small) == "" {
		t.Fatalf("budgeted map should still emit at least one (highest-ranked) symbol")
	}
	// The single most central symbol (Core) should survive the tight budget.
	if !strings.Contains(small, "Core") {
		t.Fatalf("tight budget should keep the top-ranked symbol Core:\n%s", small)
	}
}

// Test 5: output is deterministic — same input yields byte-identical output
// across runs (map iteration order must not leak into the result).
func TestDeterministicOrdering(t *testing.T) {
	files := map[string]string{
		"z.go": "package p\nfunc Zed() { Mid() }\n",
		"m.go": "package p\nfunc Mid() { Base() }\n",
		"a.go": "package p\nfunc Base() {}\n",
	}
	first := RepoMap(files, 0, nil)
	for i := 0; i < 20; i++ {
		if got := RepoMap(files, 0, nil); got != first {
			t.Fatalf("RepoMap not deterministic on run %d:\n--- first ---\n%s\n--- got ---\n%s", i, first, got)
		}
	}
}

// Test 6: personalization (files "in the chat") boosts the dependencies of the
// chat file. Boosting a referencer should raise the rank of what it points at.
func TestPersonalizationBoostsChatDeps(t *testing.T) {
	files := map[string]string{
		"libA.go": "package p\nfunc AThing() {}\n",
		"libB.go": "package p\nfunc BThing() {}\n",
		"useA.go": "package p\nfunc UA() { AThing() }\n",
		"useB.go": "package p\nfunc UB() { BThing() }\n",
	}
	var tags []Tag
	for p, src := range files {
		tags = append(tags, extractTags(src, p, p)...)
	}

	base := pageRank(buildGraph(tags, nil))
	// Put useA.go "in the chat" — its dependency (libA.go) should gain rank.
	boosted := pageRank(buildGraph(tags, map[string]bool{"useA.go": true}))

	if !(boosted["libA.go"] > base["libA.go"]) {
		t.Fatalf("personalizing useA.go should raise libA.go's rank: base=%.4f boosted=%.4f",
			base["libA.go"], boosted["libA.go"])
	}
}

// Test 7: an empty repo produces an empty map (no panic, no stray output).
func TestEmptyRepo(t *testing.T) {
	if got := RepoMap(map[string]string{}, 100, nil); got != "" {
		t.Fatalf("empty repo should yield empty map, got %q", got)
	}
	// A repo with files but no cross-file references still renders defs (each
	// file's symbols), and must not panic.
	out := RepoMap(map[string]string{"solo.go": "package p\nfunc Solo() {}\n"}, 100, nil)
	if !strings.Contains(out, "Solo") {
		t.Fatalf("single-file repo should still list its def: %q", out)
	}
}
