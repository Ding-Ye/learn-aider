package main

// gitrepo_test.go — exercises the GitRepo wrapper against a REAL git repo.
//
// We don't mock git; we run the actual binary in a throwaway repo created with
// t.TempDir() + `git init`. That keeps the test honest: it proves our os/exec
// command strings are correct, not just that some fake returned what we wanted.
// If `git` is not installed (rare CI image), every test t.Skip()s gracefully
// rather than failing.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRepo creates an isolated git repo in a temp dir with a deterministic
// identity (so commits don't depend on the developer's global git config), and
// seeds it with one committed file so HEAD exists. It returns the GitRepo and
// the repo root.
func newTestRepo(t *testing.T) (*GitRepo, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping GitRepo tests")
	}
	root := t.TempDir()
	// On macOS t.TempDir() lives under /var -> /private/var; resolve it so paths
	// compare equal to git's --show-toplevel output.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "Test Dev"},
		{"config", "user.email", "dev@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Seed an initial commit so HEAD is born.
	writeFile(t, root, "seed.txt", "seed\n")
	if out, err := exec.Command("git", "-C", root, "add", "seed.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add seed: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", root, "commit", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit seed: %v\n%s", err, out)
	}

	repo, err := OpenGitRepo(root)
	if err != nil {
		t.Fatalf("OpenGitRepo: %v", err)
	}
	return repo, root
}

func writeFile(t *testing.T, root, name, content string) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

// commitCount returns the number of commits reachable from HEAD.
func commitCount(t *testing.T, root string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-list", "--count", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-list: %v\n%s", err, out)
	}
	var n int
	if _, err := fmtSscan(strings.TrimSpace(string(out)), &n); err != nil {
		t.Fatalf("parse count %q: %v", out, err)
	}
	return n
}

// fmtSscan is a tiny wrapper so we don't import fmt just for one Sscan.
func fmtSscan(s string, n *int) (int, error) {
	v := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number: " + s)
		}
		v = v*10 + int(c-'0')
	}
	*n = v
	return 1, nil
}

// Test 1: staging + committing an edit produces exactly ONE new commit.
func TestCommitCreatesOneCommit(t *testing.T) {
	repo, root := newTestRepo(t)
	before := commitCount(t, root)

	path := writeFile(t, root, "hello.txt", "hi there\n")
	sha, err := repo.Commit([]string{path}, "", "add hello", AiderDefaults())
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if sha == "" {
		t.Fatal("Commit returned empty sha")
	}

	after := commitCount(t, root)
	if after != before+1 {
		t.Fatalf("expected exactly 1 new commit, got %d -> %d", before, after)
	}
	// The returned sha should be HEAD.
	head, err := repo.HeadSHA()
	if err != nil {
		t.Fatalf("HeadSHA: %v", err)
	}
	if head != sha {
		t.Fatalf("Commit sha %q != HEAD %q", sha, head)
	}
}

// Test 2: the commit message we build is what git actually records (prefix +
// summary), and the co-authored-by trailer is added when requested.
func TestCommitMessageRecorded(t *testing.T) {
	repo, root := newTestRepo(t)

	writeFile(t, root, "a.txt", "alpha\n")
	if _, err := repo.Commit([]string{filepath.Join(root, "a.txt")},
		"aider: ", "tweak alpha", AiderDefaults()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	msg, err := repo.HeadMessage()
	if err != nil {
		t.Fatalf("HeadMessage: %v", err)
	}
	if !strings.HasPrefix(msg, "aider: tweak alpha") {
		t.Fatalf("message missing prefix+summary: %q", msg)
	}

	// Now a commit with a co-authored-by trailer.
	writeFile(t, root, "b.txt", "beta\n")
	if _, err := repo.Commit([]string{filepath.Join(root, "b.txt")},
		"", "add beta", Attribution{CoAuthoredBy: true, Model: "claude-sonnet-4-6"}); err != nil {
		t.Fatalf("Commit (co-authored): %v", err)
	}
	msg, err = repo.HeadMessage()
	if err != nil {
		t.Fatalf("HeadMessage: %v", err)
	}
	if !strings.Contains(msg, "Co-authored-by: aider (claude-sonnet-4-6) <aider@aider.chat>") {
		t.Fatalf("missing co-authored-by trailer in:\n%q", msg)
	}
}

// Test 3: attribution rewrites the AUTHOR name to "<user.name> (aider)".
func TestCommitAttributionAuthor(t *testing.T) {
	repo, root := newTestRepo(t)

	writeFile(t, root, "c.txt", "gamma\n")
	if _, err := repo.Commit([]string{filepath.Join(root, "c.txt")},
		"", "add gamma", AiderDefaults()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	author, err := repo.HeadAuthor()
	if err != nil {
		t.Fatalf("HeadAuthor: %v", err)
	}
	// newTestRepo set user.name = "Test Dev"; AttributeAuthor should append (aider).
	if author != "Test Dev (aider)" {
		t.Fatalf("expected author 'Test Dev (aider)', got %q", author)
	}

	// Control: with no attribution, the author stays the plain configured name.
	writeFile(t, root, "d.txt", "delta\n")
	if _, err := repo.Commit([]string{filepath.Join(root, "d.txt")},
		"", "add delta", Attribution{}); err != nil {
		t.Fatalf("Commit (plain): %v", err)
	}
	author, err = repo.HeadAuthor()
	if err != nil {
		t.Fatalf("HeadAuthor: %v", err)
	}
	if author != "Test Dev" {
		t.Fatalf("expected plain author 'Test Dev', got %q", author)
	}
}

// Test 4: committing a clean tree is a no-op (no empty commit), and IsDirty
// tracks the transition from dirty (after an edit) to clean (after commit).
func TestNoEmptyCommitAndIsDirty(t *testing.T) {
	repo, root := newTestRepo(t)

	// Clean tree to start.
	if repo.IsDirty() {
		t.Fatal("freshly seeded repo should be clean")
	}
	before := commitCount(t, root)

	// Commit with nothing staged and nothing dirty -> ErrNothingToCommit.
	_, err := repo.Commit(nil, "", "noop", AiderDefaults())
	if !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("expected ErrNothingToCommit on clean tree, got %v", err)
	}
	if got := commitCount(t, root); got != before {
		t.Fatalf("clean-tree commit changed history: %d -> %d", before, got)
	}

	// Make an edit: tree is now dirty.
	path := writeFile(t, root, "e.txt", "epsilon\n")
	if !repo.IsDirty() {
		t.Fatal("tree should be dirty after writing a new file")
	}

	// Commit it: succeeds, and the tree becomes clean again.
	if _, err := repo.Commit([]string{path}, "", "add epsilon", AiderDefaults()); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if repo.IsDirty() {
		t.Fatal("tree should be clean after committing the edit")
	}
	if got := commitCount(t, root); got != before+1 {
		t.Fatalf("expected one new commit, got %d -> %d", before, got)
	}
}

// Test 5: TrackedFiles lists committed + staged files, and OpenGitRepo finds the
// root from a SUBDIRECTORY (search-parent-directories behavior).
func TestTrackedFilesAndRootFromSubdir(t *testing.T) {
	repo, root := newTestRepo(t)

	// Commit a file in a nested directory.
	if err := os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	nested := writeFile(t, root, filepath.Join("pkg", "sub", "f.txt"), "nested\n")
	if _, err := repo.Commit([]string{nested}, "", "add nested", AiderDefaults()); err != nil {
		t.Fatalf("Commit nested: %v", err)
	}

	files, err := repo.TrackedFiles()
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	if !contains(files, "seed.txt") || !contains(files, "pkg/sub/f.txt") {
		t.Fatalf("tracked files missing expected entries: %v", files)
	}

	// Opening from a subdir must resolve to the same working-tree root.
	subRepo, err := OpenGitRepo(filepath.Join(root, "pkg", "sub"))
	if err != nil {
		t.Fatalf("OpenGitRepo from subdir: %v", err)
	}
	if subRepo.Root() != repo.Root() {
		t.Fatalf("subdir root %q != repo root %q", subRepo.Root(), repo.Root())
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
