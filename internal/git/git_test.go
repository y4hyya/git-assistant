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
// the duration of the test. The host's global/system git config is replaced so
// results don't depend on the developer's machine (autocrlf, gpgsign, hooks,
// templates, aliases) — see isolatedGitConfig for the one setting it keeps.
func scratchRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", isolatedGitConfig(t))
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
		{
			// All seven unmerged codes, and every one of them used to be
			// swallowed by an arm below: UU read as modified, AA/AU as added,
			// DD/DU/UD as deleted. A file full of conflict markers then reached
			// the commit wizard as an ordinary "M".
			name: "the seven unmerged codes are their own status",
			data: "UU both.txt\x00AA added.txt\x00DD gone.txt\x00AU we-added.txt\x00" +
				"UA they-added.txt\x00DU we-deleted.txt\x00UD they-deleted.txt\x00",
			want: []types.FileEntry{
				{Path: "both.txt", Status: types.StatusConflicted},
				{Path: "added.txt", Status: types.StatusConflicted},
				{Path: "gone.txt", Status: types.StatusConflicted},
				{Path: "we-added.txt", Status: types.StatusConflicted},
				{Path: "they-added.txt", Status: types.StatusConflicted},
				{Path: "we-deleted.txt", Status: types.StatusConflicted},
				{Path: "they-deleted.txt", Status: types.StatusConflicted},
			},
		},
		{
			// Ordinary staged/unstaged combinations keep their old meaning —
			// "U" is what makes a code unmerged, not the presence of A or D.
			name: "two-sided ordinary codes are unaffected",
			data: "AM added.txt\x00MD deleted.txt\x00MM modified.txt\x00",
			want: []types.FileEntry{
				{Path: "added.txt", Status: types.StatusAdded},
				{Path: "deleted.txt", Status: types.StatusDeleted},
				{Path: "modified.txt", Status: types.StatusModified},
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

// The state no screen could see: a conflicted stash pop leaves "UU" entries and
// NO MERGE_HEAD, so nothing keyed on a merge being in progress knows anything is
// wrong. GetStatus is the only thing that ever looks at those files, and while
// it called them "M" the wizard committed the raw markers under an innocent
// subject — plain `git commit` refuses ("you have unmerged files"), but the
// wizard's reset → add → commit sequence steps around git's own guard.
func TestGetStatusReportsAStashPopConflictAsConflicted(t *testing.T) {
	scratchRepo(t)
	write(t, "conflict.txt", "l1\nl2\nl3\n")
	commitAll(t, "seed")
	write(t, "conflict.txt", "l1\nSTASHED\nl3\n")
	runGit(t, "stash", "push", "-q")
	write(t, "conflict.txt", "l1\nCOMMITTED\nl3\n")
	commitAll(t, "conflicting change")

	if err := StashPopRef("stash@{0}"); err == nil {
		t.Fatal("the fixture pop did not conflict")
	}
	if MergeInProgress() {
		t.Fatal("MERGE_HEAD exists — this is not the unguarded state being tested")
	}

	entry, ok := statusByPath(t)["conflict.txt"]
	if !ok {
		t.Fatal("GetStatus did not report the conflicted file at all")
	}
	if entry.Status != types.StatusConflicted {
		t.Errorf("status = %v (symbol %q), want StatusConflicted — the wizard would list it as an ordinary edit",
			entry.Status, entry.Status.Symbol())
	}
	if entry.Status.Symbol() != "U" {
		t.Errorf("symbol = %q, want %q", entry.Status.Symbol(), "U")
	}
	// And the file really is the hazard the status now advertises.
	if got := readFile(t, "conflict.txt"); !strings.Contains(got, "<<<<<<<") {
		t.Fatalf("no conflict markers in the fixture file:\n%s", got)
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

// The deadline has to fire even when the command's own CHILDREN outlive it.
//
// git never talks to a remote itself: an https fetch runs git-remote-https, an
// ssh one runs ssh, and both inherit the stdout/stderr pipes CombinedOutput is
// reading. Killing git alone leaves the transport holding the write end, and
// the read blocks until IT exits — for the hung-ssh case this timeout exists
// for, that is never. `sh -c 'sleep 30 & exec sleep 30'` is the same shape in
// one line: the backgrounded sleep keeps the pipe open after the foreground
// process is killed.
func TestRunNetworkTimeoutKillsChildrenHoldingThePipes(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	start := time.Now()
	out, err := runNetworkTimeout(200*time.Millisecond, "sh", "-c", "sleep 30 & exec sleep 30")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNetworkTimeout) {
		t.Fatalf("err = %v (output %q), want ErrNetworkTimeout", err, out)
	}
	// Well under the grandchild's 30s: the point is that the wait is bounded by
	// the deadline, not by whatever the transport decides to do next.
	if elapsed > 10*time.Second {
		t.Errorf("took %s — the read waited for the child that held the pipes", elapsed)
	}
}

// The same deadline against the transport git really uses: GIT_SSH_COMMAND
// points at something that hangs, and git spawns it for an ssh:// remote.
//
// Traced in a scratch repo: killing git alone DID return here — git does not
// hand its ssh child the pipe CombinedOutput reads — but the ssh process
// SURVIVED, orphaned, still holding the repository a fetch writes packs and
// refs into, seconds after the user was told the operation had timed out. The
// group kill is what collects it, and that is what this pins.
func TestNetworkTimeoutKillsAHungSSHTransport(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	scratchRepo(t)
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "ssh-survived")
	hang := filepath.Join(tmp, "hang-ssh")
	script := "#!/bin/sh\n{ sleep 1; : > '" + marker + "'; } &\nexec sleep 30\n"
	if err := os.WriteFile(hang, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("GIT_SSH_COMMAND", hang)
	runGit(t, "remote", "add", "origin", "ssh://git@example.invalid/repo.git")

	start := time.Now()
	out, err := runNetworkTimeout(500*time.Millisecond, "git", "fetch", "--quiet", "origin")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNetworkTimeout) {
		t.Fatalf("err = %v (output %q), want ErrNetworkTimeout", err, out)
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %s — the deadline did not bound the wait", elapsed)
	}
	// Past the transport's own sleep: with the group kill it never got there.
	time.Sleep(1500 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the ssh transport outlived the timeout — it is still writing to the repository")
	}
}

// …and the transport child must actually DIE, not merely be waited on: an
// orphaned git-remote-https goes on writing to the repository the user was just
// told the operation had timed out on. The grandchild here leaves a marker file
// behind if it survives its parent's kill.
func TestRunNetworkTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	marker := filepath.Join(t.TempDir(), "grandchild-survived")
	script := "{ sleep 1; : > '" + marker + "'; } & exec sleep 30"

	if _, err := runNetworkTimeout(200*time.Millisecond, "sh", "-c", script); !errors.Is(err, ErrNetworkTimeout) {
		t.Fatalf("err = %v, want ErrNetworkTimeout", err)
	}
	// Past the grandchild's own sleep: if the group kill worked it never got
	// there. (A slow machine can only make this test pass, never fail wrongly.)
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the transport child outlived the deadline — the kill did not reach the process group")
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

// ── Push ───────────────────────────────────────────────

// bareOrigin creates a bare repository, wires it up as `origin`, and returns
// its path. Real refs, real rejections, no network.
func bareOrigin(t *testing.T) string {
	t.Helper()
	origin := t.TempDir()
	runGit(t, "init", "-q", "--bare", origin)
	runGit(t, "remote", "add", "origin", origin)
	return origin
}

// rev resolves a ref in the repo at dir ("" for the current one).
func rev(t *testing.T, dir, ref string) string {
	t.Helper()
	if dir == "" {
		return strings.TrimSpace(runGit(t, "rev-parse", ref))
	}
	return strings.TrimSpace(runGit(t, "-C", dir, "rev-parse", ref))
}

// The first push has to set tracking up (-u), or every later ahead/behind
// reading on the branch answers "no upstream".
func TestPushPublishesAndTracksTheCurrentBranch(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "init")
	origin := bareOrigin(t)

	if HasUpstream("main") {
		t.Fatal("a never-pushed branch reports an upstream")
	}
	if err := Push("main"); err != nil {
		t.Fatalf("Push(main): %v", err)
	}
	if !HasUpstream("main") {
		t.Error("the first push did not set tracking — -u was not applied")
	}
	if got, want := rev(t, origin, "main"), rev(t, "", "main"); got != want {
		t.Errorf("origin/main = %s, want %s", got, want)
	}

	// A second push has nothing to set up. It must still work — and must not
	// re-declare the upstream it already has.
	write(t, "a.txt", "aa\n")
	commitAll(t, "second")
	if err := Push("main"); err != nil {
		t.Fatalf("second Push(main): %v", err)
	}
	if got, want := rev(t, origin, "main"), rev(t, "", "main"); got != want {
		t.Errorf("after the second push origin/main = %s, want %s", got, want)
	}
}

// The landmine the old two-argument Push carried: with a target that was not
// the current branch it pushed `HEAD:<target>`, i.e. the commits you are
// standing on onto somebody else's branch. Push takes one branch now, and that
// branch is both the source and the destination.
func TestPushOnlyEverMovesItsOwnBranch(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "init")
	origin := bareOrigin(t)
	if err := Push("main"); err != nil {
		t.Fatalf("Push(main): %v", err)
	}
	mainBefore := rev(t, origin, "main")

	runGit(t, "checkout", "-q", "-b", "feat")
	write(t, "b.txt", "b\n")
	commitAll(t, "feat work")
	if err := Push("feat"); err != nil {
		t.Fatalf("Push(feat): %v", err)
	}

	if got, want := rev(t, origin, "feat"), rev(t, "", "feat"); got != want {
		t.Errorf("origin/feat = %s, want %s", got, want)
	}
	if got := rev(t, origin, "main"); got != mainBefore {
		t.Errorf("pushing feat moved origin/main to %s (was %s)", got, mainBefore)
	}
}

func TestPushRejectsAnEmptyBranch(t *testing.T) {
	scratchRepo(t)
	if err := Push(""); err == nil {
		t.Error("Push(\"\") succeeded — git would have pushed the default refspec")
	}
	if err := PushForceWithLease("", ""); err == nil {
		t.Error("PushForceWithLease(\"\") succeeded")
	}
}

// The amend loop, end to end: rewrite a commit origin already has, watch the
// plain push bounce, then replace origin's copy with the lease held.
func TestPushForceWithLeaseReplacesARewrittenCommit(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "original")
	origin := bareOrigin(t)
	if err := Push("main"); err != nil {
		t.Fatalf("Push(main): %v", err)
	}

	lease := RemoteTrackingSHA("main")
	if lease == "" {
		t.Fatal("RemoteTrackingSHA(main) is empty right after a push")
	}
	write(t, "a.txt", "amended\n")
	runGit(t, "add", "-A")
	runGit(t, "commit", "-q", "--amend", "-m", "rewritten")

	if err := Push("main"); err == nil {
		t.Fatal("a plain push of a rewritten commit succeeded — the remote was overwritten")
	}
	if err := PushForceWithLease("main", lease); err != nil {
		t.Fatalf("PushForceWithLease(main): %v", err)
	}
	if got, want := rev(t, origin, "main"), rev(t, "", "main"); got != want {
		t.Errorf("origin/main = %s, want %s", got, want)
	}
	if subj := strings.TrimSpace(runGit(t, "-C", origin, "log", "-1", "--format=%s", "main")); subj != "rewritten" {
		t.Errorf("origin's subject = %q, want %q", subj, "rewritten")
	}
}

// The lease is the whole point: if someone else pushed since we were shown what
// we would be replacing, git must refuse and leave their commit alone.
//
// The fetch in the middle is not incidental — it is the reason the lease is
// pinned to an explicit SHA. git-assist fetches at startup and every 30
// seconds, and a bare --force-with-lease leases against the remote-tracking ref
// that fetch updates: with the default lease this exact sequence DELETES their
// commit, while the screen promises it cannot.
func TestPushForceWithLeaseRefusesWhenOriginMoved(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "original")
	origin := bareOrigin(t)
	if err := Push("main"); err != nil {
		t.Fatalf("Push(main): %v", err)
	}
	// What the user is shown, and therefore what they consent to replace.
	lease := RemoteTrackingSHA("main")

	// Someone else clones, commits and pushes.
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "clone", "-q", origin, other)
	runGit(t, "-C", other, "config", "user.name", "someone else")
	runGit(t, "-C", other, "config", "user.email", "else@example.invalid")
	if err := os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "-C", other, "add", "-A")
	runGit(t, "-C", other, "commit", "-q", "-m", "their work")
	runGit(t, "-C", other, "push", "-q", "origin", "main")
	theirs := rev(t, origin, "main")

	// Meanwhile we rewrite our copy of the shared commit — and the background
	// fetch lands, moving the ref a default lease would have trusted.
	write(t, "a.txt", "amended\n")
	runGit(t, "add", "-A")
	runGit(t, "commit", "-q", "--amend", "-m", "rewritten")
	if err := Fetch(); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	err := PushForceWithLease("main", lease)
	if err == nil {
		t.Fatal("force-with-lease overwrote a commit it had never seen")
	}
	if !strings.Contains(err.Error(), "stale info") {
		t.Errorf("error = %q, want git's 'stale info' wording (the UI hint keys off it)", err)
	}
	if got := rev(t, origin, "main"); got != theirs {
		t.Errorf("origin/main = %s, want %s — their commit was destroyed", got, theirs)
	}
}

// Amending a commit in a fresh clone is a legitimate rewrite: nothing else has
// moved, and the lease must not stand in the way. (git's --force-if-includes,
// the other candidate fix for the fetch hole above, rejects exactly this.)
func TestPushForceWithLeaseAllowsARewriteInAFreshClone(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "original")
	origin := bareOrigin(t)
	if err := Push("main"); err != nil {
		t.Fatalf("Push(main): %v", err)
	}

	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, "clone", "-q", origin, clone)
	t.Chdir(clone)
	runGit(t, "config", "user.name", "git-assist test")
	runGit(t, "config", "user.email", "test@example.invalid")
	runGit(t, "config", "commit.gpgsign", "false")

	lease := RemoteTrackingSHA("main")
	runGit(t, "commit", "-q", "--amend", "-m", "rewritten in a clone")
	if err := PushForceWithLease("main", lease); err != nil {
		t.Fatalf("PushForceWithLease in a fresh clone: %v", err)
	}
	if subj := strings.TrimSpace(runGit(t, "-C", origin, "log", "-1", "--format=%s", "main")); subj != "rewritten in a clone" {
		t.Errorf("origin's subject = %q, want the rewrite", subj)
	}
}

// ── Outgoing commits ───────────────────────────────────

func TestOutgoingCommits(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "one")
	bareOrigin(t)
	if err := Push("main"); err != nil {
		t.Fatalf("Push(main): %v", err)
	}

	if got := CountOutgoingCommits("main"); got != 0 {
		t.Errorf("CountOutgoingCommits right after a push = %d, want 0", got)
	}
	for _, s := range []string{"two", "three"} {
		write(t, s+".txt", s+"\n")
		commitAll(t, s)
	}
	if got := CountOutgoingCommits("main"); got != 2 {
		t.Errorf("CountOutgoingCommits(main) = %d, want 2", got)
	}
	// Newest first, and the sample is capped — the caller pairs it with the
	// count so the screen can say what it is not showing.
	sample := GetOutgoingCommits("main", 1)
	if len(sample) != 1 || sample[0] != "three" {
		t.Errorf("GetOutgoingCommits(main, 1) = %v, want [three]", sample)
	}
	// A branch origin has never heard of: nothing to compare, and no error to
	// leak into the screen.
	runGit(t, "checkout", "-q", "-b", "unpublished")
	if got := CountOutgoingCommits("unpublished"); got != 0 {
		t.Errorf("CountOutgoingCommits on an unpublished branch = %d, want 0", got)
	}
	if got := GetOutgoingCommits("unpublished", 5); got != nil {
		t.Errorf("GetOutgoingCommits on an unpublished branch = %v, want nil", got)
	}
	if got := CountOutgoingCommits(""); got != 0 {
		t.Errorf("CountOutgoingCommits(\"\") = %d, want 0", got)
	}
	if HasUpstream("unpublished") {
		t.Error("a branch created locally reports an upstream")
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

// ── "behind main" measures the ref the sync action merges ──

// mainFixture builds: origin with main @ seed, a local `feat` branch at seed,
// and returns with `feat` checked out. Callers then move local main and/or
// origin/main apart to create the divergence they want to test.
func mainFixture(t *testing.T) {
	t.Helper()
	scratchRepo(t)
	remote := t.TempDir()
	runGit(t, "init", "-q", "--bare", remote)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "chore: seed")
	runGit(t, "remote", "add", "origin", remote)
	runGit(t, "push", "-q", "-u", "origin", "main")
	runGit(t, "branch", "feat")
}

// The badge counted local main while `s` merged origin/main. With origin ahead
// of a stale local main the badge read 0, so the shortcut was hidden — while
// the sync dialog, which measures origin/main, was offering the very same merge.
func TestBehindMainCountsAgainstOriginNotAStaleLocalMain(t *testing.T) {
	mainFixture(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "feat: a")
	runGit(t, "push", "-q", "origin", "main")
	// Local main falls behind what it just pushed; origin/main keeps the commit.
	runGit(t, "reset", "-q", "--hard", "HEAD~1")
	runGit(t, "checkout", "-q", "feat")

	if got := CountIncomingCommits("feat", "main"); got != 0 {
		t.Fatalf("fixture wrong: feat is %d behind LOCAL main, want 0", got)
	}
	if got := MainSyncRef(); got != "origin/main" {
		t.Fatalf("MainSyncRef() = %q, want origin/main", got)
	}
	if got := GetBehindMain("feat"); got != 1 {
		t.Errorf("GetBehindMain(feat) = %d, want 1 — the badge is blind to origin/main", got)
	}
}

// The other direction: an unpushed commit on local main used to make every
// branch look "behind main" forever, because `s` merged origin/main and got
// "Already up to date" while the badge kept measuring the local ref.
func TestBehindMainIgnoresUnpushedLocalMainCommits(t *testing.T) {
	mainFixture(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "feat: unpushed hotfix")
	runGit(t, "checkout", "-q", "feat")

	if got := CountIncomingCommits("feat", "main"); got != 1 {
		t.Fatalf("fixture wrong: feat is %d behind LOCAL main, want 1", got)
	}
	if got := GetBehindMain("feat"); got != 0 {
		t.Errorf("GetBehindMain(feat) = %d, want 0 — syncing with origin/main can never clear it", got)
	}
}

// Remoteless repositories still get a badge, measured (and merged) against the
// local branch.
func TestBehindMainFallsBackToLocalMainWithoutARemote(t *testing.T) {
	scratchRepo(t)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "chore: seed")
	runGit(t, "branch", "feat")
	write(t, "a.txt", "a\n")
	commitAll(t, "feat: a")
	runGit(t, "checkout", "-q", "feat")

	if got := MainSyncRef(); got != "main" {
		t.Fatalf("MainSyncRef() = %q, want main", got)
	}
	if got := GetBehindMain("feat"); got != 1 {
		t.Errorf("GetBehindMain(feat) = %d, want 1", got)
	}
	if got := GetBehindMain("main"); got != 0 {
		t.Errorf("GetBehindMain(main) = %d, want 0 — main is not behind itself", got)
	}
}

// A fresh clone of a master-based remote has no local branch to read the
// spelling off. Defaulting to "main" made the dialog label the branch wrongly
// and ask git for an origin/main that does not exist.
func TestResolveMainBranchTakesOriginsSpelling(t *testing.T) {
	scratchRepo(t)
	runGit(t, "remote", "add", "origin", seedRemote(t, "master"))
	if err := Fetch(); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// Leave the local branch as the scratch repo's unborn "main".
	if got := ResolveMainBranch(); got != "master" {
		t.Errorf("ResolveMainBranch() = %q, want master", got)
	}
	if got := MainSyncRef(); got != "origin/master" {
		t.Errorf("MainSyncRef() = %q, want origin/master", got)
	}
}

// ── Config: unset vs empty, and git's bool spellings ───

// Clearing a config field has to UNSET the key. Writing "" instead leaves a
// key that exists and is empty: a local user.name = "" shadows the global one
// and `git commit` dies with "empty ident name", while the editor happily
// renders the key as set-but-blank.
func TestUnsetConfigValueRemovesTheKeyInsteadOfEmptyingIt(t *testing.T) {
	scratchRepo(t)

	if err := SetConfigValue("user.name", "Someone", false); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	if v, set, _ := GetConfigValue("user.name", false); !set || v != "Someone" {
		t.Fatalf("setup: value = %q set = %v", v, set)
	}

	if err := UnsetConfigValue("user.name", false); err != nil {
		t.Fatalf("UnsetConfigValue: %v", err)
	}
	if v, set, _ := GetConfigValue("user.name", false); set {
		t.Errorf("key survived the unset: value = %q set = %v", v, set)
	}

	// Unsetting what is already unset is the desired end state, not an error
	// (git exits 5 for it).
	if err := UnsetConfigValue("user.name", false); err != nil {
		t.Errorf("second UnsetConfigValue: %v", err)
	}

	// The contrast this exists to avoid.
	if err := SetConfigValue("user.name", "", false); err != nil {
		t.Fatalf("SetConfigValue empty: %v", err)
	}
	if _, set, _ := GetConfigValue("user.name", false); !set {
		t.Error("writing an empty value should leave the key SET — the premise of the fix")
	}
}

// git reads true/yes/on/1 (any case) as true. The config editor compared the
// raw string to "true", so a repo with commit.gpgsign = 1 displayed "off"
// while git was signing every commit.
func TestConfigBoolUnderstandsGitSpellings(t *testing.T) {
	on := []string{"true", "TRUE", "True", "yes", "YES", "on", "On", "1", " true "}
	off := []string{"false", "no", "off", "0", "", "  ", "maybe", "2"}
	for _, v := range on {
		if !ConfigBool(v) {
			t.Errorf("ConfigBool(%q) = false, want true", v)
		}
	}
	for _, v := range off {
		if ConfigBool(v) {
			t.Errorf("ConfigBool(%q) = true, want false", v)
		}
	}
}

// ── Merge: "Already up to date" is not a merge ─────────

func TestMergeBranchReportsAlreadyUpToDate(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")
	runGit(t, "branch", "feature")

	// feature is an ancestor of main: git has nothing to merge.
	upToDate, err := MergeBranch("feature")
	if err != nil {
		t.Fatalf("MergeBranch: %v", err)
	}
	if !upToDate {
		t.Error("a merge with nothing to merge was reported as a real merge")
	}

	// Now give feature a commit of its own — a genuine merge.
	runGit(t, "checkout", "-q", "feature")
	write(t, "b.txt", "b\n")
	commitAll(t, "feature work")
	runGit(t, "checkout", "-q", "main")

	upToDate, err = MergeBranch("feature")
	if err != nil {
		t.Fatalf("MergeBranch (real): %v", err)
	}
	if upToDate {
		t.Error("a real merge was reported as up to date")
	}
}

func TestMergeWasNoOpMatchesBothSpellings(t *testing.T) {
	// git renamed the message in 2.29; both spellings are in the wild.
	for _, out := range []string{"Already up to date.\n", "Already up-to-date.\n", "ALREADY UP TO DATE.\n"} {
		if !mergeWasNoOp([]byte(out)) {
			t.Errorf("mergeWasNoOp(%q) = false, want true", out)
		}
	}
	if mergeWasNoOp([]byte("Merge made by the 'ort' strategy.\n")) {
		t.Error("a real merge was classified as a no-op")
	}
}

// ── Discarding a file's changes ────────────────────────

// fileExists is the "is it on disk at all" check the discard tests need —
// os.Stat follows symlinks, and a discarded symlink is gone either way.
func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return err == nil
}

// Each status routes to a different operation, and getting one wrong destroys
// something the confirmation did not warn about: restoring an untracked file
// fails outright, deleting a modified one throws away the committed version too.
func TestDiscardFileRoutesByStatus(t *testing.T) {
	scratchRepo(t)
	write(t, "modified.txt", "committed\n")
	write(t, "deleted.txt", "committed\n")
	write(t, "renamed.txt", "committed\n")
	commitAll(t, "seed")

	write(t, "modified.txt", "edited\n")
	if err := os.Remove("deleted.txt"); err != nil {
		t.Fatal(err)
	}
	runGit(t, "mv", "renamed.txt", "moved.txt")
	write(t, "untracked.txt", "brand new\n")
	write(t, "staged-new.txt", "brand new\n")
	runGit(t, "add", "staged-new.txt")
	write(t, "junk/inner.txt", "in an untracked folder\n")

	before := statusByPath(t)
	for _, want := range []struct {
		path   string
		status types.FileStatus
	}{
		{"modified.txt", types.StatusModified},
		{"deleted.txt", types.StatusDeleted},
		{"moved.txt", types.StatusRenamed},
		{"untracked.txt", types.StatusUntracked},
		{"staged-new.txt", types.StatusAdded},
		{"junk/inner.txt", types.StatusUntracked},
	} {
		if got, ok := before[want.path]; !ok || got.Status != want.status {
			t.Fatalf("fixture: %s is %v (present=%v), want %v", want.path, got.Status, ok, want.status)
		}
	}

	for _, path := range []string{"modified.txt", "deleted.txt", "moved.txt",
		"untracked.txt", "staged-new.txt", "junk/inner.txt"} {
		if err := DiscardFile(before[path]); err != nil {
			t.Fatalf("DiscardFile(%s): %v", path, err)
		}
	}

	// Modified: back to the committed content, and unstaged with it.
	if got := readFileString(t, "modified.txt"); got != "committed\n" {
		t.Errorf("modified.txt = %q, want the committed content back", got)
	}
	// Deleted: x resurrects, it does not delete harder.
	if got := readFileString(t, "deleted.txt"); got != "committed\n" {
		t.Errorf("deleted.txt = %q, want the file restored", got)
	}
	// Renamed: BOTH halves, or the working tree keeps half a rename.
	if !fileExists(t, "renamed.txt") {
		t.Error("renamed.txt did not come back — the rename was only half undone")
	}
	if fileExists(t, "moved.txt") {
		t.Error("moved.txt still exists — the rename was only half undone")
	}
	// Untracked and staged-new: nothing to restore them from, so they go.
	for _, path := range []string{"untracked.txt", "staged-new.txt", "junk/inner.txt"} {
		if fileExists(t, path) {
			t.Errorf("%s still exists — an uncommitted file's discard is a delete", path)
		}
	}

	// Nothing is left over: a clean tree is the whole point.
	if entries, err := GetStatus(); err != nil {
		t.Fatalf("GetStatus: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("working tree still dirty after discarding everything: %+v", entries)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestDiscardFileRejectsAnEmptyPath(t *testing.T) {
	scratchRepo(t)
	if err := DiscardFile(types.FileEntry{}); err == nil {
		t.Error("discarding a pathless entry should fail rather than run `git restore --`")
	}
}

// ── Revert ─────────────────────────────────────────────

func TestRevertAddsACommitInsteadOfRewriting(t *testing.T) {
	scratchRepo(t)
	write(t, "f.txt", "one\n")
	commitAll(t, "first")
	write(t, "f.txt", "two\n")
	commitAll(t, "second")
	second := strings.TrimSpace(runGit(t, "rev-parse", "HEAD"))

	if err := Revert(second); err != nil {
		t.Fatalf("Revert: %v", err)
	}

	if got := readFileString(t, "f.txt"); got != "one\n" {
		t.Errorf("f.txt = %q, want the pre-commit content", got)
	}
	// History GREW: the reverted commit is still there, which is what makes
	// this the push-safe path.
	if got := commitCount(t); got != "3" {
		t.Errorf("commit count = %s, want 3 (revert adds, never replaces)", got)
	}
	if !strings.Contains(runGit(t, "log", "--format=%s"), "Revert \"second\"") {
		t.Errorf("the revert commit does not name what it undid:\n%s",
			runGit(t, "log", "--format=%s"))
	}
	if strings.TrimSpace(runGit(t, "rev-parse", "HEAD~1")) != second {
		t.Error("the reverted commit is no longer HEAD~1 — this rewrote history")
	}
}

func TestRevertConflictAbortsBackToWhereItStarted(t *testing.T) {
	scratchRepo(t)
	write(t, "f.txt", "a\nb\nc\n")
	commitAll(t, "first")
	write(t, "f.txt", "a\nB\nc\n")
	commitAll(t, "second")
	second := strings.TrimSpace(runGit(t, "rev-parse", "HEAD"))
	write(t, "f.txt", "a\nZ\nc\n")
	commitAll(t, "third")
	head := strings.TrimSpace(runGit(t, "rev-parse", "HEAD"))

	err := Revert(second)
	if err == nil {
		t.Fatal("reverting a commit whose lines have since moved should conflict")
	}
	if conflicts := GetConflictFiles(); len(conflicts) != 1 || conflicts[0] != "f.txt" {
		t.Fatalf("conflict files = %v, want [f.txt]", conflicts)
	}
	if err := RevertAbort(); err != nil {
		t.Fatalf("RevertAbort: %v", err)
	}

	if got := strings.TrimSpace(runGit(t, "rev-parse", "HEAD")); got != head {
		t.Errorf("HEAD = %s after the abort, want %s", got, head)
	}
	if got := readFileString(t, "f.txt"); got != "a\nZ\nc\n" {
		t.Errorf("f.txt = %q — the abort left conflict markers behind", got)
	}
	if entries, _ := GetStatus(); len(entries) != 0 {
		t.Errorf("working tree dirty after the abort: %+v", entries)
	}
}

func TestRevertRejectsAnEmptySHA(t *testing.T) {
	scratchRepo(t)
	if err := Revert("  "); err == nil {
		t.Error("Revert(\"\") should fail rather than revert HEAD by accident")
	}
}

// ── Branch rename ──────────────────────────────────────

func TestRenameBranchToMovesLocalBranches(t *testing.T) {
	scratchRepo(t)
	write(t, "f.txt", "x\n")
	commitAll(t, "seed")
	runGit(t, "branch", "feature")
	runGit(t, "branch", "taken")

	// A branch you are not standing on.
	if err := RenameBranchTo("feature", "feature-2"); err != nil {
		t.Fatalf("RenameBranchTo: %v", err)
	}
	if BranchExists("feature") || !BranchExists("feature-2") {
		t.Error("the rename did not move the branch")
	}

	// The branch you are on: the checkout follows it.
	if err := RenameBranchTo("main", "trunk"); err != nil {
		t.Fatalf("RenameBranchTo(current): %v", err)
	}
	if got, _ := GetCurrentBranch(); got != "trunk" {
		t.Errorf("current branch = %q after renaming it, want trunk", got)
	}

	// Refusals, each with something the user can act on.
	for _, tc := range []struct {
		name     string
		from, to string
		want     string
	}{
		{"existing name", "trunk", "taken", "already exists"},
		{"invalid name", "trunk", "has spaces", "not a valid branch name"},
		{"same name", "trunk", "trunk", "already called that"},
		{"empty", "trunk", "  ", "both names"},
	} {
		err := RenameBranchTo(tc.from, tc.to)
		if err == nil {
			t.Errorf("%s: rename was allowed", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
	// The refusals changed nothing.
	if !BranchExists("trunk") || !BranchExists("taken") {
		t.Error("a refused rename moved something anyway")
	}
}

// ── Detached HEAD ──────────────────────────────────────

func TestIsDetachedHead(t *testing.T) {
	scratchRepo(t)
	// Unborn: on a branch that has nothing on it yet, which is NOT detached.
	if IsDetachedHead() {
		t.Error("a fresh repo with no commits is on a branch, not detached")
	}
	write(t, "f.txt", "x\n")
	commitAll(t, "seed")
	if IsDetachedHead() {
		t.Error("IsDetachedHead is true on an ordinary branch")
	}
	if got, _ := GetCurrentBranch(); got != "main" {
		t.Errorf("current branch = %q, want main", got)
	}

	runGit(t, "checkout", "-q", "--detach", "HEAD")
	if !IsDetachedHead() {
		t.Error("IsDetachedHead is false after `git checkout --detach`")
	}
	if got, _ := GetCurrentBranch(); got != DetachedLabel {
		t.Errorf("current branch = %q while detached, want the %q label", got, DetachedLabel)
	}
	// The label is a label. Nothing may hand it to git as a ref.
	if BranchExists(DetachedLabel) {
		t.Error("DetachedLabel resolves as a branch name")
	}
}
