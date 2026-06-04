package main

import (
	"strings"
	"testing"
)

// Test 1 (placeholder substitution): {fence[0]}/{fence[1]} render to the actual
// fence strings, and {final_reminders} is filled from the lazy/overeager flags.
func TestPlaceholderSubstitution(t *testing.T) {
	vars := PromptVars{Fence: [2]string{"```", "```"}, Lazy: true}
	sys, err := SystemPrompt(FormatDiff, vars)
	if err != nil {
		t.Fatalf("SystemPrompt: %v", err)
	}

	// The fence placeholder must be gone, replaced by real backticks.
	if strings.Contains(sys, "{fence[0]}") || strings.Contains(sys, "{fence[1]}") {
		t.Errorf("fence placeholder not substituted:\n%s", sys)
	}
	if !strings.Contains(sys, "```python") {
		t.Errorf("expected substituted fence ```python in output:\n%s", sys)
	}
	// {final_reminders} must be gone, and (Lazy=true) the lazy text spliced in.
	if strings.Contains(sys, "{final_reminders}") {
		t.Errorf("{final_reminders} placeholder not substituted:\n%s", sys)
	}
	if !strings.Contains(sys, "COMPLETELY IMPLEMENT") {
		t.Errorf("Lazy=true should splice the lazy reminder into {final_reminders}:\n%s", sys)
	}

	// A non-default fence proves substitution uses vars, not a constant.
	custom := PromptVars{Fence: [2]string{"<<<CODE", "CODE>>>"}}
	sys2, _ := SystemPrompt(FormatWhole, custom)
	if !strings.Contains(sys2, "<<<CODE") || strings.Contains(sys2, "```") {
		t.Errorf("custom fence not honored:\n%s", sys2)
	}
}

// Test 2 (format selection returns the right prompt set): each EditFormat maps
// to a distinct prompt whose body matches that format's markers.
func TestFormatSelectionReturnsRightPromptSet(t *testing.T) {
	cases := []struct {
		format   EditFormat
		must     string // a literal that MUST appear
		mustNot  string // a literal that must NOT appear
		whatMust string
	}{
		{FormatDiff, "<<<<<<< SEARCH", "@@ ... @@", "editblock prompt has the SEARCH marker"},
		{FormatWhole, "entire content of the updated file", "<<<<<<< SEARCH", "wholefile prompt has no S/R markers"},
		{FormatUDiff, "@@ ... @@", "<<<<<<< SEARCH", "udiff prompt has hunk markers, no S/R"},
	}
	for _, c := range cases {
		p, err := PromptsFor(c.format)
		if err != nil {
			t.Fatalf("PromptsFor(%s): %v", c.format, err)
		}
		sys := p.Render(DefaultVars())
		if !strings.Contains(sys, c.must) {
			t.Errorf("%s: missing %q in:\n%s", c.whatMust, c.must, sys)
		}
		if c.mustNot != "" && strings.Contains(sys, c.mustNot) {
			t.Errorf("%s: should NOT contain %q in:\n%s", c.whatMust, c.mustNot, sys)
		}
	}
}

// Test 3 (example messages included + alternate roles): each format ships
// few-shot turns, they render with the fence filled, and they alternate
// user/assistant starting with user.
func TestExampleMessagesIncludedAndAlternate(t *testing.T) {
	for _, f := range KnownFormats() {
		p, _ := PromptsFor(f)
		msgs := p.RenderExamples(DefaultVars())
		if len(msgs) == 0 {
			t.Errorf("format %s has no example messages", f)
			continue
		}
		for i, m := range msgs {
			wantRole := "user"
			if i%2 == 1 {
				wantRole = "assistant"
			}
			if m.Role != wantRole {
				t.Errorf("format %s example %d: role=%q want %q", f, i, m.Role, wantRole)
			}
			// Rendered examples must not still carry the fence placeholder.
			if strings.Contains(firstText(m.Content), "{fence[0]}") {
				t.Errorf("format %s example %d: fence placeholder left in content", f, i)
			}
		}
	}

	// The editblock assistant example must actually demonstrate the format.
	p, _ := PromptsFor(FormatDiff)
	msgs := p.RenderExamples(DefaultVars())
	if len(msgs) < 2 || !strings.Contains(firstText(msgs[1].Content), "<<<<<<< SEARCH") {
		t.Errorf("editblock few-shot assistant turn should show a SEARCH/REPLACE block")
	}
}

// Test 4 (missing-var / unknown-token handling): an unknown {token} is left
// untouched (so literal braces in example code survive), and an unknown format
// returns an error rather than panicking.
func TestMissingVarHandling(t *testing.T) {
	// Unknown placeholder is preserved verbatim.
	got := substitute("keep {this} but fill {fence[0]}", DefaultVars(), "")
	if !strings.Contains(got, "{this}") {
		t.Errorf("unknown token {this} should be left untouched, got: %q", got)
	}
	if !strings.Contains(got, "```") {
		t.Errorf("known token {fence[0]} should still be filled, got: %q", got)
	}

	// Literal braces inside example code (f"Hey {name}") must survive rendering.
	whole, _ := PromptsFor(FormatWhole)
	ex := firstText(whole.RenderExamples(DefaultVars())[1].Content)
	if !strings.Contains(ex, "{name}") {
		t.Errorf("literal {name} in example code must survive substitution:\n%s", ex)
	}

	// Unknown format => error, no panic.
	if _, err := SystemPrompt(EditFormat("bogus"), DefaultVars()); err == nil {
		t.Errorf("SystemPrompt with unknown format should error")
	}
	if _, err := PromptsFor(EditFormat("nope")); err == nil {
		t.Errorf("PromptsFor with unknown format should error")
	}
}

// Test 5 (format choice swaps the system string without touching loop code):
// rendering two different formats with the SAME vars yields different system
// strings — proving the decoupling that is the whole point of s05.
func TestFormatChoiceSwapsSystemString(t *testing.T) {
	vars := DefaultVars()
	whole, _ := SystemPrompt(FormatWhole, vars)
	diff, _ := SystemPrompt(FormatDiff, vars)
	udiff, _ := SystemPrompt(FormatUDiff, vars)

	if whole == diff || diff == udiff || whole == udiff {
		t.Errorf("different formats must produce different system prompts")
	}
	// Only the data (the Prompts value) changed — the call site is identical.
	if !strings.Contains(diff, "SEARCH") {
		t.Errorf("diff system prompt should mention SEARCH")
	}
	if strings.Contains(whole, "SEARCH/REPLACE") {
		t.Errorf("whole-file system prompt should not mention SEARCH/REPLACE")
	}
}

// Test 6 (lazy/overeager nudges are toggled by vars): the {final_reminders}
// content depends on the flags, and an empty set collapses cleanly.
func TestFinalRemindersToggling(t *testing.T) {
	p, _ := PromptsFor(FormatDiff)

	none := p.Render(PromptVars{Fence: [2]string{"```", "```"}}) // both off
	if strings.Contains(none, "COMPLETELY IMPLEMENT") {
		t.Errorf("Lazy=false should NOT include the lazy reminder")
	}
	if strings.Contains(none, "{final_reminders}") {
		t.Errorf("{final_reminders} must collapse to empty when no nudges are set")
	}

	both := p.Render(PromptVars{Fence: [2]string{"```", "```"}, Lazy: true, Overeager: true})
	if !strings.Contains(both, "COMPLETELY IMPLEMENT") {
		t.Errorf("Lazy=true should include the lazy reminder")
	}
	if !strings.Contains(both, "no more") {
		t.Errorf("Overeager=true should include the overeager reminder")
	}
}

// Test 7 (the NoEditsRetry contract message is carried per format): part of the
// prompt contract used by the loop when a reply parses to zero edits.
func TestNoEditsRetryCarried(t *testing.T) {
	for _, f := range KnownFormats() {
		p, _ := PromptsFor(f)
		if p.NoEditsRetry == "" {
			t.Errorf("format %s should carry a NoEditsRetry message", f)
		}
	}
}
