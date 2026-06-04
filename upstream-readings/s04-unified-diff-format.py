# Source: aider/coders/udiff_coder.py  (lines 312-429, annotated + simplified)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/udiff_coder.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). This is a verbatim
# excerpt reproduced under Apache-2.0 for study; only comments were added and a
# few edge branches trimmed for readability. The original file is 429 lines.
#
# WHY THIS EXCERPT FOR s04
# ------------------------
# This is the PARSER + DERIVE half of aider's unified-diff ("udiff") edit format.
# s04's findDiffs / processFencedBlock / hunkToBeforeAfter are near-direct Go
# ports of these three functions. The flow:
#   find_diffs            -> scan the reply for ```diff fences            (L312)
#   process_fenced_block  -> split one fence into (path, hunk) pairs      (L337)
#   hunk_to_before_after  -> turn a hunk's ' '/'-'/'+' lines into         (L403)
#                            (before_text, after_text)
# Once you have (before, after), a hunk reduces to the s03 search/replace problem:
# locate `before` in the file, splice `after`. The APPLY half (do_replace /
# apply_hunk, L121-199) does exactly that and REUSES the search/replace engine —
# read it next; s04's udiff.go reuses the s03 matcher the same way.


def find_diffs(content):
    # We can always fence with triple-backticks, because all the udiff content
    # is prefixed with +/-/space, so the fence is unambiguous. s04: findDiffs.
    if not content.endswith("\n"):
        content = content + "\n"

    lines = content.splitlines(keepends=True)
    line_num = 0
    edits = []
    while line_num < len(lines):
        while line_num < len(lines):
            line = lines[line_num]
            if line.startswith("```diff"):                      # a diff fence opens
                line_num, these_edits = process_fenced_block(lines, line_num + 1)
                edits += these_edits
                break
            line_num += 1

    # NOTE: the "just take 1" line is commented out upstream — ALL hunks are kept.
    return edits


def process_fenced_block(lines, start_line_num):
    # Find the closing ``` fence.
    for line_num in range(start_line_num, len(lines)):
        line = lines[line_num]
        if line.startswith("```"):
            break

    block = lines[start_line_num:line_num]
    block.append("@@ @@")                  # sentinel so the LAST hunk gets flushed

    # A leading `--- a/x` / `+++ b/x` pair names the file. Strip git's a//b/ (or
    # /dev/null) prefixes to recover the real path. s04: same prefix handling.
    if block[0].startswith("--- ") and block[1].startswith("+++ "):
        a_fname = block[0][4:].strip()
        b_fname = block[1][4:].strip()
        if (a_fname.startswith("a/") or a_fname == "/dev/null") and b_fname.startswith("b/"):
            fname = b_fname[2:]
        else:
            fname = b_fname            # path is as intended (no git prefixes)
        block = block[2:]
    else:
        fname = None                   # sticky filename filled in by get_edits

    edits = []
    keeper = False                     # does the current hunk contain a -/+ change?
    hunk = []
    op = " "
    for line in block:
        hunk.append(line)
        if len(line) < 2:
            continue

        # A fresh `--- / +++` header mid-block: flush the current hunk, switch file.
        if line.startswith("+++ ") and hunk[-2].startswith("--- "):
            if hunk[-3] == "\n":
                hunk = hunk[:-3]
            else:
                hunk = hunk[:-2]
            edits.append((fname, hunk))
            hunk = []
            keeper = False
            fname = line[4:].strip()
            continue

        op = line[0]
        if op in "-+":
            keeper = True              # this hunk actually changes something
            continue
        if op != "@":
            continue                   # a normal context line — accumulate
        if not keeper:
            hunk = []                  # `@@` but no change yet: drop pure context
            continue

        hunk = hunk[:-1]               # drop the `@@` line itself, flush the hunk
        edits.append((fname, hunk))
        hunk = []
        keeper = False

    return line_num + 1, edits


def hunk_to_before_after(hunk, lines=False):
    # Split a hunk's diff lines into the BEFORE text (context + removed lines =
    # what the file currently holds) and the AFTER text (context + added lines =
    # what it should hold). THIS is the conversion that turns a unified-diff hunk
    # into the s03 search/replace problem. s04: hunkToBeforeAfter.
    before = []
    after = []
    op = " "
    for line in hunk:
        if len(line) < 2:
            op = " "
            line = line
        else:
            op = line[0]
            line = line[1:]

        if op == " ":          # context: belongs to BOTH sides (the anchor)
            before.append(line)
            after.append(line)
        elif op == "-":        # removed: BEFORE only
            before.append(line)
        elif op == "+":        # added: AFTER only
            after.append(line)

    if lines:
        return before, after

    before = "".join(before)
    after = "".join(after)
    return before, after


# READING MAP
# -----------
# 1. PARSE (above): find_diffs (L312-334) -> s04 findDiffs;
#    process_fenced_block (L337-400) -> s04 processFencedBlock. The sentinel
#    "@@ @@" trick and the git a//b/ prefix stripping are ported verbatim.
#
# 2. DERIVE (above): hunk_to_before_after (L403-429) -> s04 hunkToBeforeAfter.
#    Context lines go to both sides, '-' to before, '+' to after.
#
# 3. APPLY: do_replace (L121-149) -> s04 applyOne. It routes an empty before-text
#    to create/append, otherwise calls apply_hunk (L151-199), which tries
#    directly_apply_hunk (L261-279) -> flexi_just_search_and_replace ->
#    flexible_search_and_replace (search_replace.py). s04 SIMPLIFIES this: instead
#    of upstream's full partial-hunk machinery (apply_partial_hunk L282-309, which
#    progressively drops context lines to retry), s04 reuses the s03 tiered
#    matcher (replaceMostSimilarChunk: exact -> whitespace-flexible) on the whole
#    before/after. Both share the load-bearing idea: locate the hunk by its
#    CONTEXT and ignore the `@@ -a,b +c,d @@` line numbers entirely.
#
# 4. ORCHESTRATION: UnifiedDiffCoder.get_edits/apply_edits (L52-118) wrap the
#    above, make the filename sticky across hunks, dedup, drop pure-context hunks
#    via normalize_hunk (L250-258), and collect misses into a "UnifiedDiffNoMatch"
#    message. That message becomes a reflected_message (our s09). The per-format
#    system prompt lives in udiff_prompts.py (our s05).
