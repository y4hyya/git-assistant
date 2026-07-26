package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git-assist/internal/types"
)

// ErrBinaryFile is returned when a file is detected as binary.
var ErrBinaryFile = errors.New("binary file")

// IsGitRepo checks if the current directory is inside a git repository.
func IsGitRepo() bool {
	return exec.Command("git", "rev-parse", "--is-inside-work-tree").Run() == nil
}

// RepoToplevel returns the absolute path of the working tree root. Every path
// git reports is relative to that root, so callers launched from a
// subdirectory must chdir there before any status/add/diff work. Errors when
// the cwd isn't inside a working tree (bare repo or no repo at all).
func RepoToplevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", errors.New("git rev-parse --show-toplevel returned no path")
	}
	return root, nil
}

// InitRepo runs `git init` in the current working directory. When
// defaultBranch is non-empty, it also sets the initial branch via
// `-b <name>` so the first commit lands on the desired branch name.
func InitRepo(defaultBranch string) error {
	args := []string{"init"}
	if defaultBranch != "" {
		args = append(args, "-b", defaultBranch)
	}
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("init failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// AddOriginRemote adds `origin` pointing at the given URL. If origin already
// exists with a different URL, it updates the URL instead.
func AddOriginRemote(url string) error {
	if existing := GetRemoteURL(); existing != "" {
		out, err := exec.Command("git", "remote", "set-url", "origin", url).CombinedOutput()
		if err != nil {
			return fmt.Errorf("remote set-url failed: %s", strings.TrimSpace(string(out)))
		}
		return nil
	}
	out, err := exec.Command("git", "remote", "add", "origin", url).CombinedOutput()
	if err != nil {
		return fmt.Errorf("remote add failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveOriginRemote removes the `origin` remote, if it exists. No-op when
// origin isn't configured.
func RemoveOriginRemote() error {
	if GetRemoteURL() == "" {
		return nil
	}
	out, err := exec.Command("git", "remote", "remove", "origin").CombinedOutput()
	if err != nil {
		return fmt.Errorf("remote remove failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// RenameBranch renames the currently checked-out branch. Used right after
// init when the default branch name needs changing (older git versions that
// don't support `init -b`).
func RenameBranch(name string) error {
	out, err := exec.Command("git", "branch", "-M", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("branch rename failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// HasGHCLI reports whether the GitHub CLI (`gh`) is on PATH.
func HasGHCLI() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

// IsGHAuthed reports whether `gh` has a valid authenticated session.
// `gh auth status` exits 0 when authenticated.
func IsGHAuthed() bool {
	if !HasGHCLI() {
		return false
	}
	return exec.Command("gh", "auth", "status").Run() == nil
}

// GHCreateRepo creates a new GitHub repo from the current directory using
// the `gh` CLI. Always sets `origin` via `--source=.`. When push is true,
// also pushes the current branch — requires at least one commit. When push
// is false, only creates the empty remote and wires `origin`, so the caller
// can push later via the normal commit → push flow.
func GHCreateRepo(name string, private bool, push bool) error {
	visibility := "--public"
	if private {
		visibility = "--private"
	}
	args := []string{"repo", "create", name, visibility, "--source=.", "--remote=origin"}
	if push {
		args = append(args, "--push")
	}
	out, err := exec.Command("gh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh repo create failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// HasAnyCommit reports whether the repo has at least one commit on HEAD.
func HasAnyCommit() bool {
	return exec.Command("git", "rev-parse", "--verify", "HEAD").Run() == nil
}

// PushInitial pushes the current branch to origin and sets upstream tracking.
// Used after InitRepo + AddOriginRemote when connecting to an existing remote.
func PushInitial(branch string) error {
	out, err := exec.Command("git", "push", "-u", "origin", branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GetCurrentBranch returns the name of the current branch.
func GetCurrentBranch() (string, error) {
	out, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "HEAD (detached)", nil
	}
	return branch, nil
}

// GetStatus returns the list of changed files.
//
// Uses the NUL-separated porcelain format: in -z mode git never quotes or
// C-escapes paths, so filenames with spaces, quotes or non-ASCII characters
// come through verbatim (plain `--porcelain` renders them as `"d\303\266k.md"`,
// which then fails every downstream pathspec). `--untracked-files=all` lists
// the individual files inside untracked directories instead of a single
// `dir/` record that can't be diffed.
func GetStatus() ([]types.FileEntry, error) {
	out, err := exec.Command("git", "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if err != nil {
		return nil, err
	}
	return parseStatusZ(string(out)), nil
}

// parseStatusZ turns `git status --porcelain=v1 -z` output into file entries.
// Records are NUL-terminated. A record is `XY <path>`; for renames and copies
// (X or Y is R/C) the record is `XY <new>\0<old>\0`, i.e. the following field
// is the original path and belongs to the same entry.
func parseStatusZ(data string) []types.FileEntry {
	records := strings.Split(data, "\x00")
	var files []types.FileEntry

	for i := 0; i < len(records); i++ {
		rec := records[i]
		// "XY " + at least one path character
		if len(rec) < 4 {
			continue
		}

		xy := rec[:2]
		path := rec[3:]

		// Rename/copy records carry the original path in the next field.
		// Consume it regardless of how the status is classified below —
		// e.g. "RD" (renamed in index, deleted in worktree) is reported as
		// a deletion but still has two path fields.
		var origPath string
		if xy != "??" && (xy[0] == 'R' || xy[1] == 'R' || xy[0] == 'C' || xy[1] == 'C') {
			if i+1 < len(records) {
				origPath = records[i+1]
				i++
			}
		}

		var status types.FileStatus
		switch {
		case xy == "??":
			status = types.StatusUntracked
		case xy[0] == 'A' || xy[1] == 'A':
			status = types.StatusAdded
		case xy[0] == 'D' || xy[1] == 'D':
			status = types.StatusDeleted
		case xy[0] == 'R' || xy[1] == 'R':
			status = types.StatusRenamed
		default:
			status = types.StatusModified
		}

		// No on-disk filter here: git is the authority on what changed.
		// A stat-based filter silently swallowed broken symlinks (stat
		// follows the link) and every path the old quoting bug mangled.
		files = append(files, types.FileEntry{
			Path:     path,
			OrigPath: origPath,
			Status:   status,
		})
	}

	return files
}

// GetBranches returns available branches for pushing.
// The current branch is always listed first.
func GetBranches(currentBranch string) []string {
	branches := []string{currentBranch}

	out, err := exec.Command("git", "branch", "-r").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			branch := strings.TrimSpace(line)
			if branch == "" || strings.Contains(branch, "->") {
				continue
			}
			// Only origin is addressable: every push/checkout built from this
			// list hardcodes `origin`, so an `upstream/main` entry would turn
			// into `git push origin HEAD:upstream/main` and create a junk
			// branch on the user's own remote.
			if !strings.HasPrefix(branch, "origin/") {
				continue
			}
			branch = strings.TrimPrefix(branch, "origin/")
			if branch != currentBranch {
				branches = append(branches, branch)
			}
		}
	}

	return branches
}

// HasRemote checks if any remote is configured.
func HasRemote() bool {
	out, err := exec.Command("git", "remote").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// Fetch updates all remote-tracking refs. Non-destructive: never touches the
// working tree, index, or local branches — only refs under refs/remotes/*.
func Fetch() error {
	out, err := exec.Command("git", "fetch", "--all", "--prune", "--quiet").CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// isStageable reports whether `git add` can act on a path: it either still
// exists on disk or git has it in the index. `git add` fails with "pathspec
// did not match any files" on a path that is in neither — the normal state of
// a rename's original path when the rename is already staged.
func isStageable(path string) bool {
	if _, err := os.Lstat(path); err == nil {
		return true
	}
	return exec.Command("git", "ls-files", "--error-unmatch", "--", path).Run() == nil
}

// stagePaths returns the pathspecs that must be staged for one entry. A
// rename needs both halves: `git add <new>` alone records the addition and
// leaves the old path tracked, so the commit would contain half a rename and
// a dangling `D <old>` in the working tree.
func stagePaths(f types.FileEntry) []string {
	if f.Status == types.StatusRenamed && f.OrigPath != "" && isStageable(f.OrigPath) {
		return []string{f.OrigPath, f.Path}
	}
	return []string{f.Path}
}

// stageEntries stages every entry, collecting failures instead of stopping at
// the first one. Returns the paths that could not be staged plus git's last
// error output, so callers can refuse to commit a partial selection.
func stageEntries(files []types.FileEntry) (failed []string, lastErr string) {
	for _, f := range files {
		args := append([]string{"add", "--"}, stagePaths(f)...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			failed = append(failed, f.Path)
			lastErr = strings.TrimSpace(string(out))
		}
	}
	return failed, lastErr
}

// Commit stages the selected files and creates a commit.
// cachedPaths are files that were gitignored and need git rm --cached
// re-applied after the staging reset.
func Commit(files []types.FileEntry, cachedPaths []string, message string) error {
	// Reset staging area so only the user's selected files are committed.
	// `git reset` fails on repos with no commits yet (no HEAD to reset to);
	// skip the call there. For repos with commits, propagate the error so
	// we don't silently commit files the user didn't choose.
	if HasAnyCommit() {
		if out, err := exec.Command("git", "reset").CombinedOutput(); err != nil {
			return fmt.Errorf("reset staging: %s", strings.TrimSpace(string(out)))
		}
	}

	// Re-apply rm --cached for gitignored tracked files
	if err := RemoveCached(cachedPaths); err != nil {
		return err
	}

	// Stage the selection. If any file fails to stage we commit nothing —
	// silently committing the survivors while the confirm screen promised
	// all of them is worse than an error the user can act on.
	failed, lastErr := stageEntries(files)
	if len(failed) > 0 {
		return fmt.Errorf("could not stage %s (nothing committed): %s",
			strings.Join(failed, ", "), lastErr)
	}

	// Commit
	if out, err := exec.Command("git", "commit", "-m", message).CombinedOutput(); err != nil {
		return fmt.Errorf("commit failed: %s", strings.TrimSpace(string(out)))
	}

	return nil
}

// GetLastCommitHash returns the short hash of the last commit.
func GetLastCommitHash() string {
	out, err := exec.Command("git", "log", "-1", "--format=%h").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GetLastCommitMessage returns the subject line of the last commit.
func GetLastCommitMessage() string {
	out, err := exec.Command("git", "log", "-1", "--format=%s").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GetLastCommitFull returns the subject (first line) and body of the most
// recent commit as separate strings. Used by the amend flow to pre-fill the
// wizard with the existing commit content. Empty strings if anything fails.
func GetLastCommitFull() (subject, body string) {
	s, err := exec.Command("git", "log", "-1", "--format=%s").Output()
	if err != nil {
		return "", ""
	}
	subject = strings.TrimSpace(string(s))
	b, bErr := exec.Command("git", "log", "-1", "--format=%b").Output()
	if bErr != nil {
		return subject, ""
	}
	body = strings.TrimSpace(string(b))
	return subject, body
}

// IsLastCommitPushed reports whether HEAD is contained in any remote-tracking
// branch (i.e. already pushed). The amend flow uses this to warn the user
// that amending a pushed commit will require `git push --force-with-lease`
// to update upstream — silently amending and then failing the next plain
// push is a worse experience than the up-front warning.
func IsLastCommitPushed() bool {
	out, err := exec.Command("git", "branch", "-r", "--contains", "HEAD").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// Amend stages any newly-selected files on top of the existing index and
// re-runs the last commit with the given message. Unlike Commit, we don't
// `git reset` first — the whole point of amend is to keep what's already
// in HEAD and layer additional changes (if any) into the same commit.
func Amend(files []types.FileEntry, message string) error {
	failed, lastErr := stageEntries(files)
	if len(failed) > 0 {
		return fmt.Errorf("could not stage %s (commit not amended): %s",
			strings.Join(failed, ", "), lastErr)
	}
	out, err := exec.Command("git", "commit", "--amend", "-m", message).CombinedOutput()
	if err != nil {
		return fmt.Errorf("amend failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// UndoLastCommit performs a soft reset, keeping changes staged.
func UndoLastCommit() error {
	// On a single-commit repo HEAD~1 doesn't resolve and git answers with a
	// raw "fatal: ambiguous argument 'HEAD~1'". Check first and say what the
	// user can actually do about it.
	if exec.Command("git", "rev-parse", "--verify", "--quiet", "HEAD~1").Run() != nil {
		return errors.New("nothing to undo — this is the repository's first commit (use Amend to change it)")
	}
	out, err := exec.Command("git", "reset", "--soft", "HEAD~1").CombinedOutput()
	if err != nil {
		return fmt.Errorf("undo failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Push pushes to the specified remote branch.
func Push(currentBranch, targetBranch string) error {
	var args []string
	if currentBranch == targetBranch {
		args = []string{"push", "-u", "origin", targetBranch}
	} else {
		args = []string{"push", "origin", "HEAD:" + targetBranch}
	}

	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ignoreKey normalizes a .gitignore line for duplicate detection so that
// "/x", "./x" and "x" (and their trailing-slash variants) compare equal.
func ignoreKey(entry string) string {
	e := strings.TrimSpace(entry)
	e = strings.TrimPrefix(e, "./")
	e = strings.TrimPrefix(e, "/")
	return strings.TrimSuffix(e, "/")
}

// anchorIgnorePattern turns a repo-relative path into an anchored .gitignore
// pattern. A slash-less pattern like `config.json` matches at every depth, so
// ignoring one root file would also hide `deploy/config.json` — and ignored
// files never show up in git status, leaving no way to notice. A leading "/"
// pins the pattern to the repo root, which is exactly what the picker meant.
// Negations and comments are passed through untouched.
func anchorIgnorePattern(entry string) string {
	e := strings.TrimSpace(entry)
	if e == "" || strings.HasPrefix(e, "#") || strings.HasPrefix(e, "!") {
		return e
	}
	e = strings.TrimPrefix(e, "./")
	if strings.HasPrefix(e, "/") {
		return e
	}
	return "/" + e
}

// AddToGitignore appends the given paths to .gitignore, skipping duplicates.
// Paths are repo-relative (they come from git status), so they are written
// anchored to the repo root.
func AddToGitignore(paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	existing := make(map[string]bool)
	content, err := os.ReadFile(".gitignore")
	if err == nil {
		for _, line := range strings.Split(string(content), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				existing[ignoreKey(line)] = true
			}
		}
	}

	var toAdd []string
	for _, p := range paths {
		pattern := anchorIgnorePattern(p)
		if pattern == "" {
			continue
		}
		key := ignoreKey(pattern)
		if existing[key] {
			continue
		}
		existing[key] = true // dedupe within this batch too
		toAdd = append(toAdd, pattern)
	}

	if len(toAdd) == 0 {
		return nil
	}

	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open .gitignore: %w", err)
	}
	defer f.Close()

	// Ensure we start on a new line if file doesn't end with one
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}

	for _, p := range toAdd {
		if _, err := f.WriteString(p + "\n"); err != nil {
			return err
		}
	}

	return nil
}

// GetGitignoreEntries reads .gitignore and returns non-empty, non-comment lines.
func GetGitignoreEntries() []string {
	content, err := os.ReadFile(".gitignore")
	if err != nil {
		return nil
	}
	var entries []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			entries = append(entries, line)
		}
	}
	return entries
}

// RemoveFromGitignore removes the given entries from .gitignore by rewriting the file.
func RemoveFromGitignore(entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	remove := make(map[string]bool)
	for _, e := range entries {
		remove[e] = true
	}

	content, err := os.ReadFile(".gitignore")
	if err != nil {
		return nil // no .gitignore, nothing to remove
	}

	var kept []string
	for _, line := range strings.Split(string(content), "\n") {
		if !remove[strings.TrimSpace(line)] {
			kept = append(kept, line)
		}
	}

	// Trim trailing empty lines
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}

	result := strings.Join(kept, "\n")
	if result != "" {
		result += "\n"
	}
	return os.WriteFile(".gitignore", []byte(result), 0644)
}

// RemoveCached removes files from git tracking without deleting them from disk.
func RemoveCached(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"rm", "--cached", "-r", "--"}, paths...)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rm --cached failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GetCommitStats returns a short summary of the last commit.
func GetCommitStats() string {
	out, err := exec.Command("git", "diff", "--stat", "HEAD~1..HEAD").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return ""
}

// maxPreviewBytes caps how much of an untracked file we read for the diff
// view. The read happens synchronously inside Update(), and the rendered copy
// costs another 2-3x in memory, so anything larger freezes the UI instead of
// being useful.
const maxPreviewBytes = 2 << 20 // 2 MiB

// stripCR removes carriage returns from content headed for the TUI. Lip Gloss
// measures \r as zero-width but the terminal acts on it, returning the cursor
// to column 0 so the box padding and right border overwrite the line.
func stripCR(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	return strings.ReplaceAll(s, "\r", "")
}

// GetFileDiff returns the diff output for a single file.
// Routes by FileStatus to avoid guessing from empty diff output.
func GetFileDiff(path string, status types.FileStatus) (string, error) {
	switch status {
	case types.StatusUntracked:
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("read file: %w", err)
		}
		switch {
		case info.IsDir():
			// git reports embedded repos as a single directory entry; there
			// is no file to read.
			return "(directory — no preview available)\n", nil
		case info.Mode()&os.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return "(symlink)\n", nil
			}
			return "(new symlink)\n+ → " + stripCR(target) + "\n", nil
		case info.Size() > maxPreviewBytes:
			return fmt.Sprintf("(file too large to preview — %d KB)\n", info.Size()/1024), nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read file: %w", err)
		}
		if isBinary(content) {
			return "", ErrBinaryFile
		}
		var b strings.Builder
		b.WriteString("(new file)\n")
		for _, line := range strings.Split(strings.TrimRight(stripCR(string(content)), "\n"), "\n") {
			b.WriteString("+ " + line + "\n")
		}
		return b.String(), nil

	case types.StatusDeleted:
		out, err := exec.Command("git", "show", "HEAD:"+path).CombinedOutput()
		if err != nil {
			// File may not be in HEAD — try index
			out, err = exec.Command("git", "diff", "--cached", "--", path).CombinedOutput()
			if err != nil {
				return "(deleted file)\n", nil
			}
			result := stripCR(strings.TrimSpace(string(out)))
			if result != "" {
				return result, nil
			}
			return "(deleted file)\n", nil
		}
		if isBinary(out) {
			return "", ErrBinaryFile
		}
		var b strings.Builder
		b.WriteString("(deleted file)\n")
		for _, line := range strings.Split(strings.TrimRight(stripCR(string(out)), "\n"), "\n") {
			b.WriteString("- " + line + "\n")
		}
		return b.String(), nil

	default:
		// Modified, Added, Renamed — try diff against HEAD, then cached
		out, err := exec.Command("git", "diff", "HEAD", "--", path).CombinedOutput()
		if err == nil {
			result := stripCR(strings.TrimSpace(string(out)))
			if result != "" {
				if strings.Contains(result, "Binary files") {
					return "", ErrBinaryFile
				}
				return result, nil
			}
		}
		// Fallback: staged changes not yet committed
		out, err = exec.Command("git", "diff", "--cached", "--", path).CombinedOutput()
		if err == nil {
			result := stripCR(strings.TrimSpace(string(out)))
			if result != "" {
				if strings.Contains(result, "Binary files") {
					return "", ErrBinaryFile
				}
				return result, nil
			}
		}
		// Fallback: unstaged changes only
		out, err = exec.Command("git", "diff", "--", path).CombinedOutput()
		if err == nil {
			result := stripCR(strings.TrimSpace(string(out)))
			if result != "" {
				if strings.Contains(result, "Binary files") {
					return "", ErrBinaryFile
				}
				return result, nil
			}
		}
		return "(no changes to display)\n", nil
	}
}

// IsBinaryContent reports whether content looks like binary data (contains a
// NUL byte). Exported so callers can re-check content they read themselves —
// e.g. before handing a file to a text editor, where round-tripping binary
// bytes through a UTF-8 string would destroy the file.
func IsBinaryContent(content string) bool {
	return isBinary([]byte(content))
}

// ReadFileContent reads the raw content of a file in the working tree.
func ReadFileContent(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	return string(content), nil
}

// WriteFileContent writes content to a file in the working tree.
func WriteFileContent(path string, content string) error {
	info, err := os.Stat(path)
	perm := os.FileMode(0644)
	if err == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// GetBehindMain returns how many commits the given branch is behind main.
// Tries local main/master first, then origin/main / origin/master so users
// who clone fresh (no local main yet) still get the "behind" indicator.
// Returns 0 if branch is main/master itself or if no candidate ref resolves.
func GetBehindMain(branch string) int {
	if branch == "main" || branch == "master" {
		return 0
	}
	for _, ref := range []string{"main", "master", "origin/main", "origin/master"} {
		out, err := exec.Command("git", "rev-list", "--count", branch+".."+ref).Output()
		if err != nil {
			continue
		}
		var count int
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count)
		return count
	}
	return 0
}

// ── Config operations ──────────────────────────────────

// GetConfigValue returns the value of a git config key. The `set` flag
// distinguishes "key not configured" (git exits 1) from "value is the
// empty string" — the previous bool-free signature lost that distinction
// and made the config editor unable to show a useful "not set" hint.
// A non-nil err means git itself failed (missing binary, broken config),
// which is different from the key simply not being set.
func GetConfigValue(key string, global bool) (value string, set bool, err error) {
	args := []string{"config"}
	if global {
		args = append(args, "--global")
	} else {
		args = append(args, "--local")
	}
	args = append(args, "--", key)
	out, runErr := exec.Command("git", args...).Output()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Exit 1 = key is not set. Normal, not an error.
			return "", false, nil
		}
		return "", false, fmt.Errorf("git config: %w", runErr)
	}
	return strings.TrimSpace(string(out)), true, nil
}

// SetConfigValue sets a git config key to the given value.
func SetConfigValue(key, value string, global bool) error {
	args := []string{"config"}
	if global {
		args = append(args, "--global")
	} else {
		args = append(args, "--local")
	}
	args = append(args, key, value)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("config set failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GetRemoteURL returns the URL of the 'origin' remote.
func GetRemoteURL() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ── Graph operations ───────────────────────────────────

// GetUnifiedGraph returns the git log graph for all branches (local + remote).
// Uses %d to include branch name decorations on relevant commits.
func GetUnifiedGraph(limit int) string {
	out, err := exec.Command("git", "log", "--graph",
		"--format=%s%d", "--all",
		fmt.Sprintf("-%d", limit)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

// GetAheadBehind returns how many commits the local branch is ahead/behind its
// upstream. hasUpstream is false when the branch tracks nothing (never pushed,
// or the tracking ref is gone) — callers must not render that case as 0/0,
// which reads identically to a fully synced branch.
func GetAheadBehind(branch string) (ahead, behind int, hasUpstream bool) {
	if branch == "" {
		return 0, 0, false
	}
	upOut, err := exec.Command("git", "rev-parse", "--abbrev-ref",
		branch+"@{upstream}").Output()
	if err != nil {
		return 0, 0, false
	}
	upstream := strings.TrimSpace(string(upOut))
	if upstream == "" {
		return 0, 0, false
	}

	out, err := exec.Command("git", "rev-list", "--left-right", "--count",
		branch+"..."+upstream).Output()
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &ahead)
		fmt.Sscanf(parts[1], "%d", &behind)
	}
	return ahead, behind, true
}

// ── Branch operations ──────────────────────────────────

// GetAllBranches returns local and remote-only branches.
// Local branches come first (current branch at top), then remote-only branches.
// Remote branches that have a local equivalent are deduplicated.
func GetAllBranches() []types.BranchEntry {
	var entries []types.BranchEntry
	localNames := make(map[string]bool)

	// Local branches
	out, err := exec.Command("git", "branch").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			isCurrent := strings.HasPrefix(line, "* ")
			// "+ name" marks a branch checked out in another worktree.
			// Without stripping it the name becomes "+ name" and every
			// checkout/delete/merge built from it fails on a bad pathspec.
			elsewhere := strings.HasPrefix(line, "+ ")
			name := strings.TrimPrefix(strings.TrimPrefix(line, "* "), "+ ")
			name = strings.TrimSpace(name)
			if name == "" || strings.HasPrefix(name, "(HEAD detached") {
				continue
			}
			localNames[name] = true
			entries = append(entries, types.BranchEntry{
				Name:                name,
				IsCurrent:           isCurrent,
				CheckedOutElsewhere: elsewhere,
			})
		}
	}

	// Remote branches (only those without a local equivalent)
	out, err = exec.Command("git", "branch", "-r").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.Contains(line, "->") {
				continue
			}
			// Only origin: switching to a remote-only branch runs
			// `git checkout -b <n> origin/<n>`, so an `upstream/foo` entry
			// would resolve to the nonexistent ref origin/upstream/foo.
			if !strings.HasPrefix(line, "origin/") {
				continue
			}
			// Strip origin/ prefix for dedup check
			short := strings.TrimPrefix(line, "origin/")
			if localNames[short] {
				continue
			}
			entries = append(entries, types.BranchEntry{
				Name:     short,
				IsRemote: true,
			})
		}
	}

	return entries
}

// SwitchBranch checks out an existing branch.
// For remote-only branches, it creates a local tracking branch.
func SwitchBranch(name string, isRemote bool) error {
	var out []byte
	var err error
	if isRemote {
		out, err = exec.Command("git", "checkout", "-b", name, "origin/"+name).CombinedOutput()
	} else {
		out, err = exec.Command("git", "checkout", name).CombinedOutput()
	}
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CreateBranch creates a new branch from HEAD and switches to it.
func CreateBranch(name string) error {
	out, err := exec.Command("git", "checkout", "-b", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteBranch deletes a local branch (safe delete, -d).
func DeleteBranch(name string) error {
	out, err := exec.Command("git", "branch", "-d", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// MergeBranch merges the given branch into the current branch.
func MergeBranch(name string) error {
	out, err := exec.Command("git", "merge", "--no-ff", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// MergeFromOrigin merges origin/<branch> into the current branch. When noFF is
// true, forces a merge commit (used for integrating upstream branches like
// main → feature). When false, allows fast-forward (used for pulling the same
// branch from origin — no artificial merge commits when catching up).
func MergeFromOrigin(branch string, noFF bool) error {
	args := []string{"merge"}
	if noFF {
		args = append(args, "--no-ff")
	}
	args = append(args, "origin/"+branch)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GetIncomingCommits returns commit subjects reachable from `to` but not from
// `from`. Used to preview what a pull or merge would bring in.
// Example: GetIncomingCommits("main", "origin/main", 10) shows what pulling
// origin/main would add to local main.
func GetIncomingCommits(from, to string, limit int) []string {
	out, err := exec.Command("git", "log",
		"--format=%s", fmt.Sprintf("-%d", limit),
		from+".."+to).Output()
	if err != nil {
		return nil
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commits = append(commits, line)
		}
	}
	return commits
}

// ResolveMainBranch returns "main" or "master" depending on which exists as a
// local branch. Returns "main" if neither exists (safe default).
func ResolveMainBranch() string {
	if exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/main").Run() == nil {
		return "main"
	}
	if exec.Command("git", "show-ref", "--verify", "--quiet", "refs/heads/master").Run() == nil {
		return "master"
	}
	return "main"
}

// MergeAbort aborts an in-progress merge.
func MergeAbort() error {
	out, err := exec.Command("git", "merge", "--abort").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GetConflictFiles returns the list of files with merge conflicts.
func GetConflictFiles() []string {
	out, err := exec.Command("git", "diff", "--name-only", "--diff-filter=U").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}

// HasUncommittedChanges reports whether the working tree has uncommitted
// changes. Callers must check the error — returning a default of "clean"
// when git itself fails would let downstream destructive ops (like
// branch switch with auto-stash) proceed on an unknown working tree.
func HasUncommittedChanges() (bool, error) {
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// StashChanges stashes uncommitted changes, including untracked files. The
// returned ref is the short SHA of the new stash entry — surface it to the
// user when a later pop fails so they can recover via `git stash apply <ref>`
// without having to guess which stash@{N} is theirs.
func StashChanges() (ref string, err error) {
	out, cmdErr := exec.Command("git", "stash", "push", "--include-untracked").CombinedOutput()
	if cmdErr != nil {
		return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	// Resolve stash@{0} to a short SHA. The SHA is stable across subsequent
	// stash operations; stash@{N} indices shift, which is why we don't hand
	// the symbolic ref back to the user.
	refOut, refErr := exec.Command("git", "rev-parse", "--short", "stash@{0}").Output()
	if refErr != nil {
		return "stash@{0}", nil
	}
	return strings.TrimSpace(string(refOut)), nil
}

// StashPop restores the most recent stash.
func StashPop() error {
	out, err := exec.Command("git", "stash", "pop").CombinedOutput()
	if err != nil {
		return fmt.Errorf("stash pop failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CleanupFailedStashPop resets the working directory after a failed stash pop.
// The stash remains in the stack so the user can retry manually.
func CleanupFailedStashPop() {
	exec.Command("git", "reset", "HEAD").Run()
	exec.Command("git", "checkout", "--", ".").Run()
}

// isBinary checks if content contains null bytes (indicating binary data).
func isBinary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}
