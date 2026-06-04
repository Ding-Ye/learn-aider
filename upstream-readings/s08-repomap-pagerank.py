# Source: aider/repomap.py  (get_ranked_tags, lines 365-574; excerpted)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/repomap.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). Verbatim excerpts
# reproduced under Apache-2.0 for study; the scanning/cache/progress plumbing
# and most comments were trimmed to expose the algorithm. repomap.py is 867
# lines; get_ranked_tags spans L365-574.
#
# WHY THIS EXCERPT FOR s08
# ------------------------
# This is the ALGORITHM the whole chapter is about: turn a pile of tags into a
# ranked list of symbols by building a definition->reference graph and running
# PageRank over it. s08's buildGraph + pageRank + rankTags are a direct, stdlib
# port. The two big simplifications s08 makes are flagged inline:
#   (1) we do NOT compute the per-ident weight multipliers (mul) below;
#   (2) we run PageRank ourselves (power iteration) instead of nx.pagerank.


def get_ranked_tags(self, chat_fnames, other_fnames, mentioned_fnames, mentioned_idents, progress=None):
    import networkx as nx

    # defines[ident]      -> set of files that DEFINE ident
    # references[ident]   -> list of files that USE ident (one entry per use)
    # definitions[(f,id)] -> the actual Tag objects, kept to emit later
    defines = defaultdict(set)
    references = defaultdict(list)
    definitions = defaultdict(set)

    personalization = dict()                      # the PageRank teleport vector
    fnames = sorted(set(chat_fnames).union(set(other_fnames)))
    chat_rel_fnames = set()

    # Each file's baseline personalization weight (uniform-ish). Files that are
    # in the chat / mentioned get this added; everyone else gets nothing, so the
    # teleport mass concentrates on the code the user cares about. s08 does the
    # same with PromptVars-style "personal" set -> a teleport vector p.
    personalize = 100 / len(fnames)

    for fname in fnames:
        rel_fname = self.get_rel_fname(fname)
        if fname in chat_fnames:
            personalization[rel_fname] = personalize     # boost chat files
            chat_rel_fnames.add(rel_fname)
        # (mentioned_fnames / mentioned_idents add more boost here; omitted)

        # get_tags() is the tree-sitter extractor (s08 = the regex extractTags).
        for tag in self.get_tags(fname, rel_fname):
            if tag.kind == "def":
                defines[tag.name].add(rel_fname)
                definitions[(rel_fname, tag.name)].add(tag)
            elif tag.kind == "ref":
                references[tag.name].append(rel_fname)

    # If a language's query only yields defs (no refs), treat every def as its
    # own ref so the graph isn't empty. s08 keeps this corner simple.
    if not references:
        references = dict((k, list(v)) for k, v in defines.items())

    # An ident only makes an edge if it is BOTH defined and referenced.
    idents = set(defines.keys()).intersection(set(references.keys()))

    G = nx.MultiDiGraph()                          # the def->ref graph

    for ident in idents:
        definers = defines[ident]

        # --- weight multipliers s08 OMITS (it uses raw counts) -------------
        mul = 1.0
        is_snake = ("_" in ident) and any(c.isalpha() for c in ident)
        is_camel = any(c.isupper() for c in ident) and any(c.islower() for c in ident)
        if ident in mentioned_idents:              # user named this symbol
            mul *= 10
        if (is_snake or is_camel) and len(ident) >= 8:  # "interesting" names
            mul *= 10
        if ident.startswith("_"):                  # private -> downweight
            mul *= 0.1
        if len(defines[ident]) > 5:                # defined everywhere -> noise
            mul *= 0.1
        # -------------------------------------------------------------------

        for referencer, num_refs in Counter(references[ident]).items():
            for definer in definers:
                use_mul = mul
                if referencer in chat_rel_fnames:  # edges FROM chat files
                    use_mul *= 50                  # (s08 uses personalization instead)
                num_refs = math.sqrt(num_refs)     # damp high-frequency idents
                # The edge: referencer DEPENDS ON definer, weighted.
                G.add_edge(referencer, definer, weight=use_mul * num_refs, ident=ident)

    # THE RANKING. With a personalization vector, this is "personalized
    # PageRank": teleport mass goes to chat/mentioned files, so their
    # dependencies inherit rank. s08 reimplements exactly this with ~20 rounds
    # of power iteration and damping 0.85 (nx.pagerank's default alpha).
    if personalization:
        pers_args = dict(personalization=personalization, dangling=personalization)
    else:
        pers_args = dict()
    try:
        ranked = nx.pagerank(G, weight="weight", **pers_args)
    except ZeroDivisionError:
        return []

    # Distribute each file's PageRank across its OUT edges, accumulating a score
    # per (definer, ident). s08 takes a coarser route: it splits a file's rank
    # evenly across the symbols that file defines (rankTags). Same goal: turn a
    # per-FILE rank into a per-SYMBOL rank.
    ranked_definitions = defaultdict(float)
    for src in G.nodes:
        src_rank = ranked[src]
        total_weight = sum(data["weight"] for _s, _d, data in G.out_edges(src, data=True))
        for _s, dst, data in G.out_edges(src, data=True):
            data["rank"] = src_rank * data["weight"] / total_weight
            ranked_definitions[(dst, data["ident"])] += data["rank"]

    # Sort symbols best-first (ties broken on the key for determinism) and emit
    # their Tags — skipping files already in the chat (the model has those).
    ranked_tags = []
    ranked_definitions = sorted(ranked_definitions.items(), reverse=True, key=lambda x: (x[1], x[0]))
    for (fname, ident), rank in ranked_definitions:
        if fname in chat_rel_fnames:
            continue
        ranked_tags += list(definitions.get((fname, ident), []))

    return ranked_tags


# READING MAP
# -----------
# 1. EXTRACT (get_tags, L233 -> get_tags_raw L279): a tree-sitter query yields
#    name.definition.* / name.reference.* captures -> Tag(kind="def"|"ref").
#    -> s08 repomap.go extractTags (regex, not tree-sitter; the fidelity gap).
#
# 2. GRAPH (above, L470-514): one node per file; edge referencer->definer for
#    every shared ident, weighted by sqrt(use count) * heuristics. -> s08
#    buildGraph, weight = raw cross-file reference count (no heuristics).
#
# 3. RANK (above, L519-558): nx.pagerank with a personalization vector, then
#    spread each node's rank over its out edges into per-(file,ident) scores.
#    -> s08 pageRank (hand-rolled power iteration, 20 iters, damping 0.85) +
#    rankTags (split a file's rank evenly across its defs).
#
# 4. BUDGET (get_ranked_tags_map L576 -> L666-706): a binary search over how
#    many top tags to include, re-rendering and token-counting until the result
#    just fits max_map_tokens. -> s08 renderMap, a simpler GREEDY fill that
#    walks symbols best-first and stops before the ~chars/4 estimate exceeds the
#    budget.
#
# To see how the map reaches the model, follow ranked_tags into to_tree (L748)
# and render_tree (L710), then up into base_coder.py get_repo_map: the string
# this function ultimately produces is injected as a read-only message before
# the LLM call — which is the -as-message output in s08's main.go.
