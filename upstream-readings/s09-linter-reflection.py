# Source: aider/linter.py  (Linter L21-116 + LintResult L171-174, excerpted + annotated)
#          + aider/coders/base_coder.py (run_one reflection loop L924-944, shown at the bottom)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/linter.py
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/base_coder.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). Verbatim excerpts
# reproduced under Apache-2.0 for study; only comments/whitespace were trimmed.
# linter.py is 304 lines; base_coder.py is ~2485 lines.
#
# WHY THIS EXCERPT FOR s09
# ------------------------
# Two mechanisms meet here:
#
#   1. The Linter (linter.py): "run a checker over a file, capture its output,
#      label it so the model can read it." Linter.lint is the dispatcher; our
#      Go Linter.Lint/lintOne/runCmd are a direct port of L82-116.
#
#   2. The reflection loop (base_coder.run_one L924-944): after a turn, if a
#      `reflected_message` was produced (lint/test errors live there), make it
#      the next message and loop — bounded by max_reflections. Our Go
#      ReflectLoop.Run is a transcription of that while-loop.
#
# Read them together: the Linter PRODUCES the text, the loop FEEDS it back.


# ===== aider/linter.py — the Linter dispatcher =====

class Linter:
    def __init__(self, encoding="utf-8", root=None):
        self.encoding = encoding
        self.root = root
        # Per-language checker registry. Upstream ships a Python checker; the
        # default for any other language is the tree-sitter basic_lint below.
        # Our Go port maps a file EXTENSION -> shell command instead, and ships
        # `gofmt -l -e` as the built-in.
        self.languages = dict(
            python=self.py_lint,
        )
        self.all_lint_cmd = None  # --lint-cmd with no language: override everything

    def run_cmd(self, cmd, rel_fname, code):
        # Append the file to the command and run it. A ZERO exit code means
        # clean -> return None (no LintResult). This single rule is what lets the
        # reflection loop know "nothing to fix". (Our runCmd mirrors it exactly.)
        cmd += " " + oslex.quote(rel_fname)
        returncode, stdout = run_cmd_subprocess(cmd, cwd=self.root, encoding=self.encoding)
        errors = stdout
        if returncode == 0:
            return  # zero exit status == lint clean

        res = f"## Running: {cmd}\n\n"   # the per-file header we copy verbatim
        res += errors
        return self.errors_to_lint_result(rel_fname, res)

    def lint(self, fname, cmd=None):
        # THE DISPATCHER. Precedence: explicit cmd arg, then all_lint_cmd, then
        # the per-language checker, else the built-in basic_lint. Our lintOne
        # uses the same precedence (allCmd -> per-extension command -> built-in).
        rel_fname = self.get_rel_fname(fname)
        code = Path(fname).read_text(encoding=self.encoding, errors="replace")

        if cmd:
            cmd = cmd.strip()
        if not cmd:
            lang = filename_to_lang(fname)
            if not lang:
                return            # no checker for this language -> skip (clean)
            if self.all_lint_cmd:
                cmd = self.all_lint_cmd
            else:
                cmd = self.languages.get(lang)

        if callable(cmd):
            lintres = cmd(fname, rel_fname, code)   # py_lint path
        elif cmd:
            lintres = self.run_cmd(cmd, rel_fname, code)
        else:
            lintres = basic_lint(rel_fname, code)   # built-in tree-sitter pass

        if not lintres:
            return                                  # clean -> None

        # Wrap the result so the model reads it as a FIX REQUEST, not a new task.
        # This exact header is what our Go Linter.Lint emits.
        res = "# Fix any errors below, if possible.\n\n"
        res += lintres.text
        res += "\n"
        res += tree_context(rel_fname, code, lintres.lines)  # we drop this pretty-print
        return res


# LintResult: the per-file outcome (text + the line numbers of interest). Our Go
# LintResult is the same pair; the reflection loop only needs .text.
@dataclass
class LintResult:
    text: str
    lines: list


# ===== aider/coders/base_coder.py — the reflection loop (L924-944) =====

def run_one(self, user_message, preproc):
    self.init_before_message()
    message = self.preproc_user_input(user_message) if preproc else user_message

    while message:
        self.reflected_message = None
        list(self.send_message(message))   # send -> apply -> (auto-)lint sets
                                           # self.reflected_message on failure

        if not self.reflected_message:
            break                          # nothing to fix -> done (our `ok` path)

        if self.num_reflections >= self.max_reflections:
            # The cap. Without it a model that keeps emitting broken code loops
            # forever. Our ReflectLoop sets HitCap here and returns.
            self.io.tool_warning(
                f"Only {self.max_reflections} reflections allowed, stopping."
            )
            return

        self.num_reflections += 1
        message = self.reflected_message   # THE feedback step: errors become the
                                           # next message. One assignment turns a
                                           # one-shot call into a loop.
