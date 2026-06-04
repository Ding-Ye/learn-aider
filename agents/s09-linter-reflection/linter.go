package main

// linter.go — run a linter over changed files and turn failures into text the
// LLM can read. This is the Go port of upstream aider/linter.py.
//
// Upstream Linter.lint (linter.py L82) picks a checker for a file: a per-language
// callable (py_lint), a configured shell command, or a built-in tree-sitter
// syntax pass (basic_lint). It runs the checker, and if it reports problems it
// formats them under a "# Fix any errors below, if possible." header with the
// offending file:line so the model can locate them. We keep that exact shape:
//
//   - a `commands` map of extension -> shell command (the pluggable per-language
//     linter, e.g. ".go" -> "gofmt -l -e", ".py" -> "ruff check"),
//   - a built-in `gofmt -e`-style syntax check for .go files when no command is
//     configured (our analog of basic_lint),
//   - one Lint(paths) entry point returning (errText, ok): ok==true means clean,
//     ok==false means errText holds the formatted problems to reflect back.
//
// What we deliberately DROP vs upstream: the tree-sitter ERROR-node walk for 40+
// languages, flake8's exact fatal-code selection, and TreeContext's pretty
// source rendering with the █ marker. Those are quality-of-life; the load-bearing
// idea is "run a checker, capture its output, label it for the model".

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// LintResult is one file's outcome. Mirrors upstream's LintResult dataclass
// (linter.py L171-174): the human-readable text plus the line numbers of
// interest. We keep Lines so a caller could render context like upstream's
// tree_context, but the reflection loop only needs Text.
type LintResult struct {
	Text  string // formatted error output ("" when the file is clean)
	Lines []int  // 0-based line numbers the errors point at
}

// Linter runs a checker per file and aggregates failures. Mirrors upstream
// Linter (linter.py L21): a registry of per-language checkers plus an optional
// "all languages" override command.
type Linter struct {
	// root, if set, is prepended to relative paths and used as the command's
	// working directory (upstream Linter.root). Empty means "use paths as given".
	Root string

	// commands maps a lowercased file extension (".go", ".py") to a shell command
	// template. The file path is appended as the final argument before running —
	// exactly like upstream run_cmd does with oslex.quote(rel_fname) (L47-48).
	// A non-zero exit code means "lint failed"; its stdout+stderr is the error.
	commands map[string]string

	// allCmd, when set, overrides every per-extension command (upstream
	// all_lint_cmd, set via --lint-cmd with no language). Empty means unused.
	allCmd string

	// runner runs a command in dir and returns (combinedOutput, exitCode). It is
	// a field so tests can inject a fake linter without spawning processes —
	// upstream tests likewise stub run_cmd_subprocess.
	runner func(name string, args []string, dir string) (string, int)
}

// NewLinter builds a Linter with the built-in defaults: ".go" files are checked
// with `gofmt -l -e` (lists files that aren't gofmt-clean AND surfaces parse
// errors), which is our self-contained stand-in for upstream's py_lint stack
// (basic_lint + compile + flake8). Add or override with SetLinter.
func NewLinter(root string) *Linter {
	return &Linter{
		Root: root,
		commands: map[string]string{
			".go": "gofmt -l -e",
		},
		runner: execRunner,
	}
}

// SetLinter registers a command for one extension, or (with ext=="") sets the
// all-languages override. Mirrors upstream Linter.set_linter (L31-36): an empty
// language means "use this command for everything".
func (l *Linter) SetLinter(ext, cmd string) {
	cmd = strings.TrimSpace(cmd)
	if ext == "" {
		l.allCmd = cmd
		return
	}
	if l.commands == nil {
		l.commands = map[string]string{}
	}
	l.commands[strings.ToLower(ext)] = cmd
}

// Lint runs the appropriate checker over each path and returns (errText, ok).
// ok==true means every file is clean (errText==""). ok==false means at least one
// file failed and errText is the concatenated, model-ready report. This is the
// surface the reflection loop calls after edits are applied — the Go analog of
// the per-file loop in base_coder.lint_edited (which calls Linter.lint L82).
func (l *Linter) Lint(paths []string) (string, bool) {
	var reports []string
	for _, p := range paths {
		res := l.lintOne(p)
		if res == nil || res.Text == "" {
			continue // clean file (upstream returns None -> skipped)
		}
		reports = append(reports, res.Text)
	}
	if len(reports) == 0 {
		return "", true
	}
	// Upstream prefixes the whole reflection with this exact instruction so the
	// model knows the following text is a problem report, not a new request
	// (linter.py L111).
	out := "# Fix any errors below, if possible.\n\n" + strings.Join(reports, "\n\n")
	return out, false
}

// lintOne selects and runs the checker for a single file. Precedence mirrors
// upstream Linter.lint (L90-106): an explicit allCmd override wins, then a
// per-extension command, otherwise the built-in syntax check.
func (l *Linter) lintOne(path string) *LintResult {
	ext := strings.ToLower(filepath.Ext(path))

	cmd := l.allCmd
	if cmd == "" {
		cmd = l.commands[ext]
	}
	if cmd == "" {
		// No configured command: fall back to the built-in check (analog of
		// basic_lint). Today that only knows Go; other extensions are skipped,
		// exactly as upstream skips languages with no tree-sitter parser.
		if ext == ".go" {
			return l.runCmd("gofmt -l -e", path)
		}
		return nil
	}
	return l.runCmd(cmd, path)
}

// runCmd appends the file path to the command template, runs it, and — only on a
// non-zero exit — formats the output as a LintResult. A zero exit is "clean" and
// returns nil, matching upstream run_cmd's `if returncode == 0: return` (L62-63).
func (l *Linter) runCmd(cmdTemplate, path string) *LintResult {
	rel := l.relPath(path)
	fields := strings.Fields(cmdTemplate)
	if len(fields) == 0 {
		return nil
	}
	name := fields[0]
	args := append(append([]string{}, fields[1:]...), rel)

	stdout, code := l.runner(name, args, l.Root)
	if code == 0 {
		return nil // zero exit status == lint clean
	}

	// Build the "## Running: <cmd> <file>" header upstream uses (L65), then the
	// captured output. gofmt -l prints just the filename when a file isn't
	// formatted; gofmt -e prints "file:line:col: message" on a parse error.
	full := strings.TrimSpace(stdout)
	if full == "" {
		// Non-zero exit with no output (e.g. gofmt -l listing the file): make the
		// failure legible by naming the file so the model knows what to fix.
		full = rel + ": not gofmt-clean (formatting or syntax issue)"
	}
	text := fmt.Sprintf("## Running: %s %s\n\n%s", name, rel, full)

	return &LintResult{Text: text, Lines: findLineNums(full, rel)}
}

// relPath returns path relative to Root when Root is set, mirroring upstream
// get_rel_fname (L38-45). On failure it falls back to the original path.
func (l *Linter) relPath(path string) string {
	if l.Root == "" {
		return path
	}
	if rel, err := filepath.Rel(l.Root, path); err == nil {
		return rel
	}
	return path
}

// findLineNums scrapes "<file>:<n>" occurrences out of linter output and returns
// the 0-based line numbers, a trimmed port of upstream
// find_filenames_and_linenums (L272-285) specialised to one filename. Errors
// from tools like gofmt -e look like "main.go:7:1: expected ...".
func findLineNums(text, rel string) []int {
	var nums []int
	seen := map[int]bool{}
	prefix := rel + ":"
	for _, line := range strings.Split(text, "\n") {
		idx := strings.Index(line, prefix)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(prefix):]
		// Read leading digits as the line number; stop at the first non-digit.
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		n := 0
		fmt.Sscanf(rest[:end], "%d", &n)
		if n > 0 && !seen[n] {
			seen[n] = true
			nums = append(nums, n-1) // upstream stores 0-based (L78)
		}
	}
	sort.Ints(nums)
	return nums
}

// execRunner is the real process runner used outside tests. It returns the
// command's combined stdout+stderr and its exit code; a failure to even start
// the process is reported as exit code 1 with the error text, so the caller
// always gets *something* legible (upstream prints "Unable to execute lint
// command" and returns None — we surface it instead of swallowing it).
func execRunner(name string, args []string, dir string) (string, int) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	// Command not found / could not start.
	return fmt.Sprintf("%s\n(failed to run %s: %v)", string(out), name, err), 1
}
