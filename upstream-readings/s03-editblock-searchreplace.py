# Source: aider/coders/editblock_coder.py  (lines 439-535, annotated + simplified)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/editblock_coder.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). This is a verbatim
# excerpt reproduced under Apache-2.0 for study; only comments/whitespace were
# trimmed for readability. The original file is 657 lines.
#
# WHY THIS EXCERPT FOR s03
# ------------------------
# This is the PARSER half of aider's SEARCH/REPLACE ("diff") edit format — the
# heart of aider. s03's Coder.GetEdits is a near-direct Go port of this
# generator. It walks the LLM reply line by line; when it sees a `<<<<<<< SEARCH`
# marker it pulls the filename from the lines just above, reads the SEARCH body
# up to `=======`, reads the REPLACE body up to `>>>>>>> REPLACE`, and yields a
# (filename, original, updated) triple. The APPLY half (replace_most_similar_chunk
# + the whitespace-tolerant matcher) lives at L134-188 / L243-293 — read it next;
# s03's editblock.go ports those too.


# The fence regexes (L386-388). They tolerate 5-9 angle brackets / equals so a
# model that emits a slightly-off fence still parses. s03 matches leniently by
# trimming + prefix-checking instead of a regex.
HEAD = r"^<{5,9} SEARCH>?\s*$"
DIVIDER = r"^={5,9}\s*$"
UPDATED = r"^>{5,9} REPLACE\s*$"


def find_original_update_blocks(content, fence=DEFAULT_FENCE, valid_fnames=None):
    lines = content.splitlines(keepends=True)
    i = 0
    current_filename = None  # sticky: a 2nd block can omit the filename

    head_pattern = re.compile(HEAD)
    divider_pattern = re.compile(DIVIDER)
    updated_pattern = re.compile(UPDATED)

    while i < len(lines):
        line = lines[i]

        # (Upstream also special-cases ```bash shell blocks here — omitted; s03
        #  doesn't run shell commands.)

        # A SEARCH marker starts a block.
        if head_pattern.match(line.strip()):
            try:
                # The filename is on one of the up-to-3 lines ABOVE the marker.
                # If the line right after HEAD is already the DIVIDER, the SEARCH
                # body is empty => "create a new file", so don't require a known
                # name (valid_fnames=None). s03's findFilename mirrors this scan.
                if i + 1 < len(lines) and divider_pattern.match(lines[i + 1].strip()):
                    filename = find_filename(lines[max(0, i - 3) : i], fence, None)
                else:
                    filename = find_filename(lines[max(0, i - 3) : i], fence, valid_fnames)

                if not filename:
                    if current_filename:
                        filename = current_filename       # inherit the sticky name
                    else:
                        raise ValueError(missing_filename_err.format(fence=fence))
                current_filename = filename

                # SEARCH body: accumulate until the divider.
                original_text = []
                i += 1
                while i < len(lines) and not divider_pattern.match(lines[i].strip()):
                    original_text.append(lines[i])
                    i += 1
                if i >= len(lines) or not divider_pattern.match(lines[i].strip()):
                    raise ValueError(f"Expected `{DIVIDER_ERR}`")   # malformed -> hard error

                # REPLACE body: accumulate until the replace marker (or a divider,
                # which upstream also accepts as a separator between stacked blocks).
                updated_text = []
                i += 1
                while i < len(lines) and not (
                    updated_pattern.match(lines[i].strip())
                    or divider_pattern.match(lines[i].strip())
                ):
                    updated_text.append(lines[i])
                    i += 1
                if i >= len(lines) or not (
                    updated_pattern.match(lines[i].strip())
                    or divider_pattern.match(lines[i].strip())
                ):
                    raise ValueError(f"Expected `{UPDATED_ERR}` or `{DIVIDER_ERR}`")

                # One parsed edit. s03 returns Edit{Path, Search, Replace}.
                yield filename, "".join(original_text), "".join(updated_text)

            except ValueError as e:
                # Re-raise with the processed prefix so the model sees WHERE it
                # went wrong. s03 returns a plain error instead.
                processed = "".join(lines[: i + 1])
                raise ValueError(f"{processed}\n^^^ {e.args[0]}")

        i += 1


# READING MAP
# -----------
# 1. PARSE (above): find_original_update_blocks (L439-560) -> s03 Coder.GetEdits.
#    Helpers strip_filename (L408) + find_filename (L538) -> s03 stripFilename +
#    findFilename. s03 drops the fuzzy difflib.get_close_matches on valid_fnames.
#
# 2. APPLY: do_replace (L364-383) -> s03 applyOne. It routes empty-SEARCH to
#    create/append, otherwise calls:
#      replace_most_similar_chunk (L157-188)  -> s03 replaceMostSimilarChunk
#        perfect_or_whitespace    (L134-144)  -> s03 perfectOrWhitespace
#          perfect_replace        (L146-154)  -> s03 perfectReplace
#          replace_part_with_missing_leading_whitespace (L243-273)
#                                              -> s03 replacePartWith...Whitespace
#            match_but_for_leading_whitespace (L276-293) -> s03 matchButFor...
#        try_dotdotdots           (L190-240)  -> s03 tryDotDotDots
#    NOTE: replace_most_similar_chunk `return`s at L183 BEFORE the edit-distance
#    tier (replace_closest_edit_distance, L296) — that tier is dead code upstream,
#    and s03 likewise stops at the dotdotdots tier. The real fuzziness that does
#    the work is the whitespace-tolerant tier.
#
# 3. ORCHESTRATION: EditBlockCoder.get_edits/apply_edits (L21-124) wrap the above
#    and collect failures into a "SEARCH/REPLACE block failed to match" message.
#    That message becomes a reflected_message (our s09). To see the format get
#    even more compact, read aider/coders/udiff_coder.py (unified diff = our s04).
