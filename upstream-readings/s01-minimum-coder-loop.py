# Source: aider/coders/wholefile_coder.py  (lines 22-128, annotated + simplified)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/wholefile_coder.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). This is a verbatim
# excerpt reproduced under Apache-2.0 for study; only comments/whitespace were
# trimmed for readability. The original file is 144 lines.
#
# WHY THIS EXCERPT FOR s01
# ------------------------
# s01's Coder.Run -> extractCodeBlock -> applyWholeFile is a one-block, one-file
# collapse of exactly this code. `WholeFileCoder` is aider's SIMPLEST edit
# format ("whole"): the model replies with fenced code blocks, and each block's
# contents become the entire new file. Reading this shows you precisely what s01
# left out: multi-block parsing, filename extraction from the line above a
# fence, and the priority rules for *which* filename wins.


class WholeFileCoder(Coder):
    """A coder that operates on entire files for code modifications."""

    edit_format = "whole"          # <- this string selects prompt + parser upstream
    # gpt_prompts = WholeFilePrompts()   (the per-format prompt; s05 in our curriculum)

    def get_edits(self, mode="update"):
        # The raw LLM reply text. In s01 this is firstText(resp.Content).
        content = self.get_multi_response_content_in_progress()

        # Files the user put "in chat" (editable). s01 has exactly one target
        # path passed on the CLI, so we never need this set.
        chat_files = self.get_inchat_relative_files()

        lines = content.splitlines(keepends=True)
        edits = []

        # --- the state machine ---
        # It walks the reply line by line. `fname is None` => "outside a block",
        # `fname is not None` => "inside a block, accumulating new_lines".
        saw_fname = None
        fname = None
        fname_source = None
        new_lines = []
        for i, line in enumerate(lines):
            # self.fence is ("```", "```") by default — a fence line toggles state.
            if line.startswith(self.fence[0]) or line.startswith(self.fence[1]):
                if fname is not None:
                    # We were INSIDE a block and just hit the closing fence:
                    # emit (filename, source, body) and reset. s01 does the same
                    # "stop at the closing fence" in extractCodeBlock, but only
                    # for the first block and without a filename.
                    edits.append((fname, fname_source, new_lines))
                    fname = None
                    fname_source = None
                    new_lines = []
                    continue

                # We were OUTSIDE a block and hit an OPENING fence. The filename
                # is on the PREVIOUS line. s01 skips all of this — it already
                # knows the path from argv, so it never parses a filename.
                if i > 0:
                    fname_source = "block"
                    fname = lines[i - 1].strip()
                    fname = fname.strip("*")   # **filename.py**
                    fname = fname.rstrip(":")  # filename.py:
                    fname = fname.strip("`")   # `filename.py`
                    fname = fname.lstrip("#")  # # filename.py  (markdown heading)
                    fname = fname.strip()

                    if len(fname) > 250:       # Issue #1232: absurd "filename" -> ignore
                        fname = ""

                    # The model loves to echo the "path/to/" prefix from the
                    # one-shot example in the prompt. Collapse to basename if the
                    # basename is a real chat file.
                    if fname and fname not in chat_files and Path(fname).name in chat_files:
                        fname = Path(fname).name

                if not fname:  # bare ``` with no usable filename above it
                    if saw_fname:                       # a filename mentioned earlier in prose
                        fname, fname_source = saw_fname, "saw"
                    elif len(chat_files) == 1:          # only one editable file -> must be it
                        fname, fname_source = chat_files[0], "chat"
                    else:
                        raise ValueError(
                            f"No filename provided before {self.fence[0]} in file listing"
                        )

            elif fname is not None:
                # Inside a block: this line is part of the new file body.
                new_lines.append(line)
            # (else: prose outside any block; upstream also scans it for
            #  backtick-quoted filenames to set saw_fname — omitted here.)

        if fname:  # reply ended without a closing fence -> still emit the block
            edits.append((fname, fname_source, new_lines))

        # De-dupe by filename, preferring the most reliable source:
        #   "block" (name above fence) > "saw" (mentioned in prose) > "chat" (only file).
        # s01 has no such ambiguity — one block, one known path.
        seen = set()
        refined_edits = []
        for source in ("block", "saw", "chat"):
            for fname, fname_source, new_lines in edits:
                if fname_source != source or fname in seen:
                    continue
                seen.add(fname)
                refined_edits.append((fname, fname_source, new_lines))
        return refined_edits

    def apply_edits(self, edits):
        # The actual write. Note how trivial it is: join the lines, write the
        # file. ALL the intelligence is in get_edits (the parser). This is the
        # whole point of the "whole-file" format and why s01 starts here:
        # writing is a no-op once you've parsed the block.
        for path, fname_source, new_lines in edits:
            full_path = self.abs_root_path(path)
            new_lines = "".join(new_lines)
            self.io.write_text(full_path, new_lines)  # s01: os.WriteFile(path, body)


# READING MAP
# -----------
# get_edits (above) is called from base_coder.py:apply_updates (L2296), which is
# itself called from send_message (L1419) inside run_one (L924). That chain --
# run -> run_one -> send_message -> apply_updates -> get_edits/apply_edits -- is
# the loop s01 collapses into a single Coder.Run method. Follow it next in
# aider/coders/base_coder.py to see where reflection, token budgeting, and git
# auto-commit hook in (our s07/s09 chapters). To see the format get harder,
# read aider/coders/editblock_coder.py (SEARCH/REPLACE = our s03).
