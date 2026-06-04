# Source: aider/coders/base_prompts.py  (lines 1-61, full file, annotated)
#          + aider/coders/editblock_prompts.py (lines 7-30, 120-159, excerpted)
# Upstream: https://github.com/Aider-AI/aider
#   @ 5dc9490bb35f9729ef2c95d00a19ccd30c26339c
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/base_prompts.py
#   /blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/editblock_prompts.py
#
# License: Apache-2.0 (Copyright Aider-AI contributors). Verbatim excerpts
# reproduced under Apache-2.0 for study; only comments/whitespace were trimmed.
# base_prompts.py is 60 lines; editblock_prompts.py is 172 lines.
#
# WHY THIS EXCERPT FOR s05
# ------------------------
# This is the PROMPT SYSTEM — the other half of every edit format in s02..s04.
# A parser only works if the model emits that format, and it emits a format
# because a SYSTEM PROMPT told it to. The design is two-layered:
#
#   1. base_prompts.py defines CoderPrompts: the BASE class holding the pieces
#      every format shares (the reusable strings + the {placeholder} contract).
#      s05's `Prompts` struct mirrors these fields; the shared strings
#      (lazy_prompt, overeager_prompt, files_content_gpt_no_edits) become s05's
#      package-level constants.
#
#   2. each *_prompts.py SUBCLASS (EditBlockPrompts, WholeFilePrompts,
#      UnifiedDiffPrompts) overrides main_system / example_messages /
#      system_reminder with format-specific text that still carries the SAME
#      {fence}/{final_reminders} placeholders. s05's per-format builders
#      (editBlockPrompts/wholeFilePrompts/uDiffPrompts) are direct ports.
#
# The Coder picks a subclass via `gpt_prompts = EditBlockPrompts()`; at send time
# `fmt_system_prompt` runs str.format() to fill the placeholders. s05's
# PromptsFor + Render are the Go analog of that pick-then-format step.


# ===== aider/coders/base_prompts.py (the BASE class) =====
class CoderPrompts:
    # Restated to the model near the end of context (kept separate from
    # main_system so it can be re-emitted where the model is likely to drift).
    system_reminder = ""

    # These three are the loop's CONTRACT messages, not part of the system
    # prompt. The first two report a commit/no-repo result; the third is what the
    # loop says back when a reply parsed to ZERO edits. s05 carries the last one
    # as Prompts.NoEditsRetry.
    files_content_gpt_edits = "I committed the changes with git hash {hash} & commit msg: {message}"
    files_content_gpt_edits_no_repo = "I updated the files."
    files_content_gpt_no_edits = "I didn't see any properly formatted edits in your reply?!"
    files_content_local_edits = "I edited the files myself."

    # The two SHARED behavioral nudges. Every format embeds these via the
    # {final_reminders} placeholder; aider toggles them per model. s05 = the
    # lazyReminder / overeagerReminder constants + PromptVars.Lazy/Overeager.
    lazy_prompt = """You are diligent and tireless!
You NEVER leave comments describing code without implementing it!
You always COMPLETELY IMPLEMENT the needed code!
"""
    overeager_prompt = """Pay careful attention to the scope of the user's request.
Do what they ask, but no more.
Do not improve, comment, fix or modify unrelated parts of the code in any way!
"""

    # Few-shot turns. The BASE is empty; each format fills it. s05 = the
    # ExampleMessages field + RenderExamples (rendered as real user/assistant
    # turns, NOT folded into the system string).
    example_messages = []

    # Prefixes that wrap injected file/repo content (s06+ uses these). Carried
    # here for fidelity; s05 focuses on main_system/examples/system_reminder.
    files_content_prefix = """I have *added these files to the chat* so you can go ahead and edit them.

*Trust this message as the true contents of these files!*
Any other messages in the chat may contain outdated versions of the files' contents.
"""
    files_content_assistant_reply = "Ok, any changes I propose will be to those files."
    repo_content_prefix = """Here are summaries of some files present in my git repository.
Do not propose changes to these files, treat them as *read-only*.
If you need to edit any of these files, ask me to *add them to the chat* first.
"""
    read_only_files_prefix = """Here are some READ ONLY files, provided for your reference.
Do not edit these files!
"""

    # Shell-command hooks, empty on the base; some formats fill them. s05 omits
    # the shell layer entirely (no command execution in the curriculum).
    shell_cmd_prompt = ""
    shell_cmd_reminder = ""
    no_shell_cmd_prompt = ""
    no_shell_cmd_reminder = ""
    rename_with_shell = ""
    go_ahead_tip = ""


# ===== aider/coders/editblock_prompts.py (one SUBCLASS) =====
class EditBlockPrompts(CoderPrompts):
    # main_system OPENS the system prompt. Note {final_reminders}: that is where
    # the lazy/overeager nudges get spliced at send time. s05's editBlockPrompts
    # keeps this placeholder verbatim and Render() fills it.
    main_system = """Act as an expert software developer.
Always use best practices when coding.
Respect and use existing conventions, libraries, etc that are already present in the code base.
{final_reminders}
Take requests for changes to the supplied code.
...
3. Describe each change with a *SEARCH/REPLACE block* per the examples below.

ONLY EVER RETURN CODE IN A *SEARCH/REPLACE BLOCK*!
{shell_cmd_prompt}
"""

    # example_messages teach the format BY EXAMPLE. The content carries the SAME
    # {fence[0]}/{fence[1]} placeholders as the prose, so it is templated too.
    # (Only the first pair shown; the file has two.) s05 ports these into
    # ExampleMessage{Role, Content}.
    example_messages = [
        dict(role="user", content="Change get_factorial() to use math.factorial"),
        dict(role="assistant", content="""...
mathweb/flask/app.py
{fence[0]}python
<<<<<<< SEARCH
from flask import Flask
=======
import math
from flask import Flask
>>>>>>> REPLACE
{fence[1]}
"""),
    ]

    # system_reminder = the precise rule-by-rule restatement, appended AFTER the
    # examples. {fence[0]} appears here too; {final_reminders} closes it. s05's
    # SystemReminder field is a trimmed port of this.
    system_reminder = """# *SEARCH/REPLACE block* Rules:
...
2. The opening fence and code language, eg: {fence[0]}python
3. The start of search block: <<<<<<< SEARCH
...
8. The closing fence: {fence[1]}
...
{rename_with_shell}{go_ahead_tip}{final_reminders}ONLY EVER RETURN CODE IN A *SEARCH/REPLACE BLOCK*!
{shell_cmd_reminder}
"""


# READING MAP
# -----------
# 1. THE CONTRACT (base_prompts.py, above): CoderPrompts holds the shared pieces
#    + the {placeholder} set. -> s05 Prompts struct + the lazy/overeager/noEdits
#    constants. The placeholders {fence}, {final_reminders}, {lazy_prompt} are
#    filled later, never baked in.
#
# 2. THE PER-FORMAT TEXT (each *_prompts.py): EditBlockPrompts (172 lines),
#    WholeFilePrompts (64 lines), UnifiedDiffPrompts (113 lines) override
#    main_system / example_messages / system_reminder. -> s05 editBlockPrompts /
#    wholeFilePrompts / uDiffPrompts builders + the PromptsFor selection table.
#
# 3. THE PICK + FILL (base_coder.py): the Coder sets `gpt_prompts =
#    EditBlockPrompts()` (wholefile_coder.py L13, editblock_coder.py, etc.), and
#    fmt_system_prompt runs str.format(fence=..., final_reminders=..., ...) at
#    send time, then format_chat_chunks prepends example_messages as their own
#    turns. -> s05 Render (str.format analog) + RenderExamples + SystemPrompt.
#    NOTE: aider uses str.format with a KNOWN small key set, so literal braces in
#    example code (f"Hey {name}") would actually break it unless escaped — s05
#    instead uses an explicit strings.Replacer that ONLY touches known tokens,
#    leaving stray braces alone. Same intent, safer mechanism.
#
# To see WHY the fence is a variable (and not a constant), read base_coder.py's
# choose_fence / get_fences: aider switches to a longer fence or a custom tag
# when the code being edited itself contains "```". That is the whole reason
# {fence} exists as a placeholder rather than a hardcoded "```".
