package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ── Helpers ────────────────────────────────────────────

// isolatedGitConfig writes the global config the suite runs under and returns
// its path, for GIT_CONFIG_GLOBAL.
//
// It is a real file rather than /dev/null because the suite needs two things
// that pull in opposite directions: none of the host's settings (autocrlf,
// gpgsign, hooks, templates, aliases), but init.defaultBranch PINNED. With it
// unset, `git init` falls back to git's compiled-in default — still "master"
// on a stock Linux CI runner, while this project's macOS developers get "main"
// from Xcode's git-core gitconfig, which GIT_CONFIG_SYSTEM=/dev/null does not
// suppress. Every fixture that creates a BARE repo (bareOrigin, seedRemote)
// then leaves HEAD on a branch nothing ever creates, and the clones taken from
// it come out with no checkout at all: "src refspec main does not match any",
// "You have nothing to amend". Pinning it here makes the whole suite read the
// same on both.
func isolatedGitConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(path, []byte("[init]\n\tdefaultBranch = main\n"), 0o644); err != nil {
		t.Fatalf("write test gitconfig: %v", err)
	}
	return path
}

// scratchDir is scratchRepo without the `git init` — the state the first-run
// setup functions are actually called in. Chdirs into a named subdirectory so
// CurrentDirName has something deterministic to report.
func scratchDir(t *testing.T, name string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", isolatedGitConfig(t))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	t.Chdir(dir)
	return dir
}

// fakeGH puts a recording `gh` stub at the front of PATH and returns the path
// of the file it logs its argv to (one argument per line). The real CLI is not
// installed on CI and would talk to GitHub if it were, so the only way to pin
// the argv GHCreateRepo builds is to intercept it.
func fakeGH(t *testing.T, exitCode int, stderr string) string {
	t.Helper()
	binDir := t.TempDir()
	argLog := filepath.Join(binDir, "argv")

	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&script, "printf '%%s\\n' \"$@\" >> '%s'\n", argLog)
	if stderr != "" {
		fmt.Fprintf(&script, "echo '%s' >&2\n", stderr)
	}
	fmt.Fprintf(&script, "exit %d\n", exitCode)

	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script.String()), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argLog
}

// ghArgv reads back what the fake recorded. Empty slice when it never ran.
func ghArgv(t *testing.T, argLog string) []string {
	t.Helper()
	content, err := os.ReadFile(argLog)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(content), "\n"), "\n")
}

// ── InitRepo / RenameBranch ────────────────────────────

func TestInitRepoUsesTheRequestedDefaultBranch(t *testing.T) {
	scratchDir(t, "proj")

	if IsGitRepo() {
		t.Fatal("scratch dir already reports as a repository")
	}
	if err := InitRepo("trunk"); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if !IsGitRepo() {
		t.Fatal("IsGitRepo false right after InitRepo")
	}
	head := strings.TrimSpace(runGit(t, "symbolic-ref", "HEAD"))
	if head != "refs/heads/trunk" {
		t.Fatalf("HEAD = %q, want refs/heads/trunk", head)
	}
}

// The init flow calls RenameBranch straight after InitRepo because older git
// silently ignores `init -b`. It has to work on an unborn HEAD — there is no
// commit yet when it runs.
func TestRenameBranchWorksOnAnUnbornHead(t *testing.T) {
	scratchDir(t, "proj")
	if err := InitRepo(""); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if err := RenameBranch("main"); err != nil {
		t.Fatalf("RenameBranch: %v", err)
	}
	if head := strings.TrimSpace(runGit(t, "symbolic-ref", "HEAD")); head != "refs/heads/main" {
		t.Fatalf("HEAD = %q, want refs/heads/main", head)
	}
}

// A branch name git rejects must come back as an error rather than leaving a
// half-built repository the init flow then reports as success.
func TestInitRepoSurfacesGitsRefusal(t *testing.T) {
	scratchDir(t, "proj")
	err := InitRepo("has a space")
	if err == nil {
		t.Fatal("InitRepo accepted an invalid branch name")
	}
	if !strings.Contains(err.Error(), "init failed") {
		t.Fatalf("error = %q, want it prefixed with 'init failed'", err)
	}
}

// ── Origin remote ──────────────────────────────────────

// AddOriginRemote is add-or-update: called twice it must not fail with
// "remote origin already exists" — the config editor's Remote URL field and
// the init flow both re-run it on repos that already have one.
func TestAddOriginRemoteUpdatesAnExistingOrigin(t *testing.T) {
	scratchRepo(t)

	if HasRemote() {
		t.Fatal("fresh repo reports a remote")
	}
	if GetRemoteURL() != "" {
		t.Fatalf("GetRemoteURL = %q on a repo with no origin", GetRemoteURL())
	}

	if err := AddOriginRemote("https://example.invalid/one.git"); err != nil {
		t.Fatalf("AddOriginRemote (add): %v", err)
	}
	if got := GetRemoteURL(); got != "https://example.invalid/one.git" {
		t.Fatalf("after add, GetRemoteURL = %q", got)
	}
	if !HasRemote() {
		t.Fatal("HasRemote false after adding origin")
	}

	if err := AddOriginRemote("https://example.invalid/two.git"); err != nil {
		t.Fatalf("AddOriginRemote (update): %v", err)
	}
	if got := GetRemoteURL(); got != "https://example.invalid/two.git" {
		t.Fatalf("after update, GetRemoteURL = %q, want the second URL", got)
	}
	// One origin, not two.
	if remotes := strings.Fields(runGit(t, "remote")); len(remotes) != 1 {
		t.Fatalf("remotes = %v, want exactly one", remotes)
	}
}

func TestRemoveOriginRemoteIsANoOpWithoutOne(t *testing.T) {
	scratchRepo(t)

	if err := RemoveOriginRemote(); err != nil {
		t.Fatalf("RemoveOriginRemote on a repo with no origin: %v", err)
	}

	if err := AddOriginRemote("https://example.invalid/repo.git"); err != nil {
		t.Fatalf("AddOriginRemote: %v", err)
	}
	if err := RemoveOriginRemote(); err != nil {
		t.Fatalf("RemoveOriginRemote: %v", err)
	}
	if HasRemote() || GetRemoteURL() != "" {
		t.Fatalf("origin survived removal: hasRemote=%v url=%q", HasRemote(), GetRemoteURL())
	}
}

// ── gh CLI ─────────────────────────────────────────────

// The argv is the whole contract with `gh`: --source=. is what wires origin,
// --remote=origin names it, and --push must appear only when the caller asked
// for it (pushing a repo with no commits fails). Nothing in the app can see
// this argv go wrong until a user runs it for real.
func TestGHCreateRepoArgv(t *testing.T) {
	cases := []struct {
		name    string
		private bool
		push    bool
		want    []string
	}{
		{
			name: "public-nopush",
			want: []string{"repo", "create", "acme/tool", "--public", "--source=.", "--remote=origin"},
		},
		{
			name:    "private-nopush",
			private: true,
			want:    []string{"repo", "create", "acme/tool", "--private", "--source=.", "--remote=origin"},
		},
		{
			name: "public-push",
			push: true,
			want: []string{"repo", "create", "acme/tool", "--public", "--source=.", "--remote=origin", "--push"},
		},
		{
			name:    "private-push",
			private: true,
			push:    true,
			want:    []string{"repo", "create", "acme/tool", "--private", "--source=.", "--remote=origin", "--push"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scratchRepo(t)
			argLog := fakeGH(t, 0, "")

			if err := GHCreateRepo("acme/tool", tc.private, tc.push); err != nil {
				t.Fatalf("GHCreateRepo: %v", err)
			}
			got := ghArgv(t, argLog)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("argv =\n  %v\nwant\n  %v", got, tc.want)
			}
		})
	}
}

// A gh failure carries gh's own text under a prefix the user can act on —
// netFail's non-sentinel path, which is what every real failure here takes
// (rate limit, name already taken, no permission on the org).
func TestGHCreateRepoReportsGHsOwnFailure(t *testing.T) {
	scratchRepo(t)
	fakeGH(t, 1, "GraphQL: Name already exists on this account")

	err := GHCreateRepo("taken", false, false)
	if err == nil {
		t.Fatal("GHCreateRepo returned nil on a failing gh")
	}
	if !strings.Contains(err.Error(), "gh repo create failed") {
		t.Fatalf("error = %q, want the 'gh repo create failed' prefix", err)
	}
	if !strings.Contains(err.Error(), "Name already exists") {
		t.Fatalf("error = %q, want gh's own message kept", err)
	}
}

// HasGHCLI is a PATH lookup and IsGHAuthed is `gh auth status`'s exit code —
// the init flow branches on both before it offers the GitHub option at all.
func TestGHAvailabilityFollowsPathAndExitCode(t *testing.T) {
	t.Run("authed", func(t *testing.T) {
		scratchDir(t, "proj")
		argLog := fakeGH(t, 0, "")
		if !HasGHCLI() {
			t.Fatal("HasGHCLI false with gh on PATH")
		}
		if !IsGHAuthed() {
			t.Fatal("IsGHAuthed false for a gh that exits 0")
		}
		if got := ghArgv(t, argLog); strings.Join(got, " ") != "auth status" {
			t.Fatalf("argv = %v, want [auth status]", got)
		}
	})

	t.Run("not-authed", func(t *testing.T) {
		scratchDir(t, "proj")
		fakeGH(t, 1, "not logged in")
		if !HasGHCLI() {
			t.Fatal("HasGHCLI false with gh on PATH")
		}
		if IsGHAuthed() {
			t.Fatal("IsGHAuthed true for a gh that exits non-zero")
		}
	})

	t.Run("not-installed", func(t *testing.T) {
		scratchDir(t, "proj")
		t.Setenv("PATH", t.TempDir())
		if HasGHCLI() {
			t.Fatal("HasGHCLI true with an empty PATH")
		}
		// Must short-circuit rather than exec a missing binary.
		if IsGHAuthed() {
			t.Fatal("IsGHAuthed true with no gh installed")
		}
	})
}

// The auth shortcut is shelled out through tea.ExecProcess, so the argv is
// built here and never re-derived in the UI. --web is load-bearing: without
// it gh prompts on stdin, which the suspended TUI does not own.
func TestGHAuthLoginCmd(t *testing.T) {
	name, args := GHAuthLoginCmd()
	if name != "gh" {
		t.Fatalf("command = %q, want gh", name)
	}
	if strings.Join(args, " ") != "auth login --web" {
		t.Fatalf("args = %v, want [auth login --web]", args)
	}
}

// ── PushInitial ────────────────────────────────────────

// The "connect an existing remote" path ends here. -u is what makes every
// later plain `git push` (and the ahead/behind readout) work.
func TestPushInitialSetsUpstream(t *testing.T) {
	scratchRepo(t)
	origin := bareOrigin(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")

	if err := PushInitial("main"); err != nil {
		t.Fatalf("PushInitial: %v", err)
	}

	if got := rev(t, origin, "refs/heads/main"); got != rev(t, ".", "HEAD") {
		t.Fatal("origin/main does not point at the pushed commit")
	}
	_, _, hasUpstream := GetAheadBehind("main")
	if !hasUpstream {
		t.Fatal("branch has no upstream after PushInitial")
	}
}

func TestPushInitialSurfacesAFailure(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")
	if err := AddOriginRemote(filepath.Join(t.TempDir(), "nowhere.git")); err != nil {
		t.Fatalf("AddOriginRemote: %v", err)
	}
	if err := PushInitial("main"); err == nil {
		t.Fatal("PushInitial succeeded against a nonexistent remote")
	}
}

// ── .gitignore templates ───────────────────────────────

func TestDetectGitignoreTemplateReadsMarkerFiles(t *testing.T) {
	cases := []struct {
		marker string
		want   string
	}{
		{"go.mod", "Go"},
		{"package.json", "Node"},
		{"pyproject.toml", "Python"},
		{"requirements.txt", "Python"},
		{"setup.py", "Python"},
		{"Cargo.toml", "Rust"},
		{"README.md", "Generic"},
	}
	for _, tc := range cases {
		t.Run(tc.marker, func(t *testing.T) {
			scratchDir(t, "proj")
			write(t, tc.marker, "x\n")
			if got := DetectGitignoreTemplate(); got != tc.want {
				t.Fatalf("DetectGitignoreTemplate() = %q, want %q", got, tc.want)
			}
		})
	}

	// A DIRECTORY named go.mod is not a Go project — the loop skips dirs.
	t.Run("directory-marker-ignored", func(t *testing.T) {
		scratchDir(t, "proj")
		if err := os.Mkdir("go.mod", 0o755); err != nil {
			t.Fatal(err)
		}
		if got := DetectGitignoreTemplate(); got != "Generic" {
			t.Fatalf("a go.mod DIRECTORY detected as %q, want Generic", got)
		}
	})

	t.Run("empty-dir", func(t *testing.T) {
		scratchDir(t, "proj")
		if got := DetectGitignoreTemplate(); got != "Generic" {
			t.Fatalf("empty dir detected as %q, want Generic", got)
		}
	})
}

// Every catalog entry must be pickable: the UI indexes this slice by cursor
// position and writes .Content verbatim. "None (skip)" is the one empty one.
func TestGitignoreTemplatesCatalog(t *testing.T) {
	tpls := GitignoreTemplates()
	if len(tpls) == 0 {
		t.Fatal("no templates")
	}
	empty := 0
	for _, tpl := range tpls {
		if tpl.Name == "" {
			t.Fatal("template with an empty name")
		}
		if tpl.Content == "" {
			empty++
		}
	}
	if empty != 1 {
		t.Fatalf("%d templates have empty content, want exactly 1 (the skip entry)", empty)
	}
	// Detection answers must all exist in the catalog, or the init flow's
	// preselected cursor silently falls back to index 0.
	for _, name := range []string{"Go", "Node", "Python", "Rust", "Generic"} {
		found := false
		for _, tpl := range tpls {
			if tpl.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("DetectGitignoreTemplate can answer %q but the catalog has no such template", name)
		}
	}
}

// Appending must not clobber a .gitignore the user already wrote, and must not
// duplicate lines it already contains.
func TestWriteGitignoreTemplateAppendsAndDedupes(t *testing.T) {
	scratchDir(t, "proj")
	write(t, ".gitignore", "# mine\nnode_modules/\n*.log")

	if err := WriteGitignoreTemplate("# Node\nnode_modules/\ndist/\n"); err != nil {
		t.Fatalf("WriteGitignoreTemplate: %v", err)
	}
	got := readFileString(t, ".gitignore")

	if !strings.Contains(got, "# mine") || !strings.Contains(got, "*.log") {
		t.Fatalf("existing content was clobbered:\n%s", got)
	}
	if strings.Count(got, "node_modules/") != 1 {
		t.Fatalf("node_modules/ appears %d times, want 1:\n%s", strings.Count(got, "node_modules/"), got)
	}
	if !strings.Contains(got, "dist/") {
		t.Fatalf("new entry missing:\n%s", got)
	}
	// A file that ended without a newline must not have the appended block
	// glued onto its last line.
	if strings.Contains(got, "*.log# Node") {
		t.Fatalf("missing newline before the appended block:\n%s", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("result does not end in a newline:\n%q", got)
	}
}

func TestWriteGitignoreTemplateSkipsEmptyContent(t *testing.T) {
	scratchDir(t, "proj")
	if err := WriteGitignoreTemplate(""); err != nil {
		t.Fatalf("WriteGitignoreTemplate(\"\"): %v", err)
	}
	if _, err := os.Stat(".gitignore"); err == nil {
		t.Fatal("the None (skip) template created a .gitignore")
	}
}

func TestCurrentDirNameIsTheDefaultRepoName(t *testing.T) {
	scratchDir(t, "my-project")
	if got := CurrentDirName(); got != "my-project" {
		t.Fatalf("CurrentDirName() = %q, want my-project", got)
	}
}

// ── RemoveFromGitignore ────────────────────────────────

func TestRemoveFromGitignoreRewritesTheFile(t *testing.T) {
	scratchDir(t, "proj")
	write(t, ".gitignore", "# comment\n/build\n/dist\n*.log\n\n")

	if err := RemoveFromGitignore([]string{"/dist"}); err != nil {
		t.Fatalf("RemoveFromGitignore: %v", err)
	}
	got := readFileString(t, ".gitignore")
	if strings.Contains(got, "/dist") {
		t.Fatalf("/dist survived:\n%s", got)
	}
	for _, keep := range []string{"# comment", "/build", "*.log"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("%q was removed too:\n%s", keep, got)
		}
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatalf("trailing blank lines were not trimmed: %q", got)
	}
}

func TestRemoveFromGitignoreNoOps(t *testing.T) {
	scratchDir(t, "proj")

	// No entries → no file created.
	if err := RemoveFromGitignore(nil); err != nil {
		t.Fatalf("RemoveFromGitignore(nil): %v", err)
	}
	// No .gitignore → nothing to do, and no error the caller must handle.
	if err := RemoveFromGitignore([]string{"/build"}); err != nil {
		t.Fatalf("RemoveFromGitignore with no .gitignore: %v", err)
	}
	if _, err := os.Stat(".gitignore"); err == nil {
		t.Fatal("RemoveFromGitignore created a .gitignore")
	}

	// Removing every entry leaves an empty file, not a file containing "\n".
	write(t, ".gitignore", "/build\n")
	if err := RemoveFromGitignore([]string{"/build"}); err != nil {
		t.Fatalf("RemoveFromGitignore: %v", err)
	}
	if got := readFileString(t, ".gitignore"); got != "" {
		t.Fatalf(".gitignore = %q, want empty", got)
	}
}

// ── File content round trip ────────────────────────────

// The file editor reads, the user edits, WriteFileContent writes back. Losing
// the executable bit on a script the user opened to fix a typo is a silent,
// nasty regression.
func TestWriteFileContentPreservesMode(t *testing.T) {
	scratchDir(t, "proj")
	write(t, "run.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod("run.sh", 0o755); err != nil {
		t.Fatal(err)
	}

	content, err := ReadFileContent("run.sh")
	if err != nil {
		t.Fatalf("ReadFileContent: %v", err)
	}
	if content != "#!/bin/sh\necho hi\n" {
		t.Fatalf("ReadFileContent = %q", content)
	}
	if err := WriteFileContent("run.sh", content+"echo bye\n"); err != nil {
		t.Fatalf("WriteFileContent: %v", err)
	}

	info, err := os.Stat("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v after write, want 0755", info.Mode().Perm())
	}
	if got := readFileString(t, "run.sh"); !strings.HasSuffix(got, "echo bye\n") {
		t.Fatalf("content not written: %q", got)
	}
}

func TestReadFileContentReportsAMissingFile(t *testing.T) {
	scratchDir(t, "proj")
	if _, err := ReadFileContent("nope.txt"); err == nil {
		t.Fatal("ReadFileContent succeeded on a missing file")
	}
}

// A brand new file inherits the default mode rather than erroring on the
// stat that reads the existing one.
func TestWriteFileContentCreatesANewFile(t *testing.T) {
	scratchDir(t, "proj")
	if err := WriteFileContent("new.txt", "hello\n"); err != nil {
		t.Fatalf("WriteFileContent: %v", err)
	}
	if got := readFileString(t, "new.txt"); got != "hello\n" {
		t.Fatalf("content = %q", got)
	}
}

// ── Last-commit readouts ───────────────────────────────

func TestGetLastCommitFullSplitsSubjectAndBody(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	runGit(t, "add", "-A")
	runGit(t, "commit", "-q", "-m", "feat(ui): add a thing\n\nWhy it matters.\nSecond line.")

	subject, body := GetLastCommitFull()
	if subject != "feat(ui): add a thing" {
		t.Fatalf("subject = %q", subject)
	}
	if body != "Why it matters.\nSecond line." {
		t.Fatalf("body = %q", body)
	}
	if got := GetLastCommitMessage(); got != subject {
		t.Fatalf("GetLastCommitMessage = %q, want the subject", got)
	}
	if hash := GetLastCommitHash(); hash == "" || len(hash) > 40 {
		t.Fatalf("GetLastCommitHash = %q", hash)
	}
	if sha := HeadSHA(); len(sha) != 40 {
		t.Fatalf("HeadSHA = %q, want a full object name", sha)
	}
	if !strings.HasPrefix(HeadSHA(), GetLastCommitHash()) {
		t.Fatal("short hash is not a prefix of HeadSHA")
	}
}

func TestGetLastCommitFullOnASubjectOnlyCommit(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "chore: seed")

	subject, body := GetLastCommitFull()
	if subject != "chore: seed" || body != "" {
		t.Fatalf("subject=%q body=%q, want the subject and an empty body", subject, body)
	}
}

// Every last-commit readout is called on the amend/confirm screens, which are
// reachable in a repo with no commits at all. None of them may return junk or
// panic there.
func TestLastCommitReadoutsAreEmptyWithoutACommit(t *testing.T) {
	scratchRepo(t)

	if HasAnyCommit() {
		t.Fatal("a fresh repo reports a commit")
	}
	if got := GetLastCommitHash(); got != "" {
		t.Fatalf("GetLastCommitHash = %q", got)
	}
	if got := HeadSHA(); got != "" {
		t.Fatalf("HeadSHA = %q", got)
	}
	if got := GetLastCommitMessage(); got != "" {
		t.Fatalf("GetLastCommitMessage = %q", got)
	}
	if s, b := GetLastCommitFull(); s != "" || b != "" {
		t.Fatalf("GetLastCommitFull = (%q, %q)", s, b)
	}
	if IsLastCommitPushed() {
		t.Fatal("IsLastCommitPushed true in a repo with no commits")
	}
	if got := GetCommitStats(); got != "" {
		t.Fatalf("GetCommitStats = %q", got)
	}
	staged, err := GetStagedFiles()
	if err != nil {
		t.Fatalf("GetStagedFiles: %v", err)
	}
	if len(staged) != 0 {
		t.Fatalf("GetStagedFiles = %v, want none", staged)
	}
}

// The amend flow warns about rewriting a pushed commit purely on this answer.
func TestIsLastCommitPushedFollowsTheRemote(t *testing.T) {
	scratchRepo(t)
	bareOrigin(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")

	if IsLastCommitPushed() {
		t.Fatal("IsLastCommitPushed true before any push")
	}
	runGit(t, "push", "-q", "-u", "origin", "main")
	if !IsLastCommitPushed() {
		t.Fatal("IsLastCommitPushed false right after pushing it")
	}

	// A new local commit on top is not on the remote yet.
	write(t, "b.txt", "b\n")
	commitAll(t, "local only")
	if IsLastCommitPushed() {
		t.Fatal("IsLastCommitPushed true for an unpushed commit")
	}
}

// GetStagedFiles is -z like GetStatus: no quoting, no C-escapes, so a path
// with a space or a non-ASCII character arrives usable as a pathspec.
func TestGetStagedFilesUsesNULSeparatedPaths(t *testing.T) {
	scratchRepo(t)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "seed")

	write(t, "with space.txt", "x\n")
	write(t, "üni.txt", "y\n")
	write(t, "unstaged.txt", "z\n")
	runGit(t, "add", "--", "with space.txt", "üni.txt")

	staged, err := GetStagedFiles()
	if err != nil {
		t.Fatalf("GetStagedFiles: %v", err)
	}
	got := map[string]bool{}
	for _, p := range staged {
		got[p] = true
	}
	for _, want := range []string{"with space.txt", "üni.txt"} {
		if !got[want] {
			t.Fatalf("staged = %v, missing %q (quoting leaked?)", staged, want)
		}
	}
	if got["unstaged.txt"] {
		t.Fatalf("staged = %v, includes an unstaged path", staged)
	}
}

// The Done screen's one-line summary. On a root commit there is no HEAD~1 to
// diff against — that must read as "no stats", not as an error banner.
func TestGetCommitStats(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "root")
	if got := GetCommitStats(); got != "" {
		t.Fatalf("GetCommitStats on a root commit = %q, want empty", got)
	}

	write(t, "a.txt", "a\nb\n")
	commitAll(t, "second")
	got := GetCommitStats()
	if !strings.Contains(got, "1 file changed") {
		t.Fatalf("GetCommitStats = %q, want git's summary line", got)
	}
}

// ── Graph ──────────────────────────────────────────────

// The subject and the ref decorations are separated by 0x1f, not by " (" —
// a commit titled "fix: handle (edge case)" used to render "(edge case)" as
// though it were a branch name.
func TestGetUnifiedGraphSeparatesSubjectFromDecorations(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "fix: handle (edge case)")

	graph := GetUnifiedGraph(10)
	if graph == "" {
		t.Fatal("GetUnifiedGraph returned nothing")
	}
	line := strings.Split(graph, "\n")[0]
	sep := strings.Index(line, "\x1f")
	if sep < 0 {
		t.Fatalf("no 0x1f separator in %q", line)
	}
	subject, deco := line[:sep], line[sep+1:]
	if !strings.HasSuffix(subject, "fix: handle (edge case)") {
		t.Fatalf("subject half = %q, want the full parenthesised subject", subject)
	}
	if !strings.Contains(deco, "main") {
		t.Fatalf("decoration half = %q, want the branch name", deco)
	}
	if strings.Contains(deco, "edge case") {
		t.Fatalf("the subject's parentheses leaked into the decorations: %q", deco)
	}
}

func TestGetUnifiedGraphIsEmptyWithoutCommits(t *testing.T) {
	scratchRepo(t)
	if got := GetUnifiedGraph(10); got != "" {
		t.Fatalf("GetUnifiedGraph = %q on a repo with no commits", got)
	}
}

// ── Branch primitives ──────────────────────────────────

func TestCreateAndSwitchBranch(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")

	if err := CreateBranch("feat"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if b, _ := GetCurrentBranch(); b != "feat" {
		t.Fatalf("current branch = %q after CreateBranch", b)
	}
	if !BranchExists("feat") {
		t.Fatal("BranchExists false for the branch just created")
	}

	// A second create of the same name must fail rather than silently switch.
	if err := CreateBranch("feat"); err == nil {
		t.Fatal("CreateBranch succeeded on an existing branch")
	}

	if err := SwitchBranch("main", false); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	if b, _ := GetCurrentBranch(); b != "main" {
		t.Fatalf("current branch = %q after SwitchBranch", b)
	}
	if err := SwitchBranch("nope", false); err == nil {
		t.Fatal("SwitchBranch succeeded on a branch that does not exist")
	}
}

// isRemote=true creates a local tracking branch instead of checking out a ref
// that has no local name.
func TestSwitchBranchCreatesALocalTrackingBranch(t *testing.T) {
	scratchRepo(t)
	origin := seedRemote(t, "release")

	runGit(t, "remote", "add", "origin", origin)
	runGit(t, "fetch", "-q", "origin")

	if err := SwitchBranch("release", true); err != nil {
		t.Fatalf("SwitchBranch(remote): %v", err)
	}
	if b, _ := GetCurrentBranch(); b != "release" {
		t.Fatalf("current branch = %q", b)
	}
	if !BranchExists("release") {
		t.Fatal("no local branch was created for the remote-only branch")
	}
}

// ── Auto-stash primitives ──────────────────────────────

// The branch-switch dance: HasUncommittedChanges → StashChanges → checkout →
// StashPop. The returned ref is the stable short SHA the recovery hint prints.
func TestStashRoundTrip(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")

	if dirty, err := HasUncommittedChanges(); err != nil || dirty {
		t.Fatalf("HasUncommittedChanges = (%v, %v) on a clean tree", dirty, err)
	}

	write(t, "a.txt", "a\nedited\n")
	write(t, "untracked.txt", "new\n")
	dirty, err := HasUncommittedChanges()
	if err != nil {
		t.Fatalf("HasUncommittedChanges: %v", err)
	}
	if !dirty {
		t.Fatal("HasUncommittedChanges false on a dirty tree")
	}

	ref, err := StashChanges()
	if err != nil {
		t.Fatalf("StashChanges: %v", err)
	}
	if ref == "" || strings.HasPrefix(ref, "stash@") {
		t.Fatalf("StashChanges ref = %q, want a short SHA", ref)
	}
	if clean, _ := HasUncommittedChanges(); clean {
		t.Fatal("tree still dirty after StashChanges")
	}
	// --include-untracked: the new file went with it.
	if _, err := os.Stat("untracked.txt"); err == nil {
		t.Fatal("untracked file was left behind by StashChanges")
	}

	if err := StashPop(); err != nil {
		t.Fatalf("StashPop: %v", err)
	}
	if restored, _ := HasUncommittedChanges(); !restored {
		t.Fatal("changes were not restored by StashPop")
	}
	if _, err := os.Stat("untracked.txt"); err != nil {
		t.Fatalf("untracked file not restored: %v", err)
	}
	if StashCount() != 0 {
		t.Fatalf("stash stack depth = %d after a successful pop, want 0", StashCount())
	}
}

func TestStashPopWithNothingStashedFails(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")
	if err := StashPop(); err == nil {
		t.Fatal("StashPop succeeded with an empty stack")
	}
}

// CleanupFailedStashPop's contract: the working tree comes back CLEAN (no
// conflict markers anywhere) and the stash entry survives so the user can
// still recover it from the stash manager.
func TestCleanupFailedStashPopLeavesACleanTreeAndKeepsTheStash(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "base\n")
	commitAll(t, "seed")

	// Stash one edit, then make a conflicting one so the pop cannot merge.
	write(t, "a.txt", "stashed\n")
	if _, err := StashChanges(); err != nil {
		t.Fatalf("StashChanges: %v", err)
	}
	write(t, "a.txt", "conflicting\n")
	commitAll(t, "diverge")

	if err := StashPop(); err == nil {
		t.Fatal("StashPop unexpectedly succeeded — fixture no longer conflicts")
	}

	CleanupFailedStashPop()

	if dirty, err := HasUncommittedChanges(); err != nil || dirty {
		t.Fatalf("working tree is not clean after cleanup (dirty=%v err=%v):\n%s",
			dirty, err, runGit(t, "status", "--porcelain"))
	}
	if got := readFileString(t, "a.txt"); strings.Contains(got, "<<<<<<<") {
		t.Fatalf("conflict markers survived cleanup:\n%s", got)
	}
	if StashCount() != 1 {
		t.Fatalf("stash stack depth = %d, want the entry kept for manual recovery", StashCount())
	}
}

// ── Merge from origin ──────────────────────────────────

func TestMergeFromOriginFastForwardsAndReportsUpToDate(t *testing.T) {
	scratchRepo(t)
	origin := seedRemote(t, "main")
	runGit(t, "remote", "add", "origin", origin)
	runGit(t, "fetch", "-q", "origin")
	runGit(t, "checkout", "-q", "-B", "main", "origin/main")

	// Nothing new yet.
	upToDate, err := MergeFromOrigin("main", false)
	if err != nil {
		t.Fatalf("MergeFromOrigin: %v", err)
	}
	if !upToDate {
		t.Fatal("upToDate false with nothing to merge")
	}

	// Move origin forward, fetch, then merge — a fast-forward, no merge commit.
	before := commitCount(t)
	pushNewCommitTo(t, origin, "main", "remote.txt", "x\n", "remote work")
	runGit(t, "fetch", "-q", "origin")
	upToDate, err = MergeFromOrigin("main", false)
	if err != nil {
		t.Fatalf("MergeFromOrigin (ff): %v", err)
	}
	if upToDate {
		t.Fatal("upToDate true when a commit was actually merged")
	}
	if commitCount(t) == before {
		t.Fatal("nothing was merged")
	}
	if parents := strings.Fields(runGit(t, "log", "-1", "--format=%p")); len(parents) != 1 {
		t.Fatalf("HEAD has %d parents, want a fast-forward", len(parents))
	}
}

// noFF is what makes the fork/merge diamond visible in the dashboard graph.
func TestMergeFromOriginNoFFAlwaysCreatesAMergeCommit(t *testing.T) {
	scratchRepo(t)
	origin := seedRemote(t, "main")
	runGit(t, "remote", "add", "origin", origin)
	runGit(t, "fetch", "-q", "origin")
	runGit(t, "checkout", "-q", "-B", "main", "origin/main")
	pushNewCommitTo(t, origin, "main", "remote.txt", "x\n", "remote work")
	runGit(t, "fetch", "-q", "origin")

	if _, err := MergeFromOrigin("main", true); err != nil {
		t.Fatalf("MergeFromOrigin(noFF): %v", err)
	}
	if parents := strings.Fields(runGit(t, "log", "-1", "--format=%p")); len(parents) != 2 {
		t.Fatalf("HEAD has %d parents, want 2 (--no-ff)", len(parents))
	}
}

// A conflicting merge leaves MERGE_HEAD in place — the resolver is entered on
// exactly this state, so the merge must NOT be unwound by the failure path.
func TestMergeFromOriginLeavesAConflictInProgressUntilAborted(t *testing.T) {
	scratchRepo(t)
	origin := seedRemote(t, "main")
	runGit(t, "remote", "add", "origin", origin)
	runGit(t, "fetch", "-q", "origin")
	runGit(t, "checkout", "-q", "-B", "main", "origin/main")

	pushConflictingCommitTo(t, origin, "main")
	write(t, "README.md", "local change\n")
	commitAll(t, "local")
	runGit(t, "fetch", "-q", "origin")

	if _, err := MergeFromOrigin("main", false); err == nil {
		t.Fatal("MergeFromOrigin succeeded on a conflicting merge")
	}
	if !MergeInProgress() {
		t.Fatal("MERGE_HEAD is gone — the conflicting merge was unwound")
	}
	if files := GetConflictFiles(); len(files) == 0 {
		t.Fatal("no conflicted files reported")
	}

	if err := MergeAbort(); err != nil {
		t.Fatalf("MergeAbort: %v", err)
	}
	if MergeInProgress() {
		t.Fatal("merge still in progress after MergeAbort")
	}
}

func TestMergeAbortWithNoMergeFails(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")
	if err := MergeAbort(); err == nil {
		t.Fatal("MergeAbort succeeded with no merge in progress")
	}
}

// ── Stash error types ──────────────────────────────────

// The two failure modes are worded differently on screen, so callers classify
// with errors.Is against the sentinels rather than by matching git's English.
func TestStashConflictErrorNamesItsFiles(t *testing.T) {
	withFiles := &StashConflictError{Files: []string{"a.txt", "b.txt"}}
	if !strings.Contains(withFiles.Error(), "a.txt, b.txt") {
		t.Fatalf("Error() = %q, want the file list", withFiles.Error())
	}
	bare := &StashConflictError{}
	if bare.Error() != ErrStashConflict.Error() {
		t.Fatalf("Error() = %q with no files, want the plain sentinel text", bare.Error())
	}
	if !withFiles.Is(ErrStashConflict) {
		t.Fatal("errors.Is against ErrStashConflict failed")
	}
	if withFiles.Is(ErrStashDirtyTree) {
		t.Fatal("a conflict error matched the dirty-tree sentinel")
	}
}

// ── Fixture helpers ────────────────────────────────────

// pushNewCommitTo adds a commit to a bare remote via a throwaway clone, so the
// local repo under test never sees it until it fetches.
func pushNewCommitTo(t *testing.T, origin, branch, filename, content, subject string) {
	t.Helper()
	work := cloneForPush(t, origin)
	if err := os.WriteFile(filepath.Join(work, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	inDir(t, work, "add", "-A")
	inDir(t, work, "commit", "-q", "-m", subject)
	inDir(t, work, "push", "-q", "origin", branch)
}

// pushConflictingCommitTo rewrites README.md (seedRemote's one file) on the
// remote so a local edit to the same line cannot merge.
func pushConflictingCommitTo(t *testing.T, origin, branch string) {
	t.Helper()
	pushNewCommitTo(t, origin, branch, "README.md", "remote change\n", "remote edit")
}

// cloneForPush clones a bare remote into a throwaway directory and gives it a
// committer identity — the suite blanks the global config, so a fresh clone
// has none and `git commit` there would fail with "empty ident name".
func cloneForPush(t *testing.T, origin string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "clone")
	runGit(t, "clone", "-q", origin, work)
	inDir(t, work, "config", "user.name", "git-assist test")
	inDir(t, work, "config", "user.email", "test@example.invalid")
	inDir(t, work, "config", "commit.gpgsign", "false")
	return work
}

// inDir runs git with -C so the test's own cwd (the repo under test) is never
// disturbed — every helper here has to leave it where it found it.
func inDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// ── Network cancellation ───────────────────────────────

// The force-quit path calls CancelNetworkOps so a `gh repo create` the user
// just abandoned cannot go on to create the repository anyway. The latch is
// deliberately one-way, and it is process-global — ResetNetworkOps puts it
// back so one test of this cannot break every later remote call in the binary.
func TestCancelNetworkOpsIsAOneWayLatchUntilReset(t *testing.T) {
	scratchRepo(t)
	origin := bareOrigin(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")
	defer ResetNetworkOps()

	if err := PushInitial("main"); err != nil {
		t.Fatalf("PushInitial before the cancel: %v", err)
	}

	CancelNetworkOps()

	// Every remote call after the cancel fails fast, without shelling out.
	if err := Fetch(); !errors.Is(err, ErrNetworkCancelled) {
		t.Fatalf("Fetch after CancelNetworkOps = %v, want ErrNetworkCancelled", err)
	}
	write(t, "b.txt", "b\n")
	commitAll(t, "second")
	if err := Push("main"); !errors.Is(err, ErrNetworkCancelled) {
		t.Fatalf("Push after CancelNetworkOps = %v, want ErrNetworkCancelled", err)
	}
	// The push really did not happen — origin is still on the first commit.
	if rev(t, origin, "refs/heads/main") == rev(t, ".", "HEAD") {
		t.Fatal("a cancelled push still moved origin")
	}

	// Local commands are untouched: cancelling remote work must not disable
	// the rest of the app on its way out.
	if !HasAnyCommit() || GetLastCommitMessage() != "second" {
		t.Fatal("local git commands stopped working after CancelNetworkOps")
	}

	ResetNetworkOps()
	if err := Fetch(); err != nil {
		t.Fatalf("Fetch after ResetNetworkOps: %v", err)
	}
}

// ── RemoveCached ───────────────────────────────────────

// The .gitignore flow's un-track step. "Stop tracking this, but do NOT delete
// my file" is the whole point — a beginner who adds node_modules/ to
// .gitignore and finds it gone from disk has lost real work.
func TestRemoveCachedUntracksWithoutDeleting(t *testing.T) {
	scratchRepo(t)
	write(t, "keep.txt", "keep me\n")
	write(t, "secrets/token", "s3cret\n")
	commitAll(t, "seed")

	if err := RemoveCached([]string{"secrets"}); err != nil {
		t.Fatalf("RemoveCached: %v", err)
	}

	// Still on disk.
	if got := readFileString(t, "secrets/token"); got != "s3cret\n" {
		t.Fatalf("the file was deleted or changed: %q", got)
	}
	// No longer tracked.
	if err := exec.Command("git", "ls-files", "--error-unmatch", "--", "secrets/token").Run(); err == nil {
		t.Fatal("the path is still tracked")
	}
	// Untouched neighbours stay tracked.
	if err := exec.Command("git", "ls-files", "--error-unmatch", "--", "keep.txt").Run(); err != nil {
		t.Fatal("an unrelated file was untracked too")
	}
}

func TestRemoveCachedNoOpsAndReportsFailures(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "seed")

	if err := RemoveCached(nil); err != nil {
		t.Fatalf("RemoveCached(nil): %v", err)
	}
	// A path git never tracked cannot be un-tracked; the caller has to hear
	// about it rather than have the commit silently proceed.
	err := RemoveCached([]string{"never-tracked.txt"})
	if err == nil {
		t.Fatal("RemoveCached succeeded on an untracked path")
	}
	if !strings.Contains(err.Error(), "rm --cached failed") {
		t.Fatalf("error = %q, want the rm --cached prefix", err)
	}
}
