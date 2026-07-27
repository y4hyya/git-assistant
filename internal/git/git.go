package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"git-assist/internal/types"
)

// ErrBinaryFile is returned when a file is detected as binary.
var ErrBinaryFile = errors.New("binary file")

// ── Remote operations: timeout and cancellation ────────

// networkTimeout bounds every command that talks to a remote. Without it a
// stalled fetch/push (dead network, ssh waiting on a passphrase it can't
// prompt for) locks the TUI spinner forever, and the user can only force-quit
// without ever learning whether the operation finished.
const networkTimeout = 60 * time.Second

var (
	// ErrNetworkTimeout is returned when a remote operation outlived
	// networkTimeout. Identified with errors.Is by callers that want to skip
	// the usual "here is git's output" formatting — there isn't any.
	ErrNetworkTimeout = errors.New("network operation timed out after 60s — check your connection")

	// ErrNetworkCancelled is returned when CancelNetworkOps killed the
	// command mid-flight.
	ErrNetworkCancelled = errors.New("network operation cancelled")
)

// netCtx parents every remote command. Cancelling it kills the child process
// too (that is what exec.CommandContext buys us), which matters most for
// `gh repo create`: on a plain process exit the orphaned child kept running
// and created the repository the user had just tried to cancel.
//
// netMu guards both: CancelNetworkOps runs on the Bubble Tea update goroutine
// while runNetworkTimeout reads netCtx from whichever command goroutine is
// dispatching, and ResetNetworkOps writes it.
var (
	netMu             sync.Mutex
	netCtx, netCancel = context.WithCancel(context.Background())
)

// CancelNetworkOps aborts every in-flight remote operation. Wired into the
// force-quit path — after it runs, further remote calls fail fast with
// ErrNetworkCancelled, which is correct for a process on its way out.
func CancelNetworkOps() {
	netMu.Lock()
	defer netMu.Unlock()
	netCancel()
}

// ResetNetworkOps re-arms the latch CancelNetworkOps throws.
//
// Nothing in the application calls this, and it must stay that way: the
// one-way latch is the point. A force-quit has to keep a queued `gh repo
// create` from starting after the user abandoned it, so "cancelled" is a
// terminal state for the process that set it.
//
// It exists because that state is process-global, and a test binary is the
// one process that legitimately runs both halves: a single test of the
// ctrl+c handler would otherwise fail every later fetch, push and
// `gh repo create` in the binary with ErrNetworkCancelled — in whatever
// order -shuffle happened to pick. Tests that perform real remote operations
// call this first.
func ResetNetworkOps() {
	netMu.Lock()
	defer netMu.Unlock()
	netCtx, netCancel = context.WithCancel(context.Background())
}

// networkContext reads the current parent context under the lock.
func networkContext() context.Context {
	netMu.Lock()
	defer netMu.Unlock()
	return netCtx
}

// runNetworkTimeout runs a remote-touching command under an explicit deadline.
// Local-only git commands deliberately get no timeout: a large merge or
// checkout can legitimately take minutes and killing it mid-write is worse
// than waiting.
func runNetworkTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	return runNetworkCtx(networkContext(), timeout, name, args...)
}

// networkWaitDelay bounds how long Wait may go on copying output after the
// deadline (or a cancellation) has already killed the command. See
// runNetworkCtx: the kill alone does not unblock the read.
const networkWaitDelay = 3 * time.Second

// runNetworkCtx is runNetworkTimeout with the parent context spelled out, so
// the cancellation path is reachable from a test without cancelling the
// package-level netCtx for the rest of the process.
//
// Killing the command is NOT enough to make the deadline fire, and that was the
// whole bug this now guards against. git does not talk to a remote itself: an
// https fetch runs git-remote-https, an ssh one runs ssh, and those children
// inherit the stdout/stderr pipes CombinedOutput reads. SIGKILL on git leaves
// the transport holding the write end, and the read blocks until IT exits —
// which for the stalled-ssh case the timeout exists for is never. The 60s
// message never appeared and the input-blocking spinner ran forever.
//
// Two mechanisms, because neither covers the other's case:
//
//  1. the child gets its own process group and the cancel kills the GROUP, so
//     the transport dies with git instead of being orphaned onto a repository
//     it is still writing to;
//  2. WaitDelay bounds the wait regardless — on a platform with no process
//     groups, or against anything that escapes the group kill, Wait stops
//     copying, closes the parent's pipe ends and returns.
func runNetworkCtx(parent context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = networkWaitDelay

	out, err := cmd.CombinedOutput()
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return out, ErrNetworkTimeout
		case errors.Is(ctx.Err(), context.Canceled):
			return out, ErrNetworkCancelled
		}
		// The command itself finished cleanly and something it spawned kept the
		// pipe open past WaitDelay — an ssh ControlPersist master is the common
		// one. That is not a failed push, and reporting it as one would send the
		// user to re-push work that is already on the remote.
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			return out, nil
		}
	}
	return out, err
}

func runNetwork(name string, args ...string) ([]byte, error) {
	return runNetworkTimeout(networkTimeout, name, args...)
}

// netFail turns a runNetwork failure into the error callers surface. A timeout
// or cancellation already explains itself and has no meaningful output;
// anything else carries git's own text under the given prefix.
func netFail(prefix string, out []byte, err error) error {
	if errors.Is(err, ErrNetworkTimeout) || errors.Is(err, ErrNetworkCancelled) {
		return err
	}
	msg := strings.TrimSpace(string(out))
	if prefix != "" {
		return fmt.Errorf("%s: %s", prefix, msg)
	}
	return fmt.Errorf("%s", msg)
}

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
	out, err := runNetwork("gh", args...)
	if err != nil {
		return netFail("gh repo create failed", out, err)
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
	out, err := runNetwork("git", "push", "-u", "origin", branch)
	if err != nil {
		return netFail("", out, err)
	}
	return nil
}

// DetachedLabel is what GetCurrentBranch answers when HEAD is on no branch at
// all. It is a DISPLAY string and nothing else: it is not a ref, `git checkout
// "HEAD (detached)"` is a fatal, and pushing it would try to create a remote
// branch of that name. Anything that has to change BEHAVIOUR in this state must
// ask IsDetachedHead rather than compare against this constant.
const DetachedLabel = "HEAD (detached)"

// IsDetachedHead reports whether HEAD points straight at a commit instead of at
// a branch — after `git checkout <sha>`, a tag, or a bisect. Commits made there
// belong to no branch and are only reachable through the reflog.
//
// `symbolic-ref --quiet HEAD` is the exact question: it exits non-zero only when
// HEAD is not a symref. An unborn HEAD (a fresh `git init`, no commits yet) is
// still a symref pointing at refs/heads/<default>, so it correctly reports
// false — that repository is on a branch, it just has nothing on it.
func IsDetachedHead() bool {
	return exec.Command("git", "symbolic-ref", "--quiet", "HEAD").Run() != nil
}

// GetCurrentBranch returns the name of the current branch, or DetachedLabel when
// there is none. See DetachedLabel: callers that do more than print it must gate
// on IsDetachedHead first.
func GetCurrentBranch() (string, error) {
	out, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return DetachedLabel, nil
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
		// Unmerged FIRST, or the codes get swallowed by the arms below: AA and
		// AU read as "added", DD/DU/UD as "deleted", and UU as plain modified —
		// which is how a file full of conflict markers used to reach the commit
		// wizard as an ordinary "M". conflictKindFor is the single list of the
		// seven codes git calls unmerged (see conflict.go).
		case isUnmergedCode(xy):
			status = types.StatusConflicted
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
	out, err := runNetwork("git", "fetch", "--all", "--prune", "--quiet")
	if err != nil {
		return netFail("fetch", out, err)
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
			// A deletion that is already staged (rm --cached from any source
			// — this wizard's gitignore flow or the user's own terminal)
			// makes `git add` fail: the path is in no index, often not on
			// disk, and possibly ignored. The index already says exactly
			// what the picker's D row promised, so that is success.
			if indexAlreadyDeletes(f.Path) {
				continue
			}
			failed = append(failed, f.Path)
			lastErr = strings.TrimSpace(string(out))
		}
	}
	return failed, lastErr
}

// indexAlreadyDeletes reports whether the index stages a deletion for path
// with nothing further in the worktree to pick up (porcelain "D " — staged
// delete, clean worktree side).
func indexAlreadyDeletes(path string) bool {
	out, err := exec.Command("git", "status", "--porcelain=v1", "-z", "--", path).Output()
	if err != nil {
		return false
	}
	rec := strings.SplitN(string(out), "\x00", 2)[0]
	return len(rec) >= 3 && rec[0] == 'D' && rec[1] == ' '
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

	// The rm --cached above already staged those paths exactly as intended:
	// removed from the index, kept (or already gone) on disk. Running
	// `git add` on them again can only fail — the path is now absent from
	// the index, usually absent from the worktree, and matched by the very
	// .gitignore rule the user just wrote, so git answers "pathspec did not
	// match" or "paths are ignored" and the whole commit aborts. Skip them.
	cached := make(map[string]bool, len(cachedPaths))
	for _, p := range cachedPaths {
		cached[p] = true
	}
	var toStage []types.FileEntry
	for _, f := range files {
		if !cached[f.Path] {
			toStage = append(toStage, f)
		}
	}

	// Stage the selection. If any file fails to stage we commit nothing —
	// silently committing the survivors while the confirm screen promised
	// all of them is worse than an error the user can act on.
	failed, lastErr := stageEntries(toStage)
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

// HeadSHA returns the full object name of HEAD, or "" when there is none.
// Read before a rewrite (amend, undo) so callers can remember exactly which
// commit origin is expected to be holding — see PushForceWithLease.
func HeadSHA() string {
	out, err := exec.Command("git", "rev-parse", "--verify", "--quiet", "HEAD").Output()
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

// GetStagedFiles returns the paths that currently differ between the index and
// HEAD — everything `git commit` (or `git commit --amend`) would sweep into the
// next commit, whether the wizard selected it or not. The amend confirm screen
// uses this to disclose work staged outside git-assist instead of silently
// folding it into the rewritten commit.
//
// NUL-separated like GetStatus: in -z mode git never quotes or C-escapes paths,
// so filenames with spaces or non-ASCII characters come through verbatim.
// A repo with no commits has no HEAD to diff against — report an empty set
// rather than an error, since amend is unreachable there anyway.
func GetStagedFiles() ([]string, error) {
	if !HasAnyCommit() {
		return nil, nil
	}
	out, err := exec.Command("git", "diff", "--cached", "--name-only", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
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

// Revert records a NEW commit that undoes sha, leaving sha itself in history.
// It is the counterpart to UndoLastCommit for work that is already on origin:
// undo rewrites the branch and needs a force push afterwards, revert only adds
// to it and pushes normally.
//
// --no-edit keeps git from opening $EDITOR inside our alt-screen TUI; the
// generated "Revert "<subject>"" message is exactly what the screen promised.
// A conflicting revert leaves the sequencer mid-flight — callers must
// RevertAbort, there is no in-TUI conflict resolution.
func Revert(sha string) error {
	if strings.TrimSpace(sha) == "" {
		return errors.New("no commit to revert")
	}
	out, err := exec.Command("git", "revert", "--no-edit", sha).CombinedOutput()
	if err != nil {
		return fmt.Errorf("revert failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// RevertAbort cancels a revert that stopped on conflicts and puts the working
// tree back where it started. Best-effort: callers run it on the failure path,
// where a second error has nothing to add.
func RevertAbort() error {
	out, err := exec.Command("git", "revert", "--abort").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// DiscardFile throws one entry's changes away, routed by what the entry
// actually is. Every branch below is destructive and none of them is
// recoverable through git — the confirmation in front of this call is the only
// safety there is, which is why the routing lives here rather than in the UI:
// the sentence on screen and the command that runs must be decided by the same
// switch.
//
//   - untracked: git has never seen this path, so there is nothing to restore
//     it FROM. Discarding means deleting it off disk.
//   - renamed: both halves or neither. Restoring only the new path leaves the
//     old one deleted — half a rename, and a working tree in a state the user
//     never asked for.
//   - deleted: the same restore resurrects the file from HEAD. `x` on a deleted
//     entry is the one case that CREATES something.
//   - added: `git add`ed but never committed. restore --staged --worktree
//     unstages it and removes it, because HEAD has no version to fall back to.
//   - modified (and everything else): back to the last committed content.
func DiscardFile(f types.FileEntry) error {
	path := strings.TrimSpace(f.Path)
	if path == "" {
		return errors.New("no file to discard")
	}
	switch f.Status {
	case types.StatusUntracked:
		// RemoveAll, not Remove: `--untracked-files=all` expands untracked
		// directories into their files, except an embedded repository, which
		// git reports as the single directory entry it is.
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("could not delete %s: %w", path, err)
		}
		return nil
	case types.StatusRenamed:
		paths := []string{path}
		if f.OrigPath != "" && f.OrigPath != path {
			paths = append(paths, f.OrigPath)
		}
		return restorePaths(paths)
	default:
		return restorePaths([]string{path})
	}
}

// restorePaths runs the one restore both the index and the working tree see.
// --staged without --worktree would leave the edits on disk (the file still
// differs from HEAD, just unstaged), which is not what "discard" means to
// anyone.
func restorePaths(paths []string) error {
	args := append([]string{"restore", "--worktree", "--staged", "--"}, paths...)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("discard failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Push publishes the given branch to origin/<branch>.
//
// One branch, one destination: the branch you are on goes to the remote branch
// of the same name. The old two-argument form built `HEAD:<target>` when the
// picker's selection differed from the checked-out branch, which pushed the
// CURRENT commits onto some other branch's remote ref — a rename, a
// force-in-disguise or a junk branch, depending on what the two sides held, and
// nothing on screen said so.
//
// -u only when the branch tracks nothing yet. Passing it on every push
// re-declares an upstream the branch already has: harmless, but git answers
// with a "set up to track" line that reads like something changed.
func Push(branch string) error {
	if branch == "" {
		return errors.New("no branch to push")
	}
	args := []string{"push"}
	if !HasUpstream(branch) {
		args = append(args, "-u")
	}
	args = append(args, "origin", branch)

	out, err := runNetwork("git", args...)
	if err != nil {
		return netFail("", out, err)
	}
	return nil
}

// PushForceWithLease replaces origin/<branch> with the local branch, but only
// if origin still points at expect. That "lease" is the difference between this
// and --force: if anyone else pushed in the meantime, git refuses (with "stale
// info") instead of deleting their commits.
//
// expect is spelled out — `--force-with-lease=<branch>:<sha>` — rather than
// left to git's default, and that is the whole safety story here. Bare
// --force-with-lease leases against the local remote-tracking ref, which THIS
// APP moves on its own: git-assist fetches at startup and every 30 seconds. A
// fetch that lands between the rewrite and the push quietly teaches the lease
// that the other person's commit is expected, and the force then deletes it —
// verified, not theorised. Pinning the SHA the user was shown makes the promise
// on screen ("only if nobody else pushed meanwhile") the one git enforces.
//
// An empty expect falls back to the default lease: `=<branch>:` means "expect
// this ref not to exist", which would be a very different (and destructive)
// request.
//
// The one legitimate caller is a local history rewrite — an amend or an undo of
// a commit origin already has. Callers must gate on that; a branch that is
// merely behind needs a pull, and force-pushing it destroys the commits it was
// behind by.
func PushForceWithLease(branch, expect string) error {
	if branch == "" {
		return errors.New("no branch to push")
	}
	lease := "--force-with-lease"
	if expect != "" {
		lease += "=" + branch + ":" + expect
	}
	out, err := runNetwork("git", "push", lease, "origin", branch)
	if err != nil {
		return netFail("", out, err)
	}
	return nil
}

// RemoteTrackingSHA returns the commit origin/<branch> currently points at, as
// this repository last saw it. Empty when there is no such remote-tracking ref.
// Read at the moment the user is shown what a force push would replace, and
// handed back to PushForceWithLease as the lease.
func RemoteTrackingSHA(branch string) string {
	if branch == "" {
		return ""
	}
	out, err := exec.Command("git", "rev-parse", "--verify", "--quiet",
		"refs/remotes/origin/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// upstreamRef returns the remote-tracking branch `branch` follows
// ("origin/main"), and whether it tracks anything at all. Never-pushed branches
// and branches whose upstream was pruned both report false.
func upstreamRef(branch string) (string, bool) {
	if branch == "" {
		return "", false
	}
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref",
		branch+"@{upstream}").Output()
	if err != nil {
		return "", false
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" {
		return "", false
	}
	return ref, true
}

// HasUpstream reports whether branch tracks a remote branch. A false here is
// what turns a push into a publish: the first push has to set tracking up.
func HasUpstream(branch string) bool {
	_, ok := upstreamRef(branch)
	return ok
}

// GetOutgoingCommits returns the subjects of commits the given branch has and
// origin/<branch> does not — exactly what a push would send. The mirror image
// of GetIncomingCommits, and the same sampling contract: at most limit
// subjects, with CountOutgoingCommits for the true size.
//
// Measured against origin/<branch> rather than the configured upstream on
// purpose: origin/<branch> is where Push sends them.
func GetOutgoingCommits(branch string, limit int) []string {
	if branch == "" {
		return nil
	}
	return GetIncomingCommits("origin/"+branch, branch, limit)
}

// CountOutgoingCommits returns how many commits the push would send. 0 when
// origin has no such branch yet (nothing to compare against) — the publish
// case, which callers detect with HasUpstream.
func CountOutgoingCommits(branch string) int {
	if branch == "" {
		return 0
	}
	return CountIncomingCommits("origin/"+branch, branch)
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

// mainRefState records which of the four candidate "main" refs exist. One
// for-each-ref answers all of them, so every caller that needs to know what
// "main" means pays a single fork — the old probe loop spent up to four
// *failing* rev-lists per call, on the UI thread, on repos that have neither.
type mainRefState struct {
	localMain, localMaster   bool
	originMain, originMaster bool
}

func readMainRefs() mainRefState {
	out, err := exec.Command("git", "for-each-ref", "--format=%(refname)",
		"refs/heads/main", "refs/heads/master",
		"refs/remotes/origin/main", "refs/remotes/origin/master").Output()
	if err != nil {
		return mainRefState{}
	}
	var s mainRefState
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		switch strings.TrimSpace(line) {
		case "refs/heads/main":
			s.localMain = true
		case "refs/heads/master":
			s.localMaster = true
		case "refs/remotes/origin/main":
			s.originMain = true
		case "refs/remotes/origin/master":
			s.originMaster = true
		}
	}
	return s
}

// name is the branch name this repository spells "main" with. Local refs
// decide it; a fresh clone with no local main yet falls back to whichever
// spelling origin uses, so a master-based remote is not mislabelled "main".
func (s mainRefState) name() string {
	switch {
	case s.localMain:
		return "main"
	case s.localMaster:
		return "master"
	case s.originMain:
		return "main"
	case s.originMaster:
		return "master"
	}
	return "main"
}

// ref is the ref that stands in for "main" when syncing: origin/<main> when
// that remote-tracking ref exists, the local branch otherwise, "" when neither
// does. Preferring origin is what keeps the dashboard's "behind main" badge
// and the sync action pointed at the same commits — measuring the badge
// against a local main that origin has moved past left a badge no amount of
// syncing could clear.
func (s mainRefState) ref() string {
	name := s.name()
	other := "master"
	if name == "master" {
		other = "main"
	}
	has := map[string]bool{
		"origin/main":   s.originMain,
		"origin/master": s.originMaster,
		"main":          s.localMain,
		"master":        s.localMaster,
	}
	for _, cand := range []string{"origin/" + name, "origin/" + other, name, other} {
		if has[cand] {
			return cand
		}
	}
	return ""
}

// MainSync is the single definition of "the main line" shared by the dashboard
// badge, the menu's sync shortcut and the sync dialog. All three used to
// resolve it independently — the badge against local main, the action against
// origin/main — which made the badge unclearable in one direction and silently
// absent in the other.
type MainSync struct {
	Name   string // display name: "main" or "master"
	Ref    string // ref to measure and merge against; "" when none resolves
	Behind int    // commits `branch` is behind Ref
}

// ResolveMainSync answers all three questions in one pass: what main is called
// here, which ref represents it, and how far behind it `branch` is. Behind is
// 0 when branch IS main (there is nothing to sync into itself; catching up
// with origin on main is the pull path) or when no main ref resolves.
func ResolveMainSync(branch string) MainSync {
	s := readMainRefs()
	ms := MainSync{Name: s.name(), Ref: s.ref()}
	if branch == "" || ms.Ref == "" || branch == ms.Name || branch == ms.Ref {
		return ms
	}
	ms.Behind = CountIncomingCommits(branch, ms.Ref)
	return ms
}

// GetBehindMain returns how many commits the given branch is behind the main
// line — measured against exactly the ref the sync action merges. See
// ResolveMainSync.
func GetBehindMain(branch string) int {
	return ResolveMainSync(branch).Behind
}

// MainSyncRef returns the ref the sync action should merge, or "" when this
// repository has no main branch at all.
func MainSyncRef() string {
	return readMainRefs().ref()
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

// UnsetConfigValue removes a git config key from the given scope.
//
// This is what "clear this field" has to mean. SetConfigValue(key, "") writes
// an EMPTY key, which is not the same thing at all: an empty local user.name
// shadows the global one and `git commit` then dies with "empty ident name",
// while the editor happily renders the key as set-but-blank.
//
// Exit code 5 is git's "you tried to unset something that was not set" — the
// desired end state, so it counts as success.
func UnsetConfigValue(key string, global bool) error {
	args := []string{"config"}
	if global {
		args = append(args, "--global")
	} else {
		args = append(args, "--local")
	}
	args = append(args, "--unset", "--", key)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 5 {
			return nil
		}
		return fmt.Errorf("config unset failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ConfigBool interprets a raw config value the way git itself does: true, yes,
// on and 1 all mean enabled, in any case. Reading the raw string and comparing
// it to "true" reported a repository with `commit.gpgsign = 1` as "off" — while
// git was signing every commit — and made the first toggle a no-op.
func ConfigBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
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
//
// %x1f (ASCII unit separator) is emitted between the subject and the
// decorations so the renderer can tell them apart. With a bare "%s%d" the two
// are only separated by " (", which is also legal inside a subject — a commit
// titled "fix: handle (edge case)" then rendered "(edge case)" as if it were a
// branch. A subject CAN carry a 0x1f of its own (`git commit -m $'a\x1fb'` is
// accepted and %s emits the byte raw), so the renderer anchors the split on the
// LAST separator: the decorations are the final field and a ref name cannot
// contain a control character. See parseLine in internal/ui/layout.go.
//
// --exclude=refs/stash is not optional and has to come BEFORE --all (an exclude
// applies to the ref-collecting option that FOLLOWS it). --all includes
// refs/stash, and this app manufactures stashes on its own — every branch
// switch, merge and pull auto-stashes a dirty tree. One entry drew a
// three-parent "WIP on main" / "index on main" / "untracked files on main"
// diamond above the real history: five rows of the panel's budget spent telling
// a beginner that their uncommitted work was already committed.
func GetUnifiedGraph(limit int) string {
	out, err := exec.Command("git", "log", "--graph",
		"--format=%s%x1f%d", "--exclude=refs/stash", "--all",
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
	upstream, ok := upstreamRef(branch)
	if !ok {
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

// ErrBranchNotMerged marks the one `git branch -d` refusal that has an answer
// inside the TUI: the branch holds commits no other branch contains. Callers
// match it with errors.Is and offer a force delete instead of dead-ending on
// git's raw multi-line output.
var ErrBranchNotMerged = errors.New("branch is not fully merged")

// DeleteBranch deletes a local branch. force picks `git branch -D` (drops
// unmerged commits, recoverable only through the reflog) over the safe `-d`.
// A safe delete git refuses as unmerged comes back wrapped in
// ErrBranchNotMerged, with git's own text preserved for display.
func DeleteBranch(name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	out, err := exec.Command("git", "branch", flag, name).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if !force && strings.Contains(msg, "not fully merged") {
			return fmt.Errorf("%w: %s", ErrBranchNotMerged, msg)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// BranchExists reports whether a LOCAL branch of that name is already there.
// Asked before a rename so the refusal can name the collision instead of
// forwarding git's bare "fatal: a branch named 'x' already exists".
func BranchExists(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	return exec.Command("git", "show-ref", "--verify", "--quiet",
		"refs/heads/"+name).Run() == nil
}

// RenameBranchTo renames an existing local branch. Unlike RenameBranch (which
// is `-M` on whatever is checked out, used once during init), this is the safe
// `-m` form and names both ends, so it works on a branch you are not standing
// on and refuses to clobber an existing name.
//
// Purely local. `git branch -m` moves the branch's config section along with
// it, so a renamed branch keeps tracking the SAME remote-tracking ref: origin
// still holds the old name until something pushes the new one. Callers say so —
// a beginner who renames a pushed branch and sees no change on GitHub has no
// way to guess that from here.
func RenameBranchTo(oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return errors.New("branch rename needs both names")
	}
	if oldName == newName {
		return fmt.Errorf("%s is already called that", oldName)
	}
	if err := CheckRefFormatBranch(newName); err != nil {
		return err
	}
	if BranchExists(newName) {
		return fmt.Errorf("a branch named %s already exists — pick another name", newName)
	}
	out, err := exec.Command("git", "branch", "-m", oldName, newName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// CheckRefFormatBranch asks git whether name can be a branch name. git is the
// authority on ref-name rules (control characters, "..", "@{", trailing "/",
// leading "-", reserved sequences) and reimplementing them here would drift;
// `git check-ref-format --branch` needs no repository and no network.
// Returns nil when the name is usable; otherwise git's own one-line fatal.
func CheckRefFormatBranch(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("branch name is empty")
	}
	out, err := exec.Command("git", "check-ref-format", "--branch", name).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	// git appends hint lines to some refusals; the fatal is the useful line.
	if i := strings.IndexByte(msg, '\n'); i > 0 {
		msg = msg[:i]
	}
	if msg == "" {
		msg = fmt.Sprintf("fatal: '%s' is not a valid branch name", name)
	}
	return errors.New(msg)
}

// mergeWasNoOp reports whether git's merge output means "there was nothing to
// merge". Both spellings are covered: git renamed the message from
// "Already up-to-date." to "Already up to date." in 2.29.
func mergeWasNoOp(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "already up to date") ||
		strings.Contains(s, "already up-to-date")
}

// MergeBranch merges the given branch into the current branch.
//
// upToDate reports that git found nothing to merge. It is deliberately not an
// error: the callers used to render "Merged feature into main" for a merge that
// created no commit and changed no file, which reads as a completed integration
// and sends people looking for changes that were never there.
func MergeBranch(name string) (upToDate bool, err error) {
	out, err := exec.Command("git", "merge", "--no-ff", name).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return mergeWasNoOp(out), nil
}

// MergeFromOrigin merges origin/<branch> into the current branch. When noFF is
// true, forces a merge commit (used for integrating upstream branches like
// main → feature). When false, allows fast-forward (used for pulling the same
// branch from origin — no artificial merge commits when catching up).
//
// upToDate carries the same meaning as in MergeBranch.
//
// LOCAL, despite the name: origin/<branch> is a remote-TRACKING ref that the
// background fetch has already written, and merging it touches nothing but this
// repository. It used to run under runNetwork's 60s deadline, which made a big
// but perfectly healthy merge (large tree, LFS smudge filters, slow disk) die by
// SIGKILL at one minute, mid-write, reported as "network operation timed out —
// check your connection" for an operation that never opened a socket. That is
// the case the file's own rule at the top covers: a local command gets no
// timeout, because killing it half way through is worse than waiting.
func MergeFromOrigin(branch string, noFF bool) (upToDate bool, err error) {
	args := []string{"merge"}
	if noFF {
		args = append(args, "--no-ff")
	}
	args = append(args, "origin/"+branch)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return mergeWasNoOp(out), nil
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

// CountIncomingCommits returns how many commits are reachable from `to` but not
// from `from` — the true size of the set GetIncomingCommits only samples. The
// sync dialog needs both: the sample to show, the count to be honest about
// what it isn't showing. Returns 0 when either ref is missing.
func CountIncomingCommits(from, to string) int {
	out, err := exec.Command("git", "rev-list", "--count", from+".."+to).Output()
	if err != nil {
		return 0
	}
	var count int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &count)
	return count
}

// RemoteDefaultBranch returns the remote-tracking ref that best represents
// origin's default branch: origin/HEAD's target when that symref exists, then
// origin/main, origin/master, then the first origin/* ref. Empty when origin
// has no branches at all (a freshly created, empty GitHub repo). Reads local
// refs only — Fetch first if freshness matters.
func RemoteDefaultBranch() string {
	if out, err := exec.Command("git", "symbolic-ref", "--short", "-q",
		"refs/remotes/origin/HEAD").Output(); err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" {
			return ref
		}
	}
	for _, ref := range []string{"origin/main", "origin/master"} {
		if exec.Command("git", "show-ref", "--verify", "--quiet", "refs/remotes/"+ref).Run() == nil {
			return ref
		}
	}
	out, err := exec.Command("git", "branch", "-r", "--format=%(refname:short)").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "->") || !strings.HasPrefix(line, "origin/") {
			continue
		}
		return line
	}
	return ""
}

// UnrelatedHistories reports whether this repository is already set up for
// git's "refusing to merge unrelated histories" dead end against origin, and
// names the remote ref it compared against. Two shapes land there:
//
//   - local HEAD exists and shares no merge-base with the remote branch — every
//     later push and pull is rejected outright;
//   - local HEAD is unborn while origin already has commits — the first local
//     commit becomes a second root and produces the same rejection later.
//
// Reads local refs only; callers fetch first.
func UnrelatedHistories() (remoteRef string, unrelated bool) {
	ref := RemoteDefaultBranch()
	if ref == "" {
		return "", false
	}
	if !HasAnyCommit() {
		// Nothing local to compare yet, but the collision is already fixed:
		// whatever the user commits here will not descend from the remote.
		return ref, true
	}
	if exec.Command("git", "merge-base", "HEAD", ref).Run() == nil {
		return ref, false
	}
	return ref, true
}

// ResolveMainBranch returns the name this repository uses for its main branch,
// "main" or "master". Local refs decide; a fresh clone with no local main yet
// takes origin's spelling rather than defaulting to "main" and then asking git
// for an origin/main that does not exist. "main" when nothing resolves.
func ResolveMainBranch() string {
	return readMainRefs().name()
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
