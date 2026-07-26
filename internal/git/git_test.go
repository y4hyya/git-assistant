package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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

func keys(m map[string]types.FileEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
