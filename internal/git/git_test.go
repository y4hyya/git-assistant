package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git-assist/internal/types"
)

// ── Helpers ────────────────────────────────────────────

// runGit runs a git command in the current working directory and fails the
// test on error.
func runGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// scratchRepo creates an empty repository in a temp dir and chdirs into it for
// the duration of the test. The host's global/system git config is disabled so
// results don't depend on the developer's machine (autocrlf, gpgsign, hooks,
// templates, aliases).
func scratchRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	dir := t.TempDir()
	t.Chdir(dir)

	runGit(t, "init", "-q", "-b", "main")
	runGit(t, "config", "user.name", "git-assist test")
	runGit(t, "config", "user.email", "test@example.invalid")
	runGit(t, "config", "commit.gpgsign", "false")
	return dir
}

// write creates a file (and any parent directories) with the given content.
func write(t *testing.T, path, content string) {
	t.Helper()
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// commitAll stages everything and commits — used to build fixtures, never to
// exercise the code under test.
func commitAll(t *testing.T, message string) {
	t.Helper()
	runGit(t, "add", "-A")
	runGit(t, "commit", "-q", "-m", message)
}

// statusByPath indexes GetStatus() output by path.
func statusByPath(t *testing.T) map[string]types.FileEntry {
	t.Helper()
	entries, err := GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	byPath := make(map[string]types.FileEntry, len(entries))
	for _, e := range entries {
		byPath[e.Path] = e
	}
	return byPath
}

func commitCount(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, "rev-list", "--count", "HEAD"))
}

// ── GetStatus ──────────────────────────────────────────

// Paths that plain `git status --porcelain` C-quotes (spaces, non-ASCII,
// quotes) used to arrive with the quoting intact, which made them unusable as
// pathspecs and invisible in the picker.
func TestGetStatusExoticPaths(t *testing.T) {
	scratchRepo(t)

	write(t, "tracked.txt", "one\n")
	commitAll(t, "init")
	write(t, "tracked.txt", "one\ntwo\n")

	write(t, "has space.txt", "x\n")
	write(t, "döküman.md", "x\n")
	write(t, `qu"ote.txt`, "x\n")
	write(t, "arrow -> name.txt", "x\n")
	write(t, "assets/img/logo.txt", "x\n")

	got := statusByPath(t)

	tests := []struct {
		name string
		path string
		want types.FileStatus
	}{
		{"modified tracked file", "tracked.txt", types.StatusModified},
		{"filename with a space", "has space.txt", types.StatusUntracked},
		{"non-ASCII filename", "döküman.md", types.StatusUntracked},
		{"filename with a quote", `qu"ote.txt`, types.StatusUntracked},
		{"filename containing \" -> \"", "arrow -> name.txt", types.StatusUntracked},
		{"file inside an untracked directory", "assets/img/logo.txt", types.StatusUntracked},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry, ok := got[tc.path]
			if !ok {
				t.Fatalf("GetStatus() has no entry for %q; got %v", tc.path, keys(got))
			}
			if entry.Status != tc.want {
				t.Errorf("status for %q = %v, want %v", tc.path, entry.Status, tc.want)
			}
		})
	}

	// --untracked-files=all lists files, never the bare directory.
	if _, ok := got["assets/"]; ok {
		t.Errorf("GetStatus() reported the untracked directory %q as an entry", "assets/")
	}
}

func TestGetStatusRename(t *testing.T) {
	scratchRepo(t)
	write(t, "plain.txt", "content\n")
	commitAll(t, "init")
	runGit(t, "mv", "plain.txt", "renamed.txt")

	entries, err := GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	// The original path is a field of the rename record, not a record of its
	// own — a naive NUL split would yield a phantom second entry.
	if len(entries) != 1 {
		t.Fatalf("GetStatus() = %d entries, want 1: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.Path != "renamed.txt" {
		t.Errorf("Path = %q, want %q", e.Path, "renamed.txt")
	}
	if e.OrigPath != "plain.txt" {
		t.Errorf("OrigPath = %q, want %q", e.OrigPath, "plain.txt")
	}
	if e.Status != types.StatusRenamed {
		t.Errorf("Status = %v, want StatusRenamed", e.Status)
	}
}

func TestParseStatusZ(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []types.FileEntry
	}{
		{
			name: "empty output",
			data: "",
			want: nil,
		},
		{
			name: "modified and untracked",
			data: " M a.txt\x00?? b.txt\x00",
			want: []types.FileEntry{
				{Path: "a.txt", Status: types.StatusModified},
				{Path: "b.txt", Status: types.StatusUntracked},
			},
		},
		{
			name: "rename consumes the following field",
			data: "R  new.txt\x00old.txt\x00 M after.txt\x00",
			want: []types.FileEntry{
				{Path: "new.txt", OrigPath: "old.txt", Status: types.StatusRenamed},
				{Path: "after.txt", Status: types.StatusModified},
			},
		},
		{
			name: "copy consumes the following field",
			data: "C  copy.txt\x00source.txt\x00",
			want: []types.FileEntry{
				{Path: "copy.txt", OrigPath: "source.txt", Status: types.StatusModified},
			},
		},
		{
			// Renamed in the index, deleted in the worktree: classified as a
			// deletion but still a two-field record.
			name: "RD record still consumes the original path",
			data: "RD new.txt\x00old.txt\x00?? other.txt\x00",
			want: []types.FileEntry{
				{Path: "new.txt", OrigPath: "old.txt", Status: types.StatusDeleted},
				{Path: "other.txt", Status: types.StatusUntracked},
			},
		},
		{
			name: "untracked file whose name starts with R",
			data: "?? Rename.txt\x00",
			want: []types.FileEntry{
				{Path: "Rename.txt", Status: types.StatusUntracked},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseStatusZ(tc.data)
			if len(got) != len(tc.want) {
				t.Fatalf("parseStatusZ() = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// ── Commit ─────────────────────────────────────────────

func TestCommitStagesBothHalvesOfRename(t *testing.T) {
	scratchRepo(t)
	write(t, "plain.txt", "content\n")
	commitAll(t, "init")
	runGit(t, "mv", "plain.txt", "renamed.txt")

	entries, err := GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if err := Commit(entries, nil, "chore: rename"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	tree := runGit(t, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "renamed.txt") {
		t.Errorf("HEAD tree missing renamed.txt:\n%s", tree)
	}
	if strings.Contains(tree, "plain.txt") {
		t.Errorf("HEAD tree still tracks plain.txt (deletion not committed):\n%s", tree)
	}
	if left := statusByPath(t); len(left) != 0 {
		t.Errorf("working tree not clean after committing the rename: %v", keys(left))
	}
}

func TestCommitFailsWhenAPathCannotBeStaged(t *testing.T) {
	scratchRepo(t)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "init")

	write(t, "real.txt", "real\n")
	before := commitCount(t)

	err := Commit([]types.FileEntry{
		{Path: "real.txt", Status: types.StatusUntracked},
		{Path: "ghost.txt", Status: types.StatusUntracked},
	}, nil, "feat: partial")

	if err == nil {
		t.Fatal("Commit() succeeded with an unstageable path, want error")
	}
	if !strings.Contains(err.Error(), "ghost.txt") {
		t.Errorf("error %q does not name the failed path", err)
	}
	if after := commitCount(t); after != before {
		t.Errorf("commit count = %s, want %s (nothing should have been committed)", after, before)
	}
}

func TestCommitStagesOnlySelectedFiles(t *testing.T) {
	scratchRepo(t)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "init")

	write(t, "wanted.txt", "yes\n")
	write(t, "unwanted.txt", "no\n")
	// Pre-stage everything: Commit() must reset and keep only the selection.
	runGit(t, "add", "-A")

	if err := Commit([]types.FileEntry{{Path: "wanted.txt", Status: types.StatusUntracked}}, nil, "feat: wanted"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	tree := runGit(t, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "wanted.txt") {
		t.Errorf("HEAD tree missing wanted.txt:\n%s", tree)
	}
	if strings.Contains(tree, "unwanted.txt") {
		t.Errorf("HEAD tree contains unselected unwanted.txt:\n%s", tree)
	}
}

func TestCommitFirstCommitInEmptyRepo(t *testing.T) {
	scratchRepo(t)
	write(t, "first.txt", "hello\n")

	entries, err := GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if err := Commit(entries, nil, "feat: first"); err != nil {
		t.Fatalf("Commit on repo without HEAD: %v", err)
	}
	if got := commitCount(t); got != "1" {
		t.Errorf("commit count = %s, want 1", got)
	}
}

func TestAmendStagesBothHalvesOfRename(t *testing.T) {
	scratchRepo(t)
	write(t, "plain.txt", "content\n")
	commitAll(t, "init")
	runGit(t, "mv", "plain.txt", "renamed.txt")

	entries, err := GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if err := Amend(entries, "init (with rename)"); err != nil {
		t.Fatalf("Amend: %v", err)
	}

	tree := runGit(t, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "renamed.txt") || strings.Contains(tree, "plain.txt") {
		t.Errorf("amended tree = %q, want only renamed.txt", strings.TrimSpace(tree))
	}
	if got := commitCount(t); got != "1" {
		t.Errorf("commit count = %s, want 1 (amend must not add a commit)", got)
	}
}

// ── Gitignore ──────────────────────────────────────────

func TestAddToGitignoreAnchorsAndDedupes(t *testing.T) {
	scratchRepo(t)

	if err := AddToGitignore([]string{"config.json", "assets/logo.txt"}); err != nil {
		t.Fatalf("AddToGitignore: %v", err)
	}
	// Re-adding the same path in every spelling must be a no-op.
	if err := AddToGitignore([]string{"config.json", "./config.json", "/config.json", "assets/logo.txt"}); err != nil {
		t.Fatalf("AddToGitignore (second call): %v", err)
	}

	got := GetGitignoreEntries()
	want := []string{"/config.json", "/assets/logo.txt"}
	if len(got) != len(want) {
		t.Fatalf("GetGitignoreEntries() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}

	// Anchoring is the point: the pattern must match the root file only.
	write(t, "config.json", "{}\n")
	write(t, "deploy/config.json", "{}\n")
	status := statusByPath(t)
	if _, ok := status["config.json"]; ok {
		t.Errorf("root config.json should be ignored, but git still reports it")
	}
	if _, ok := status["deploy/config.json"]; !ok {
		t.Errorf("deploy/config.json was wrongly ignored by the root pattern; entries: %v", keys(status))
	}
}

func TestAddToGitignoreDedupesAgainstUnanchoredLegacyEntries(t *testing.T) {
	scratchRepo(t)
	write(t, ".gitignore", "node_modules\n")

	if err := AddToGitignore([]string{"node_modules"}); err != nil {
		t.Fatalf("AddToGitignore: %v", err)
	}
	got := GetGitignoreEntries()
	if len(got) != 1 || got[0] != "node_modules" {
		t.Errorf("GetGitignoreEntries() = %v, want [node_modules] unchanged", got)
	}
}

// ── Diff ───────────────────────────────────────────────

func TestGetFileDiffUntracked(t *testing.T) {
	scratchRepo(t)

	t.Run("strips carriage returns", func(t *testing.T) {
		write(t, "crlf.txt", "alpha\r\nbeta\r\n")
		diff, err := GetFileDiff("crlf.txt", types.StatusUntracked)
		if err != nil {
			t.Fatalf("GetFileDiff: %v", err)
		}
		if strings.Contains(diff, "\r") {
			t.Errorf("diff still contains a carriage return: %q", diff)
		}
		if !strings.Contains(diff, "+ alpha") {
			t.Errorf("diff = %q, want a '+ alpha' line", diff)
		}
	})

	t.Run("refuses huge files", func(t *testing.T) {
		big := strings.Repeat("x", 3<<20)
		write(t, "big.log", big)
		diff, err := GetFileDiff("big.log", types.StatusUntracked)
		if err != nil {
			t.Fatalf("GetFileDiff: %v", err)
		}
		if !strings.Contains(diff, "too large") {
			t.Errorf("diff = %q, want a 'too large' placeholder", diff)
		}
	})

	t.Run("explains directories", func(t *testing.T) {
		if err := os.MkdirAll("embedded", 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		diff, err := GetFileDiff("embedded", types.StatusUntracked)
		if err != nil {
			t.Fatalf("GetFileDiff on a directory returned an error: %v", err)
		}
		if !strings.Contains(diff, "directory") {
			t.Errorf("diff = %q, want a directory explanation", diff)
		}
	})
}

func TestGetFileDiffTrackedStripsCarriageReturns(t *testing.T) {
	scratchRepo(t)
	runGit(t, "config", "core.autocrlf", "false")
	write(t, "crlf.txt", "alpha\r\nbeta\r\n")
	commitAll(t, "init")
	write(t, "crlf.txt", "alpha\r\ngamma\r\n")

	diff, err := GetFileDiff("crlf.txt", types.StatusModified)
	if err != nil {
		t.Fatalf("GetFileDiff: %v", err)
	}
	if strings.Contains(diff, "\r") {
		t.Errorf("tracked diff still contains a carriage return: %q", diff)
	}
}

// ── Ahead/behind ───────────────────────────────────────

func TestGetAheadBehind(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "init")

	if ahead, behind, hasUpstream := GetAheadBehind("main"); hasUpstream {
		t.Errorf("GetAheadBehind() = %d, %d, true for a branch with no upstream; want hasUpstream=false", ahead, behind)
	}

	remote := t.TempDir()
	runGit(t, "init", "-q", "--bare", remote)
	runGit(t, "remote", "add", "origin", remote)
	runGit(t, "push", "-q", "-u", "origin", "main")

	ahead, behind, hasUpstream := GetAheadBehind("main")
	if !hasUpstream {
		t.Fatal("GetAheadBehind() reports no upstream after push -u")
	}
	if ahead != 0 || behind != 0 {
		t.Errorf("GetAheadBehind() = %d, %d; want 0, 0 right after pushing", ahead, behind)
	}

	write(t, "b.txt", "b\n")
	commitAll(t, "second")
	if ahead, behind, hasUpstream = GetAheadBehind("main"); !hasUpstream || ahead != 1 || behind != 0 {
		t.Errorf("GetAheadBehind() = %d, %d, %v; want 1, 0, true", ahead, behind, hasUpstream)
	}
}

// ── Undo ───────────────────────────────────────────────

func TestUndoLastCommit(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "init")

	err := UndoLastCommit()
	if err == nil {
		t.Fatal("UndoLastCommit() on the first commit succeeded, want a friendly error")
	}
	if strings.Contains(err.Error(), "ambiguous argument") {
		t.Errorf("UndoLastCommit() leaked git's raw fatal: %v", err)
	}
	if !strings.Contains(err.Error(), "first commit") {
		t.Errorf("error = %q, want it to mention the first commit", err)
	}
	if got := commitCount(t); got != "1" {
		t.Errorf("commit count = %s, want 1 (nothing should have changed)", got)
	}

	write(t, "b.txt", "b\n")
	commitAll(t, "second")
	if err := UndoLastCommit(); err != nil {
		t.Fatalf("UndoLastCommit() on a two-commit repo: %v", err)
	}
	if got := commitCount(t); got != "1" {
		t.Errorf("commit count = %s, want 1 after undo", got)
	}
}

// ── Branch listings ────────────────────────────────────

func TestBranchListingsIgnoreNonOriginRemotes(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "init")

	origin := t.TempDir()
	upstream := t.TempDir()
	runGit(t, "init", "-q", "--bare", origin)
	runGit(t, "init", "-q", "--bare", upstream)
	runGit(t, "remote", "add", "origin", origin)
	runGit(t, "remote", "add", "upstream", upstream)
	runGit(t, "push", "-q", "origin", "main:main")
	runGit(t, "push", "-q", "upstream", "main:release")

	for _, b := range GetBranches("main") {
		if b == "release" || strings.HasPrefix(b, "upstream/") {
			t.Errorf("GetBranches() leaked a non-origin branch: %q (all: %v)", b, GetBranches("main"))
		}
	}
	for _, e := range GetAllBranches() {
		if e.Name == "release" || strings.HasPrefix(e.Name, "upstream/") {
			t.Errorf("GetAllBranches() leaked a non-origin branch: %q", e.Name)
		}
	}
}

func TestGetAllBranchesWorktreePrefix(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "init")

	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, "worktree", "add", "-q", "-b", "feature-wt", wt)

	var found bool
	for _, e := range GetAllBranches() {
		if strings.HasPrefix(e.Name, "+") || strings.HasPrefix(e.Name, "*") {
			t.Errorf("GetAllBranches() kept a marker prefix in the name: %q", e.Name)
		}
		if e.Name == "feature-wt" {
			found = true
			if !e.CheckedOutElsewhere {
				t.Errorf("feature-wt is checked out in another worktree but CheckedOutElsewhere is false")
			}
		}
	}
	if !found {
		t.Errorf("GetAllBranches() did not list feature-wt: %+v", GetAllBranches())
	}
}

// ── Repo root ──────────────────────────────────────────

func TestRepoToplevelFromSubdirectory(t *testing.T) {
	root := scratchRepo(t)
	write(t, "sub/inner.txt", "x\n")
	commitAll(t, "init")

	t.Chdir(filepath.Join(root, "sub"))

	got, err := RepoToplevel()
	if err != nil {
		t.Fatalf("RepoToplevel: %v", err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", got, err)
	}
	if gotResolved != want {
		t.Errorf("RepoToplevel() = %q, want %q", gotResolved, want)
	}
}

func TestRepoToplevelOutsideRepo(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	t.Chdir(t.TempDir())

	if root, err := RepoToplevel(); err == nil {
		t.Errorf("RepoToplevel() = %q, nil outside a repository; want an error", root)
	}
}

func TestIsBinaryContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"plain text", "package main\n", false},
		{"utf-8 text", "héllo — wörld\n", false},
		{"empty", "", false},
		{"nul byte", "\x89PNG\r\n\x1a\n\x00\x00", true},
		{"trailing nul", "text\x00", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBinaryContent(tc.content); got != tc.want {
				t.Errorf("IsBinaryContent(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func keys(m map[string]types.FileEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── Branch deletion ────────────────────────────────────

// branchExists reports whether a local branch is still present.
func branchExists(t *testing.T, name string) bool {
	t.Helper()
	return exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/"+name).Run() == nil
}

func TestDeleteBranchForceSentinel(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "init")

	// A branch holding a commit no other branch contains: `-d` must refuse,
	// and the refusal has to be recognizable so the TUI can offer `-D`.
	runGit(t, "checkout", "-q", "-b", "feat")
	write(t, "b.txt", "b\n")
	commitAll(t, "feature work")
	runGit(t, "checkout", "-q", "main")

	err := DeleteBranch("feat", false)
	if err == nil {
		t.Fatal("DeleteBranch(feat, force=false) succeeded on an unmerged branch")
	}
	if !errors.Is(err, ErrBranchNotMerged) {
		t.Fatalf("err = %v; want it to wrap ErrBranchNotMerged", err)
	}
	if !strings.Contains(err.Error(), "not fully merged") {
		t.Errorf("err = %q; want git's own text preserved for display", err)
	}
	if !branchExists(t, "feat") {
		t.Fatal("the safe delete removed the branch anyway")
	}

	if err := DeleteBranch("feat", true); err != nil {
		t.Fatalf("DeleteBranch(feat, force=true): %v", err)
	}
	if branchExists(t, "feat") {
		t.Error("force delete left the branch behind")
	}

	// Any other failure must NOT be mistaken for "not fully merged" — that
	// would open the force-delete prompt for a branch that never existed.
	err = DeleteBranch("no-such-branch", false)
	if err == nil {
		t.Fatal("deleting a missing branch succeeded")
	}
	if errors.Is(err, ErrBranchNotMerged) {
		t.Errorf("unrelated failure classified as unmerged: %v", err)
	}

	// A merged branch still deletes safely, without force.
	runGit(t, "checkout", "-q", "-b", "merged-branch")
	runGit(t, "checkout", "-q", "main")
	if err := DeleteBranch("merged-branch", false); err != nil {
		t.Errorf("safe delete of a merged branch failed: %v", err)
	}
}

// ── Branch name validation ─────────────────────────────

func TestCheckRefFormatBranch(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"feature", true},
		{"feat/login", true},
		{"release-1.2.0", true},
		{"my new feature", false},
		{"bad:name", false},
		{"a..b", false},
		{"trailing/", false},
		{"-leading-dash", false},
		{".dotfile", false},
		{"has@{brace}", false},
		{"", false},
		{"   ", false},
	}
	for _, c := range cases {
		err := CheckRefFormatBranch(c.name)
		if c.ok && err != nil {
			t.Errorf("CheckRefFormatBranch(%q) = %v, want nil", c.name, err)
		}
		if !c.ok {
			if err == nil {
				t.Errorf("CheckRefFormatBranch(%q) = nil, want a rejection", c.name)
				continue
			}
			if strings.Contains(err.Error(), "\n") {
				t.Errorf("CheckRefFormatBranch(%q) leaked git's hint lines: %q", c.name, err)
			}
		}
	}
}

// ── Network timeouts ───────────────────────────────────

func TestRunNetworkTimeoutKillsAndReportsFriendly(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	start := time.Now()
	out, err := runNetworkTimeout(100*time.Millisecond, "sleep", "30")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNetworkTimeout) {
		t.Fatalf("err = %v (output %q), want ErrNetworkTimeout", err, out)
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %s — the deadline did not kill the child process", elapsed)
	}
	if !strings.Contains(err.Error(), "check your connection") {
		t.Errorf("timeout error = %q, want actionable text", err)
	}
}

// The force-quit path cancels the shared context; an in-flight `gh repo
// create` has to die with it instead of going on to create the repository.
func TestCancellationKillsAnInFlightNetworkCommand(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not available")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runNetworkCtx(ctx, time.Minute, "sleep", "30")
		done <- err
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrNetworkCancelled) {
			t.Fatalf("err = %v, want ErrNetworkCancelled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling the context did not kill the child process")
	}
}

func TestNetFailPassesSentinelsThroughAndPrefixesTheRest(t *testing.T) {
	if got := netFail("fetch", []byte("noise"), ErrNetworkTimeout); !errors.Is(got, ErrNetworkTimeout) {
		t.Errorf("netFail wrapped the timeout sentinel: %v", got)
	}
	if got := netFail("fetch", nil, ErrNetworkCancelled); !errors.Is(got, ErrNetworkCancelled) {
		t.Errorf("netFail wrapped the cancel sentinel: %v", got)
	}
	got := netFail("fetch", []byte("  fatal: bad remote\n"), errors.New("exit status 128"))
	if got.Error() != "fetch: fatal: bad remote" {
		t.Errorf("netFail(...) = %q, want %q", got, "fetch: fatal: bad remote")
	}
}

// Fetch has to be plumbed through runNetwork/netFail: a fast, ordinary
// failure must still surface git's own text rather than a timeout.
func TestFetchSurfacesOrdinaryFailures(t *testing.T) {
	scratchRepo(t)
	runGit(t, "remote", "add", "origin", filepath.Join(t.TempDir(), "definitely-not-a-repo"))
	err := Fetch()
	if err == nil {
		t.Fatal("Fetch() against a nonexistent remote succeeded")
	}
	if errors.Is(err, ErrNetworkTimeout) {
		t.Fatalf("an immediate failure was reported as a timeout: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "fetch:") {
		t.Errorf("Fetch() error = %q, want the 'fetch:' prefix", err)
	}
}

// ── Incoming commits ───────────────────────────────────

func TestCountIncomingCommits(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "init")
	runGit(t, "checkout", "-q", "-b", "ahead")
	for _, s := range []string{"one", "two", "three"} {
		write(t, s+".txt", s+"\n")
		commitAll(t, s)
	}
	runGit(t, "checkout", "-q", "main")

	if got := CountIncomingCommits("main", "ahead"); got != 3 {
		t.Errorf("CountIncomingCommits(main, ahead) = %d, want 3", got)
	}
	if got := CountIncomingCommits("ahead", "main"); got != 0 {
		t.Errorf("CountIncomingCommits(ahead, main) = %d, want 0", got)
	}
	if got := CountIncomingCommits("main", "no-such-ref"); got != 0 {
		t.Errorf("CountIncomingCommits with a missing ref = %d, want 0", got)
	}
	// The count is the whole set; GetIncomingCommits only samples it.
	if sample := GetIncomingCommits("main", "ahead", 2); len(sample) != 2 {
		t.Errorf("GetIncomingCommits(limit 2) returned %d subjects", len(sample))
	}
}

// ── Unrelated histories ────────────────────────────────

// seedRemote builds a bare repo with one root commit on the named branch and
// returns its path. Everything stays on local disk — no network.
func seedRemote(t *testing.T, branch string) string {
	t.Helper()
	remote := t.TempDir()
	runGit(t, "init", "-q", "--bare", remote)

	work := t.TempDir()
	runGit(t, "init", "-q", "-b", branch, work)
	runGit(t, "-C", work, "config", "user.name", "git-assist test")
	runGit(t, "-C", work, "config", "user.email", "test@example.invalid")
	runGit(t, "-C", work, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "-C", work, "add", "-A")
	runGit(t, "-C", work, "commit", "-q", "-m", "Initial commit")
	runGit(t, "-C", work, "push", "-q", remote, branch+":"+branch)
	return remote
}

func TestUnrelatedHistoriesDetectsBothShapes(t *testing.T) {
	scratchRepo(t)
	remote := seedRemote(t, "main")
	runGit(t, "remote", "add", "origin", remote)
	if err := Fetch(); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Shape 1: local HEAD unborn, remote already has commits. The first
	// local commit will become a second root.
	ref, unrelated := UnrelatedHistories()
	if ref != "origin/main" {
		t.Fatalf("compared against %q, want origin/main", ref)
	}
	if !unrelated {
		t.Error("an unborn local HEAD against a non-empty remote was not flagged")
	}

	// Shape 2: that root commit now exists and shares nothing with origin.
	write(t, "local.txt", "local\n")
	commitAll(t, "local root")
	if _, unrelated = UnrelatedHistories(); !unrelated {
		t.Error("two unrelated roots were not flagged")
	}

	// Related histories must not be flagged.
	runGit(t, "reset", "-q", "--hard", "origin/main")
	write(t, "local.txt", "local\n")
	commitAll(t, "descends from origin")
	if _, unrelated = UnrelatedHistories(); unrelated {
		t.Error("a branch descending from origin/main was flagged as unrelated")
	}
}

func TestUnrelatedHistoriesIgnoresAnEmptyRemote(t *testing.T) {
	scratchRepo(t)
	empty := t.TempDir()
	runGit(t, "init", "-q", "--bare", empty)
	runGit(t, "remote", "add", "origin", empty)
	if err := Fetch(); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	write(t, "a.txt", "a\n")
	commitAll(t, "init")

	if ref, unrelated := UnrelatedHistories(); unrelated {
		t.Errorf("an empty remote was flagged as unrelated (ref %q)", ref)
	}
}

func TestRemoteDefaultBranchFallsBackToAnyOriginRef(t *testing.T) {
	scratchRepo(t)
	runGit(t, "remote", "add", "origin", seedRemote(t, "trunk"))
	if err := Fetch(); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := RemoteDefaultBranch(); got != "origin/trunk" {
		t.Errorf("RemoteDefaultBranch() = %q, want origin/trunk", got)
	}
}
