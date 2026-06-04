# Source: aider/coders/wholefile_coder.py  (lines 22-128, annotated + simplified)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/wholefile_coder.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). This is a verbatim
# excerpt reproduced under Apache-2.0 for study; only comments/whitespace were
# trimmed for readability. The original file is 144 lines.
#
# WHY THIS EXCERPT FOR s02
# ------------------------
# s02 implements aider's "whole" edit format as a reusable WholeFileCoder. s01
# already collapsed the ONE-block, KNOWN-path case; what s02 (and this excerpt)
# adds is the part s01 skipped: parse MANY fenced blocks from one reply, recover
# the filename from the line ABOVE each fence, clean the markdown the model
# wraps it in, and resolve which filename "wins" when sources disagree. The whole
# state machine below is exactly what wholefile.go:GetEdits mirrors line for line.


class WholeFileCoder(Coder):
    """A coder that operates on entire files for code modifications."""

    edit_format = "whole"          # <- selects the prompt + this parser upstream
    # gpt_prompts = WholeFilePrompts()   (the per-format prompt; s05 in our curriculum)

    def get_edits(self, mode="update"):
        content = self.get_multi_response_content_in_progress()   # the raw LLM reply
        chat_files = self.get_inchat_relative_files()             # editable files (drives fallbacks)

        lines = content.splitlines(keepends=True)
        edits = []

        # --- the state machine ---
        # `fname is None`  => OUTSIDE a block.
        # `fname is not None` => INSIDE a block, accumulating new_lines (the file body).
        # A fence line toggles between the two states.
        saw_fname = None          # a filename mentioned in prose, e.g. update `foo.py`
        fname = None
        fname_source = None
        new_lines = []
        for i, line in enumerate(lines):
            if line.startswith(self.fence[0]) or line.startswith(self.fence[1]):
                if fname is not None:
                    # CLOSING fence: emit (filename, source, body) and reset. s01
                    # stopped at the first closing fence; here we keep looping, so
                    # a second "fname + fence" later yields a second file.
                    edits.append((fname, fname_source, new_lines))
                    fname = None
                    fname_source = None
                    new_lines = []
                    continue

                # OPENING fence: the filename is on the PREVIOUS line. This whole
                # block is what s01 had no notion of (it knew the path from argv).
                if i > 0:
                    fname_source = "block"
                    fname = lines[i - 1].strip()
                    fname = fname.strip("*")   # **filename.py**
                    fname = fname.rstrip(":")  # filename.py:
                    fname = fname.strip("`")   # `filename.py`
                    fname = fname.lstrip("#")  # # filename.py  (markdown heading)
                    fname = fname.strip()

                    if len(fname) > 250:       # Issue #1232: absurd "name" -> ignore
                        fname = ""

                    # The model loves to echo the "path/to/" prefix from the
                    # one-shot example in the prompt. If the full name isn't a
                    # chat file but its basename is, collapse to the basename.
                    if fname and fname not in chat_files and Path(fname).name in chat_files:
                        fname = Path(fname).name

                if not fname:  # bare ``` with no usable filename above it
                    if saw_fname:                       # mentioned earlier in prose
                        fname = saw_fname
                        fname_source = "saw"
                    elif len(chat_files) == 1:          # only one editable file -> must be it
                        fname = chat_files[0]
                        fname_source = "chat"
                    else:
                        # genuinely ambiguous: many files, no name. We surface the
                        # same error in Go rather than guess.
                        raise ValueError(
                            f"No filename provided before {self.fence[0]} in file listing"
                        )

            elif fname is not None:
                new_lines.append(line)   # inside a block: part of the new file body
            else:
                # prose outside any block: scan it for a backtick-quoted chat file
                # so a LATER bare fence can fall back to it (sets saw_fname).
                for word in line.strip().split():
                    word = word.rstrip(".:,;!")
                    for chat_file in chat_files:
                        if word == f"`{chat_file}`":
                            saw_fname = chat_file

        if fname:  # reply ended WITHOUT a closing fence -> still emit the block
            edits.append((fname, fname_source, new_lines))

        # De-dupe by filename, preferring the most reliable source:
        #   "block" (name above fence) > "saw" (prose mention) > "chat" (sole file).
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
        # The actual write. Note how trivial it is: resolve path, join lines,
        # write. ALL the intelligence is in get_edits (the parser). This is the
        # whole point of "whole-file": applying an edit is a no-op once parsed.
        for path, fname_source, new_lines in edits:
            full_path = self.abs_root_path(path)
            new_lines = "".join(new_lines)
            self.io.write_text(full_path, new_lines)   # Go: os.WriteFile(full, body, 0o644)


# READING MAP
# -----------
# get_edits (above) is called from base_coder.py:apply_updates (L2296), itself
# called from send_message (L1419) inside run_one (L924). That run -> run_one ->
# send_message -> apply_updates -> get_edits/apply_edits chain is the loop our
# s01 collapsed and our s02 keeps (now multi-file). To see the format get
# HARDER, read aider/coders/editblock_coder.py:find_original_update_blocks
# (L439-560) -- SEARCH/REPLACE, our s03 -- where Edit.Search finally becomes
# non-empty and "apply" stops being a plain write.
