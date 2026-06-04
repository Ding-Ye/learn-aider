package main

// reflect.go — the reflection loop: apply edits, lint them, and if the linter
// complains, feed the errors back to the model and let it try again, bounded by
// a small cap. This is the Go port of upstream run_one's loop around
// `reflected_message` (aider/coders/base_coder.py L924-944).
//
// Until now the loop was single-shot: one user message -> one LLM reply -> apply
// -> done. s09 makes it ITERATIVE. After applying edits we run the Linter; a
// non-clean result becomes a synthetic *user* turn ("# Fix any errors below..."
// + the lint output) and we re-enter the send/apply/lint cycle. We stop the
// instant the lint is clean, or when we hit MaxReflections — without that cap a
// model that keeps producing broken code would loop forever (upstream caps at
// max_reflections = 3, L939).

import (
	"context"
	"fmt"
	"strings"
)

// DefaultMaxReflections matches upstream's `max_reflections = 3`
// (base_coder.py). It is small on purpose: reflection is a safety net for the
// occasional broken edit, not a search loop. If three attempts don't converge,
// something is wrong enough that a human should look.
const DefaultMaxReflections = 3

// SendFunc performs ONE model round-trip given the running conversation, returns
// the assistant's reply text, and (optionally) an extended message slice (e.g.
// with the assistant turn appended). It abstracts "build request -> call
// Provider -> read reply" so the reflection loop doesn't care which edit format
// or provider is in play — the same hole upstream's send_message fills.
type SendFunc func(ctx context.Context, messages []Message) (reply string, err error)

// ReflectResult records what happened across the whole loop, so a CLI (or test)
// can report it. Mirrors the observable effects of run_one: how many extra
// passes we took and whether we gave up at the cap.
type ReflectResult struct {
	Reflections int      // number of EXTRA passes beyond the first (0 == clean first try)
	HitCap      bool     // true if we stopped because we reached MaxReflections
	Clean       bool     // true if the final lint pass was clean
	LintText    string   // the last (unresolved) lint report, if HitCap
	Applied     []Edit   // every edit applied across all passes
	Replies     []string // each pass's raw assistant reply (handy for the CLI)
}

// ReflectLoop ties a Coder, a Linter, and a SendFunc into the self-correcting
// cycle. It owns the reflection counter and the cap; everything format-specific
// lives behind the Coder and SendFunc.
type ReflectLoop struct {
	Coder          Coder
	Linter         *Linter
	Send           SendFunc
	MaxReflections int

	// changedPaths returns the files that should be linted after a set of edits.
	// Defaults to the paths named in the edits themselves; overridable in tests.
	changedPaths func(applied []Edit) []string
}

// NewReflectLoop wires the pieces with the upstream default cap.
func NewReflectLoop(coder Coder, linter *Linter, send SendFunc) *ReflectLoop {
	return &ReflectLoop{
		Coder:          coder,
		Linter:         linter,
		Send:           send,
		MaxReflections: DefaultMaxReflections,
	}
}

// Run drives the loop for one user instruction. The control flow is the heart of
// the chapter and is intentionally a direct transcription of upstream L932-944:
//
//	while message:                      # we keep a current `message`
//	    send_message(message)           # 1. one model round-trip
//	    apply + lint                    # 2. our addition: realise & check edits
//	    if lint clean: break            # 3. converged -> done
//	    if reflections >= cap: stop     # 4. give up rather than loop forever
//	    reflections++; message = errs   # 5. reflect: errors become next message
func (r *ReflectLoop) Run(ctx context.Context, userInstruction string) (*ReflectResult, error) {
	res := &ReflectResult{}

	// The conversation starts with the real user turn. Each reflection appends
	// the assistant reply plus a synthetic user turn carrying the lint errors —
	// the model sees its own broken output followed by the failure, exactly as a
	// human would paste a compiler error back into the chat.
	messages := []Message{userMsg(userInstruction)}
	message := userInstruction

	for message != "" {
		// 1. One model round-trip for the current message.
		reply, err := r.Send(ctx, messages)
		if err != nil {
			return res, fmt.Errorf("send (reflection %d): %w", res.Reflections, err)
		}
		res.Replies = append(res.Replies, reply)
		messages = append(messages, assistantMsg(reply))

		// 2. Realise the edits the model proposed, then lint what changed. This
		//    is the step upstream slots in via apply_updates + lint_edited; in a
		//    single-shot loop (s01..s08) there was nothing after apply.
		edits, err := r.Coder.GetEdits(reply)
		if err != nil {
			return res, fmt.Errorf("parse edits (reflection %d): %w", res.Reflections, err)
		}
		applied, _, err := r.Coder.ApplyEdits(edits)
		if err != nil {
			return res, fmt.Errorf("apply edits (reflection %d): %w", res.Reflections, err)
		}
		res.Applied = append(res.Applied, applied...)

		lintText, ok := r.Linter.Lint(r.pathsToLint(applied))

		// 3. Converged: the linter is happy, we are done.
		if ok {
			res.Clean = true
			return res, nil
		}

		// 4. Not clean. Stop if we've already used our budget — better to hand a
		//    still-broken file to the human than spin forever (upstream emits a
		//    "Only N reflections allowed, stopping." warning here).
		if res.Reflections >= r.MaxReflections {
			res.HitCap = true
			res.LintText = lintText
			return res, nil
		}

		// 5. Reflect: the lint report BECOMES the next user message. This single
		//    assignment is what turns a one-shot call into a feedback loop.
		res.Reflections++
		message = lintText
		messages = append(messages, userMsg(message))
	}

	// message went empty without a lint failure (e.g. the model returned no
	// edits at all): treat as clean — nothing to fix.
	res.Clean = true
	return res, nil
}

// pathsToLint picks which files to check after a pass. By default it lints every
// file touched by the applied edits (deduplicated); upstream similarly lints only
// the edited files, not the whole repo.
func (r *ReflectLoop) pathsToLint(applied []Edit) []string {
	if r.changedPaths != nil {
		return r.changedPaths(applied)
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range applied {
		if e.Path == "" || seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		out = append(out, e.Path)
	}
	return out
}

// ---- small message constructors (text-only, the only kind s09 uses) ----

func userMsg(text string) Message {
	return Message{Role: "user", Content: []ContentBlock{{Type: "text", Text: text}}}
}

func assistantMsg(text string) Message {
	return Message{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: text}}}
}

// replyText concatenates the text blocks of a model response, the one shape s09
// cares about.
func replyText(content []ContentBlock) string {
	var sb strings.Builder
	for _, b := range content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}
