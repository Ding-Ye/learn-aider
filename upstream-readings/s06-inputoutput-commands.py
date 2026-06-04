# Source: aider/commands.py (is_command L255-256, do_run L287-299, run L312-332,
#          cmd_add L799-, cmd_drop L912-, cmd_ls L1064-, excerpted)
#          + aider/io.py (confirm_ask L807-, the --yes fast-path L866-869)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/commands.py
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/io.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). Verbatim excerpts
# reproduced under Apache-2.0 for study; only comments/whitespace/bodies were
# trimmed. commands.py is 1712 lines; io.py is 1191 lines.
#
# WHY THIS EXCERPT FOR s06
# ------------------------
# This is the user-facing SHELL that wraps the coder loop. Two collaborating
# pieces, both stripped to their load-bearing core:
#
#   COMMANDS (commands.py): a registry of slash-command handlers. The dispatch is
#   the whole mechanism — is_command decides "is this a command?", run/do_run
#   look up the handler by NAME and call it. Aider finds handlers by REFLECTION
#   (any method named cmd_<x> auto-registers); s06 uses an explicit map, same
#   idea. Handlers mutate the Coder's file sets (abs_fnames) — i.e. the EDIT
#   SCOPE — never the network.
#
#   IO (io.py): the InputOutput contract. confirm_ask is the piece s06 leans on
#   most: note the --yes fast-path (self.yes) that returns without reading input.
#   That single branch is what makes the loop testable and scriptable.


# ===== aider/commands.py — the dispatch core =====

class Commands:
    def is_command(self, inp):
        # A line is a command iff its first char is "/" or "!". That's it.
        # s06's IsCommand is a direct port (minus the "!" shell layer, which we
        # report as unknown rather than execute).
        return inp[0] in "/!"

    def do_run(self, cmd_name, args):
        # Look up the handler by NAME: cmd_<name>. getattr is the reflection that
        # turns "add" into self.cmd_add. s06 replaces this with a map lookup.
        cmd_name = cmd_name.replace("-", "_")
        cmd_method_name = f"cmd_{cmd_name}"
        cmd_method = getattr(self, cmd_method_name, None)
        if not cmd_method:
            # Unknown command → a message, not a crash. s06 does the same and
            # still treats it as "handled" so a typo isn't sent to the model.
            self.io.tool_output(f"Error: Command {cmd_name} not found.")
            return
        try:
            return cmd_method(args)
        except ANY_GIT_ERROR as err:
            self.io.tool_error(f"Unable to complete {cmd_name}: {err}")

    def run(self, inp):
        # "!" is the shell-run alias; s06 omits the shell layer entirely.
        if inp.startswith("!"):
            return self.do_run("run", inp[1:])

        # Split the first word (the command) from the rest (its args), then find
        # matching commands by PREFIX so "/dr" can resolve to "/drop". s06 keeps
        # the split but requires an exact command word (no prefix matching) for
        # simplicity — the dispatch shape is what matters.
        res = self.matching_commands(inp)
        if res is None:
            return
        matching_commands, first_word, rest_inp = res
        if len(matching_commands) == 1:
            command = matching_commands[0][1:]
            return self.do_run(command, rest_inp)
        elif first_word in matching_commands:
            command = first_word[1:]
            return self.do_run(command, rest_inp)
        elif len(matching_commands) > 1:
            self.io.tool_error(f"Ambiguous command: {', '.join(matching_commands)}")
        else:
            self.io.tool_error(f"Invalid command: {first_word}")

    # any method called cmd_xxx becomes a command automatically.
    # each one must take an args param.  <-- this comment IS the registry contract

    def cmd_add(self, args):
        "Add files to the chat so aider can edit them or review them in detail"
        # (Body heavily trimmed.) The load-bearing effect is the last lines: a
        # matched file is put into self.coder.abs_fnames — i.e. the edit scope —
        # and confirm_ask gates creating a file that doesn't exist yet. s06's
        # cmdAdd mirrors exactly this: validate, optionally ConfirmAsk to create,
        # then InChat[f] = true.
        for matched_file in sorted(all_matched_files):  # all_matched_files: see upstream L802-845
            abs_file_path = self.coder.abs_root_path(matched_file)
            if abs_file_path in self.coder.abs_fnames:
                self.io.tool_error(f"{matched_file} is already in the chat as an editable file")
                continue
            # ... (read-only promotion, image/vision checks omitted) ...
            self.coder.abs_fnames.add(abs_file_path)        # <-- the mutation
            self.io.tool_output(f"Added {matched_file} to the chat")

    def cmd_drop(self, args=""):
        "Remove files from the chat session to free up context space"
        if not args.strip():
            self._drop_all_files()                          # no args → drop all
            return
        # (Glob/substring matching trimmed.) The effect: remove from abs_fnames.
        for matched_file in matched_files:                  # matched_files: see upstream L949-960
            abs_fname = self.coder.abs_root_path(matched_file)
            if abs_fname in self.coder.abs_fnames:
                self.coder.abs_fnames.remove(abs_fname)     # <-- the mutation
                self.io.tool_output(f"Removed {matched_file} from the chat")

    def cmd_ls(self, args):
        "List all known files and indicate which are included in the chat session"
        files = self.coder.get_all_relative_files()
        other_files = []
        chat_files = []
        for file in files:
            abs_file_path = self.coder.abs_root_path(file)
            if abs_file_path in self.coder.abs_fnames:
                chat_files.append(file)                      # in the edit scope
            else:
                other_files.append(file)                    # known but not in chat
        # ... (read-only list omitted) ...  s06 prints the same two buckets.


# ===== aider/io.py — confirm_ask, the testability knob =====

class InputOutput:
    def confirm_ask(self, question, default="y", subject=None,
                    explicit_yes_required=False, group=None, allow_never=False):
        # The --yes / --yes-always fast-path: when self.yes is set we NEVER read
        # input, we just answer. This one branch is what makes the loop
        # scriptable and what s06's IO.ConfirmAsk + ScriptIO.Confirm reproduce.
        if self.yes is True:
            res = "n" if explicit_yes_required else "y"
        elif self.yes is False:
            res = "n"
        else:
            # interactive path: prompt_toolkit / input() loop, validation,
            # never-ask-again handling — all omitted here. (upstream L873-898)
            res = input(question)
        # ... normalize res, append to chat history, return is_yes ...


# READING MAP
# -----------
# 1. THE BOUNDARY (commands.py is_command + run): "is this a command?" then
#    "dispatch by name". -> s06 IsCommand + ParseAndRun. The crucial design point
#    is that a command is intercepted and NEVER becomes an LLM message; only the
#    plain-text path flows to the coder. Upstream calls run() from
#    base_coder.py's preproc_user_input (the loop's front door); s06 inlines that
#    same check in runREPL.
#
# 2. THE REGISTRY (commands.py: every cmd_* method): handlers are discovered by
#    reflection (getattr(self, "cmd_"+name)). -> s06's explicit
#    map[string]handler. Same "add a method/entry = add a command" ergonomics,
#    minus the magic. To enumerate commands, upstream get_commands() scans dir(self)
#    for cmd_* (L276-285); s06 just ranges over the map.
#
# 3. THE STATE (cmd_add/cmd_drop/cmd_ls mutate abs_fnames): the in-chat file set
#    is the EDIT SCOPE. -> s06 Session.InChat + the three handlers. This is the
#    bridge to the rest of aider: those files become the file-content messages the
#    coder sends (base_coder.py get_files_messages), so this chapter is where the
#    scope the model sees becomes dynamic and user-controlled.
#
# 4. THE I/O CONTRACT (io.py confirm_ask + tool_output/tool_error/get_input): the
#    coder talks to the user only through this object. -> s06 Console interface +
#    IO/ScriptIO. The --yes branch (above) is the load-bearing line for testing.
#
# To see how a typed command reaches the model (or doesn't), follow base_coder.py
# run() -> preproc_user_input() -> commands.run(); for how the file set turns into
# prompt content, follow base_coder.py get_files_messages(). That trace is the
# real-source map for s06 -> s07 (git) -> the message-assembly chapters.
