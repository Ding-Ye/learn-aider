package main

import (
	"strings"
	"testing"
)

// fakeRunner scripts a Linter without spawning processes. It returns a fixed
// (output, exitCode) for any command, mirroring how upstream tests stub
// run_cmd_subprocess. exit==0 means clean; non-zero means a lint failure.
func fakeRunner(output string, exit int) func(string, []string, string) (string, int) {
	return func(name string, args []string, dir string) (string, int) {
		return output, exit
	}
}

// Test: a clean file (linter exits 0) yields no report and ok==true. This is the
// "nothing to reflect" path the loop relies on to stop.
func TestLinterCleanFileOK(t *testing.T) {
	l := NewLinter("")
	l.runner = fakeRunner("", 0) // exit 0 == gofmt-clean

	text, ok := l.Lint([]string{"main.go"})
	if !ok {
		t.Fatalf("expected ok==true for a clean file, got false")
	}
	if text != "" {
		t.Fatalf("expected empty report for a clean file, got %q", text)
	}
}

// Test: a broken file (linter exits non-zero with output) is captured, labelled
// with the upstream "# Fix any errors below" header, and reported with ok==false.
func TestLinterBrokenFileCapturesText(t *testing.T) {
	l := NewLinter("")
	l.runner = fakeRunner("main.go:7:1: expected declaration, found '}'", 1)

	text, ok := l.Lint([]string{"main.go"})
	if ok {
		t.Fatalf("expected ok==false for a broken file")
	}
	if !strings.HasPrefix(text, "# Fix any errors below") {
		t.Fatalf("missing upstream reflection header, got:\n%s", text)
	}
	if !strings.Contains(text, "expected declaration") {
		t.Fatalf("linter output not forwarded, got:\n%s", text)
	}
	if !strings.Contains(text, "## Running: gofmt") {
		t.Fatalf("missing per-file command header, got:\n%s", text)
	}
}

// Test: a non-zero exit with NO stdout (the gofmt -l case, which only prints the
// filename) is still turned into a legible failure naming the file, so the model
// has something actionable.
func TestLinterNonzeroNoOutputStillReports(t *testing.T) {
	l := NewLinter("")
	l.runner = fakeRunner("", 1) // exit 1, empty output

	text, ok := l.Lint([]string{"util.go"})
	if ok {
		t.Fatalf("non-zero exit must be treated as a failure")
	}
	if !strings.Contains(text, "util.go") {
		t.Fatalf("report should name the offending file, got:\n%s", text)
	}
}

// Test: SetLinter("", cmd) installs an all-languages override that wins over the
// per-extension default — the --lint-cmd contract from upstream set_linter.
func TestLinterAllCmdOverride(t *testing.T) {
	l := NewLinter("")
	var gotName string
	var gotArgs []string
	l.runner = func(name string, args []string, dir string) (string, int) {
		gotName, gotArgs = name, args
		return "boom", 1
	}
	l.SetLinter("", "go vet")

	_, ok := l.Lint([]string{"pkg/thing.go"})
	if ok {
		t.Fatalf("expected failure from overridden command")
	}
	if gotName != "go" || len(gotArgs) < 2 || gotArgs[0] != "vet" {
		t.Fatalf("override command not used: name=%q args=%v", gotName, gotArgs)
	}
	if gotArgs[len(gotArgs)-1] != "pkg/thing.go" {
		t.Fatalf("file path should be the final arg, got %v", gotArgs)
	}
}

// Test: an unknown extension with no configured command is skipped (clean),
// matching upstream skipping languages with no checker.
func TestLinterUnknownExtSkipped(t *testing.T) {
	l := NewLinter("")
	l.runner = fakeRunner("should not be called", 1)

	text, ok := l.Lint([]string{"notes.txt"})
	if !ok || text != "" {
		t.Fatalf("unknown extension should be skipped as clean, got ok=%v text=%q", ok, text)
	}
}

// Test: line numbers are scraped from "file:line" output and stored 0-based, the
// trimmed port of upstream find_filenames_and_linenums.
func TestFindLineNums(t *testing.T) {
	out := "main.go:7:1: expected declaration\nmain.go:12:3: undefined: foo"
	got := findLineNums(out, "main.go")
	want := []int{6, 11} // 1-based 7,12 -> 0-based 6,11
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
