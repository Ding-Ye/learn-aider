package main

// prompts.go — THE PROMPT SYSTEM. This is the whole point of s05.
//
// s02..s04 each built three working edit-format PARSERS (whole-file,
// SEARCH/REPLACE, unified diff). But a parser only works if the model actually
// emits that format — and the model emits a format because the SYSTEM PROMPT
// told it to. This file is the other half of every previous chapter: the prompt
// that elicits the format the parser then consumes.
//
// The design (straight from upstream aider/coders/base_prompts.py +
// {editblock,wholefile,udiff}_prompts.py):
//
//  1. A coder has NO prompt strings hardcoded in its logic. Instead it owns a
//     `gpt_prompts` object — here, a `Prompts` struct — with one field per
//     reusable PIECE of the system message (MainSystem, ExampleMessages,
//     SystemReminder, plus the lazy/overeager nudges).
//
//  2. Those pieces contain PLACEHOLDERS — {fence}, {lazy_prompt}, {language},
//     {final_reminders} — that are filled in at SEND time, not at definition
//     time. The fence in particular is chosen dynamically (aider switches to a
//     different fence when the code itself contains triple-backticks), so it
//     can't be baked into the literal.
//
//  3. A small RENDERER assembles the final system string from those pieces +
//     vars. The same renderer works for every format; only the `Prompts` value
//     differs. THAT is the decoupling: format choice swaps a data value, the
//     loop code never changes.
//
// Why bother? Because it makes "support a new edit format" a matter of writing
// one more `Prompts` value, and it keeps the few-shot examples (which teach the
// format far better than prose) next to the rules they illustrate.

import (
	"fmt"
	"sort"
	"strings"
)

// PromptVars are the values substituted into a prompt's {placeholders} at send
// time. Upstream passes the equivalent set via str.format(**dict(...)) inside
// fmt_system_prompt (base_coder.py). We keep only the load-bearing few.
//
// Fence is the pair of fence strings, e.g. ["```", "```"]. It is a 2-element
// array because aider picks a DIFFERENT fence (more backticks, or a custom tag)
// when the file being edited itself contains "```", so the open/close can't be
// constant. Lazy/Overeager are short behavioral nudges toggled per model.
// Language is the natural language the model should reply in (default English).
type PromptVars struct {
	Fence     [2]string // {fence[0]} / {fence[1]} — opening/closing code fence
	Lazy      bool      // inject the "don't leave TODOs" reminder
	Overeager bool      // inject the "do what's asked, no more" reminder
	Language  string    // {language} — natural language for prose ("" => English)
}

// DefaultVars is what main.go uses for a normal run: plain triple-backtick
// fence, lazy reminder on (aider turns it on for most models), English prose.
func DefaultVars() PromptVars {
	return PromptVars{
		Fence:    [2]string{"```", "```"},
		Lazy:     true,
		Language: "English",
	}
}

// ExampleMessage is one few-shot turn. Upstream stores these as
// dict(role=..., content=...) in `example_messages`; the content carries the
// SAME {fence}/{placeholder} markers as the prose, so it is templated too.
type ExampleMessage struct {
	Role    string // "user" | "assistant"
	Content string // may contain {fence[0]} / {fence[1]} placeholders
}

// Prompts is the per-format prompt set — the Go analog of upstream's
// CoderPrompts base class and its EditBlockPrompts / WholeFilePrompts /
// UnifiedDiffPrompts subclasses. Each field is a reusable PIECE of the final
// system message. A coder owns one of these instead of hardcoding strings.
//
// The fields mirror upstream attribute-for-attribute so the lineage is obvious:
//
//	upstream attr            -> field here
//	main_system              -> MainSystem
//	example_messages         -> ExampleMessages
//	system_reminder          -> SystemReminder
//	lazy_prompt              -> LazyReminder
//	overeager_prompt         -> OvereagerReminder
//	files_content_gpt_no_edits -> NoEditsRetry  (used by the loop, not the system prompt)
type Prompts struct {
	// MainSystem is the core instruction block. It opens the system prompt and
	// usually contains a {final_reminders} placeholder where the lazy/overeager
	// nudges get spliced in (exactly as upstream main_system does).
	MainSystem string

	// ExampleMessages teach the format by EXAMPLE — a few user/assistant turns
	// showing a request and the model's correctly-formatted reply. For most
	// models this does more work than the prose rules. They are prepended to the
	// real conversation as ordinary messages.
	ExampleMessages []ExampleMessage

	// SystemReminder is appended AFTER the examples: the precise, rule-by-rule
	// restatement of the format ("Every SEARCH/REPLACE block must ..."). Upstream
	// keeps it separate from MainSystem so it can be re-emitted near the end of a
	// long context where the model is most likely to drift.
	SystemReminder string

	// LazyReminder / OvereagerReminder are short behavioral nudges shared by all
	// formats (defined once on the base in upstream). They fill {final_reminders}.
	LazyReminder      string
	OvereagerReminder string

	// NoEditsRetry is what the loop says back to the model when its reply parsed
	// to zero edits ("I didn't see any properly formatted edits..."). It is part
	// of the prompt CONTRACT but flows as a user turn, not in the system string.
	NoEditsRetry string
}

// ---- The shared base pieces (upstream base_prompts.py CoderPrompts) ----
//
// These three strings are identical across every format upstream — they live on
// the CoderPrompts base class. We define them once and let each format embed
// them, so a behavior tweak (e.g. wording of the lazy nudge) happens in one
// place.

const lazyReminder = `You are diligent and tireless!
You NEVER leave comments describing code without implementing it!
You always COMPLETELY IMPLEMENT the needed code!`

const overeagerReminder = `Pay careful attention to the scope of the user's request.
Do what they ask, but no more.
Do not improve, comment, fix or modify unrelated parts of the code in any way!`

const noEditsRetry = "I didn't see any properly formatted edits in your reply?!"

// ============================================================================
// PER-FORMAT PROMPT SETS
// ============================================================================
//
// Each builder returns the Prompts value for one EditFormat. The strings are
// faithful (trimmed) ports of the upstream *_prompts.py files; the placeholders
// {fence[0]}/{fence[1]} and {final_reminders} are preserved verbatim so the
// renderer below can fill them. Read these next to:
//   wholeFilePrompts  <- aider/coders/wholefile_prompts.py
//   editBlockPrompts  <- aider/coders/editblock_prompts.py
//   uDiffPrompts      <- aider/coders/udiff_prompts.py

// wholeFilePrompts: emit the ENTIRE updated file inside a fence (s02's format).
func wholeFilePrompts() Prompts {
	return Prompts{
		MainSystem: `Act as an expert software developer.
Take requests for changes to the supplied code.
If the request is ambiguous, ask questions.
{final_reminders}
Once you understand the request you MUST:
1. Determine if any code changes are needed.
2. Explain any needed changes.
3. If changes are needed, output a copy of each file that needs changes.`,
		ExampleMessages: []ExampleMessage{
			{Role: "user", Content: "Change the greeting to be more casual"},
			{Role: "assistant", Content: `Ok, I will:

1. Switch the greeting text from "Hello" to "Hey".

show_greeting.py
{fence[0]}
import sys

def greeting(name):
    print(f"Hey {name}")

if __name__ == '__main__':
    greeting(sys.argv[1])
{fence[1]}
`},
		},
		SystemReminder: `To suggest changes to a file you MUST return the entire content of the updated file.
You MUST use this *file listing* format:

path/to/filename.js
{fence[0]}
// entire file content ...
// ... goes in between
{fence[1]}

Every *file listing* MUST use this format:
- First line: the filename with any originally provided path; no extra markup.
- Second line: opening {fence[0]}
- ... entire content of the file ...
- Final line: closing {fence[1]}

*NEVER* skip, omit or elide content from a *file listing* using "..." or comments!
{final_reminders}`,
		LazyReminder:      lazyReminder,
		OvereagerReminder: overeagerReminder,
		NoEditsRetry:      noEditsRetry,
	}
}

// editBlockPrompts: emit SEARCH/REPLACE blocks (s03's format; aider's default).
func editBlockPrompts() Prompts {
	return Prompts{
		MainSystem: `Act as an expert software developer.
Always use best practices when coding.
Respect and use existing conventions, libraries, etc that are already present in the code base.
{final_reminders}
Take requests for changes to the supplied code.
If the request is ambiguous, ask questions.

Once you understand the request you MUST:

1. Decide if you need to propose *SEARCH/REPLACE* edits to any files that haven't been added to the chat. You can create new files without asking! If you need to propose edits to existing files not already added to the chat, tell the user their full path names and ask them to *add the files to the chat*.

2. Think step-by-step and explain the needed changes in a few short sentences.

3. Describe each change with a *SEARCH/REPLACE block* per the examples below.

All changes to files must use this *SEARCH/REPLACE block* format.
ONLY EVER RETURN CODE IN A *SEARCH/REPLACE BLOCK*!`,
		ExampleMessages: []ExampleMessage{
			{Role: "user", Content: "Change get_factorial() to use math.factorial"},
			{Role: "assistant", Content: `To make this change we need to modify ` + "`mathweb/flask/app.py`" + ` to:

1. Import the math package.
2. Remove the existing factorial() function.
3. Update get_factorial() to call math.factorial instead.

Here are the *SEARCH/REPLACE* blocks:

mathweb/flask/app.py
{fence[0]}python
<<<<<<< SEARCH
from flask import Flask
=======
import math
from flask import Flask
>>>>>>> REPLACE
{fence[1]}

mathweb/flask/app.py
{fence[0]}python
<<<<<<< SEARCH
    return str(factorial(n))
=======
    return str(math.factorial(n))
>>>>>>> REPLACE
{fence[1]}
`},
		},
		SystemReminder: `# *SEARCH/REPLACE block* Rules:

Every *SEARCH/REPLACE block* must use this format:
1. The *FULL* file path alone on a line, verbatim. No bold asterisks, no quotes.
2. The opening fence and code language, eg: {fence[0]}python
3. The start of search block: <<<<<<< SEARCH
4. A contiguous chunk of lines to search for in the existing source code
5. The dividing line: =======
6. The lines to replace into the source code
7. The end of the replace block: >>>>>>> REPLACE
8. The closing fence: {fence[1]}

Every *SEARCH* section must *EXACTLY MATCH* the existing file content, character for character.
*SEARCH/REPLACE* blocks will *only* replace the first match occurrence.
Keep *SEARCH/REPLACE* blocks concise: include just the changing lines and a few surrounding lines if needed for uniqueness.

To create a new file, use a *SEARCH/REPLACE block* with a new file path, an empty SEARCH section, and the new contents in the REPLACE section.

{final_reminders}ONLY EVER RETURN CODE IN A *SEARCH/REPLACE BLOCK*!`,
		LazyReminder:      lazyReminder,
		OvereagerReminder: overeagerReminder,
		NoEditsRetry:      noEditsRetry,
	}
}

// uDiffPrompts: emit standard unified diffs (s04's format).
func uDiffPrompts() Prompts {
	return Prompts{
		MainSystem: `Act as an expert software developer.
{final_reminders}
Always use best practices when coding.
Respect and use existing conventions, libraries, etc that are already present in the code base.

Take requests for changes to the supplied code.
If the request is ambiguous, ask questions.

For each file that needs to be changed, write out the changes similar to a unified diff like ` + "`diff -U0`" + ` would produce.`,
		ExampleMessages: []ExampleMessage{
			{Role: "user", Content: "Replace is_prime with a call to sympy."},
			{Role: "assistant", Content: `Ok, I will:

1. Add an imports of sympy.
2. Remove the is_prime() function.
3. Replace the existing call to is_prime() with a call to sympy.isprime().

Here are the diffs for those changes:

{fence[0]}diff
--- mathweb/flask/app.py
+++ mathweb/flask/app.py
@@ ... @@
-class MathWeb:
+import sympy
+
+class MathWeb:
@@ ... @@
-def is_prime(x):
-    if x < 2:
-        return False
-    return True
{fence[1]}
`},
		},
		SystemReminder: `# File editing rules:

Return edits similar to unified diffs that ` + "`diff -U0`" + ` would produce.

Make sure you include the first 2 lines with the file paths.
Don't include timestamps with the file paths.

Start each hunk of changes with a ` + "`@@ ... @@`" + ` line.
Don't include line numbers like ` + "`diff -U0`" + ` does; the user's patch tool doesn't need them.

Mark all lines to be removed or changed with ` + "`-`" + ` and all new or modified lines with ` + "`+`" + `.
Indentation matters in the diffs!
Only output hunks that specify changes with ` + "`+`" + ` or ` + "`-`" + ` lines; skip hunks that are entirely unchanging.
To make a new file, show a diff from ` + "`--- /dev/null`" + ` to ` + "`+++ path/to/new/file.ext`" + `.

{final_reminders}`,
		LazyReminder:      lazyReminder,
		OvereagerReminder: overeagerReminder,
		NoEditsRetry:      noEditsRetry,
	}
}

// PromptsFor is the FORMAT-SELECTION table: it maps an EditFormat to its prompt
// set. This is the one place the curriculum reintroduces upstream's per-format
// split (s02..s04 collapsed it into a single Coder for teaching). Adding a new
// edit format = adding one case here plus one builder above.
func PromptsFor(format EditFormat) (Prompts, error) {
	switch format {
	case FormatWhole:
		return wholeFilePrompts(), nil
	case FormatDiff:
		return editBlockPrompts(), nil
	case FormatUDiff:
		return uDiffPrompts(), nil
	default:
		return Prompts{}, fmt.Errorf("no prompt set for edit format %q (known: whole, diff, udiff)", format)
	}
}

// ============================================================================
// THE RENDERER
// ============================================================================

// Render assembles the final SYSTEM PROMPT string for one format, with all
// placeholders filled from vars. This is the Go analog of upstream's
// fmt_system_prompt + format_chat_chunks: MainSystem, then the SystemReminder,
// with {final_reminders} (the lazy/overeager nudges) and {fence} substituted.
//
// NOTE the example messages are NOT part of this string — they are separate
// conversation turns (see RenderExamples). Upstream does the same: the system
// prompt is one message, the few-shot examples are prepended as their own
// user/assistant messages. Keeping them separate is what lets the model treat
// the examples as "prior conversation" rather than instructions about text.
func (p Prompts) Render(vars PromptVars) string {
	final := p.finalReminders(vars)

	var sb strings.Builder
	sb.WriteString(substitute(p.MainSystem, vars, final))
	if p.SystemReminder != "" {
		sb.WriteString("\n\n")
		sb.WriteString(substitute(p.SystemReminder, vars, final))
	}
	return sb.String()
}

// RenderExamples turns the templated few-shot turns into real Messages, with the
// fence placeholders filled. main.go prepends these to the live conversation.
func (p Prompts) RenderExamples(vars PromptVars) []Message {
	final := p.finalReminders(vars)
	msgs := make([]Message, 0, len(p.ExampleMessages))
	for _, ex := range p.ExampleMessages {
		msgs = append(msgs, Message{
			Role:    ex.Role,
			Content: []ContentBlock{{Type: "text", Text: substitute(ex.Content, vars, final)}},
		})
	}
	return msgs
}

// finalReminders builds the {final_reminders} value: the behavioral nudges that
// apply to THIS send. Upstream toggles lazy_prompt / overeager_prompt per model
// and joins them into one block. An empty result is fine — the placeholder just
// collapses to nothing.
func (p Prompts) finalReminders(vars PromptVars) string {
	var parts []string
	if vars.Overeager && p.OvereagerReminder != "" {
		parts = append(parts, p.OvereagerReminder)
	}
	if vars.Lazy && p.LazyReminder != "" {
		parts = append(parts, p.LazyReminder)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n") + "\n"
}

// substitute replaces every {placeholder} in s with its value. We do this with
// an explicit replacer rather than text/template because the prompt strings
// contain lots of literal braces in example code (e.g. f"Hey {name}") that are
// NOT placeholders — a Go template would choke on them, but a fixed replacer
// only touches the tokens we know about. This mirrors upstream's deliberate use
// of str.format with a KNOWN, small key set rather than arbitrary interpolation.
//
// Supported tokens: {fence[0]} {fence[1]} {final_reminders} {language}
// {lazy_prompt} {overeager_prompt}. An UNKNOWN {token} is left untouched (so a
// stray brace in code survives), which is the missing-var behavior the tests pin.
func substitute(s string, vars PromptVars, finalReminders string) string {
	lang := vars.Language
	if lang == "" {
		lang = "English"
	}
	r := strings.NewReplacer(
		"{fence[0]}", vars.Fence[0],
		"{fence[1]}", vars.Fence[1],
		"{final_reminders}", finalReminders,
		"{language}", lang,
		"{lazy_prompt}", pick(vars.Lazy, lazyReminder),
		"{overeager_prompt}", pick(vars.Overeager, overeagerReminder),
	)
	return r.Replace(s)
}

func pick(on bool, s string) string {
	if on {
		return s
	}
	return ""
}

// SystemPrompt is the convenience entry point the loop calls: pick the prompt
// set for `format`, then render it with `vars`. It is the s05 replacement for
// the standalone editBlockSystemPrompt() function each of s02..s04 hardcoded —
// now format choice selects the prompt instead of a different function.
func SystemPrompt(format EditFormat, vars PromptVars) (string, error) {
	p, err := PromptsFor(format)
	if err != nil {
		return "", err
	}
	return p.Render(vars), nil
}

// KnownFormats lists the formats that have a prompt set, sorted for stable
// output (used by main.go's -list and help text).
func KnownFormats() []EditFormat {
	fs := []EditFormat{FormatWhole, FormatDiff, FormatUDiff}
	sort.Slice(fs, func(i, j int) bool { return fs[i] < fs[j] })
	return fs
}
