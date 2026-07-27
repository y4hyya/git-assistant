package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"git-assist/internal/git"
	"git-assist/internal/types"
	tea "github.com/charmbracelet/bubbletea"
)

// The wizard's mutating steps hand Bubble Tea a tea.Cmd and never touch git
// themselves. Everywhere else in this suite those commands are deliberately
// left unexecuted — they would run real git against the developer's tree. Here
// they are run, inside a throwaway repository, because the message each one
// sends back is what every screen after it renders.

// runCmd executes a tea.Cmd and returns its message, flattening the one-level
// tea.Batch the wizard uses to pair an operation with its spinner tick.
func runCmd[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	var zero T
	if cmd == nil {
		t.Fatal("no command to run")
		return zero
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if m, ok := c().(T); ok {
				return m
			}
		}
		t.Fatalf("no %T in the batch", zero)
		return zero
	}
	out, ok := msg.(T)
	if !ok {
		t.Fatalf("command returned %T, want %T", msg, zero)
	}
	return out
}

func entryFor(t *testing.T, path string) types.FileEntry {
	t.Helper()
	for _, e := range statusEntries(t) {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("%q is not in git status", path)
	return types.FileEntry{}
}

func statusEntries(t *testing.T) []types.FileEntry {
	t.Helper()
	entries, err := git.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	return entries
}

// ── doCommit ───────────────────────────────────────────

// The hash and the diffstat come back WITH the result, read on the command
// goroutine — the Push and Done screens are View functions and used to fork
// `git log` and `git diff --stat` on every keypress, resize and spinner tick.
func TestDoCommitReturnsTheHashAndStatsTheNextScreensRender(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	writeFile(t, "new.txt", "hello\n")
	writeFile(t, "ignored-by-the-wizard.txt", "not selected\n")

	res := runCmd[commitResultMsg](t, doCommit(
		[]types.FileEntry{entryFor(t, "new.txt")}, nil, "feat: add new.txt"))

	if res.err != nil {
		t.Fatalf("doCommit: %v", res.err)
	}
	if res.hash == "" {
		t.Fatal("no short hash came back — the push and done screens show it")
	}
	if !strings.HasPrefix(readHead(t), res.hash) {
		t.Fatalf("hash %q is not HEAD", res.hash)
	}
	if res.stats == "" {
		t.Fatal("no diffstat came back")
	}
	if !strings.Contains(res.stats, "1 file changed") {
		t.Fatalf("stats = %q", res.stats)
	}
	if subject := gitLine(t, "log", "-1", "--format=%s"); subject != "feat: add new.txt" {
		t.Fatalf("commit subject = %q", subject)
	}
	// Only the selection was committed.
	if committed := gitLine(t, "show", "--name-only", "--format=", "HEAD"); strings.Contains(committed, "ignored-by-the-wizard") {
		t.Fatalf("an unselected file was committed:\n%s", committed)
	}
}

// A commit git refuses comes back as a message, not a panic or a silent
// success — the confirm screen has to be able to say what went wrong.
func TestDoCommitReportsAFailure(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	res := runCmd[commitResultMsg](t, doCommit(
		[]types.FileEntry{{Path: "does-not-exist.txt", Status: types.StatusModified}}, nil, "feat: nope"))

	if res.err == nil {
		t.Fatal("committing a path that does not exist was reported as success")
	}
	if res.hash != "" {
		t.Fatalf("a failed commit still reported hash %q", res.hash)
	}
	// Nothing was committed.
	if n := gitLine(t, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("commit count = %s, want the seed commit alone", n)
	}
}

// ── doAmend ────────────────────────────────────────────

// Amend rewrites HEAD rather than adding a commit, and keeps what was already
// in it while layering the newly selected files on top.
func TestDoAmendRewritesHeadInPlace(t *testing.T) {
	tempRepo(t, "feat: original subject", "")
	before := readHead(t)
	writeFile(t, "extra.txt", "extra\n")

	res := runCmd[commitResultMsg](t, doAmend(
		[]types.FileEntry{entryFor(t, "extra.txt")}, "feat: rewritten subject"))

	if res.err != nil {
		t.Fatalf("doAmend: %v", res.err)
	}
	if res.hash == "" {
		t.Fatal("no hash came back")
	}
	if n := gitLine(t, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("commit count = %s — amend added a commit instead of rewriting one", n)
	}
	if readHead(t) == before {
		t.Fatal("HEAD did not move — nothing was rewritten")
	}
	if subject := gitLine(t, "log", "-1", "--format=%s"); subject != "feat: rewritten subject" {
		t.Fatalf("subject = %q", subject)
	}
	// Both the original file and the newly staged one are in the commit.
	files := gitLine(t, "show", "--name-only", "--format=", "HEAD")
	for _, want := range []string{"seed.txt", "extra.txt"} {
		if !strings.Contains(files, want) {
			t.Fatalf("HEAD contains %q, want both halves", files)
		}
	}
}

func TestDoAmendReportsAFailure(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	res := runCmd[commitResultMsg](t, doAmend(
		[]types.FileEntry{{Path: "does-not-exist.txt", Status: types.StatusModified}}, "feat: nope"))

	if res.err == nil {
		t.Fatal("amending with an unstageable path was reported as success")
	}
	if subject := gitLine(t, "log", "-1", "--format=%s"); subject != "chore: seed" {
		t.Fatalf("the commit was rewritten anyway: %q", subject)
	}
}

// ── doSave ─────────────────────────────────────────────

// A save has to bring back BOTH the fresh file list and the fresh diff: the
// edit changes the working tree under the selector, and returning to a stale
// list would show the file as it was before the edit.
func TestDoSaveWritesAndReturnsFreshState(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	res := runCmd[saveResultMsg](t, doSave("seed.txt", "seed\nedited by the editor\n", types.StatusModified))

	if res.err != nil {
		t.Fatalf("doSave: %v", res.err)
	}
	if got := readFile(t, "seed.txt"); got != "seed\nedited by the editor\n" {
		t.Fatalf("file content = %q", got)
	}
	if len(res.files) == 0 {
		t.Fatal("no file list came back — the selector would redraw stale rows")
	}
	if !strings.Contains(res.diff, "edited by the editor") {
		t.Fatalf("diff = %q, want the edit that was just saved", res.diff)
	}
}

func TestDoSaveReportsAWriteFailure(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	if err := os.Mkdir("adir", 0o755); err != nil {
		t.Fatal(err)
	}
	// Writing a file over a directory fails at the filesystem, which is the
	// one failure this command can hit.
	res := runCmd[saveResultMsg](t, doSave("adir", "content\n", types.StatusModified))
	if res.err == nil {
		t.Fatal("writing over a directory was reported as success")
	}
}

// ── enterConfirm ───────────────────────────────────────

// The confirm screen caches everything it displays, because it is a View
// function: `git branch -r --contains HEAD` is not a question to ask ten times
// a second for the whole of "Committing...". Neither answer can change while
// the screen is up.
func TestEnterConfirmCachesTheGitStateItDisplays(t *testing.T) {
	tempRepoWithOrigin(t, "feat: original")

	writeFile(t, "staged-elsewhere.txt", "outside the wizard\n")
	gitRun(t, "add", "staged-elsewhere.txt")

	m := wizardModel(t, stepMessage, file("seed.txt"))
	m.amendMode = true
	m.enterConfirm()

	if m.step != stepConfirm {
		t.Fatalf("step = %d, want the confirm screen", m.step)
	}
	if m.amendSHA == "" {
		t.Fatal("the commit's short SHA was not cached")
	}
	if !m.amendPushed {
		t.Fatal("amendPushed is false for a commit that is on origin — the warning would never fire")
	}
	if len(m.rewritePendingSHA) != 40 {
		t.Fatalf("rewritePendingSHA = %q, want the full object name (the push lease)", m.rewritePendingSHA)
	}
	// Work staged outside git-assist would be folded into the rewritten
	// commit silently; the screen has to be able to disclose it.
	found := false
	for _, p := range m.amendStaged {
		if p == "staged-elsewhere.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("amendStaged = %v, want the path staged outside the wizard", m.amendStaged)
	}
	if m.confirmScroll != 0 {
		t.Fatalf("confirmScroll = %d, want it reset on entry", m.confirmScroll)
	}
}

// A plain commit asks git nothing: there is no commit to rewrite, so none of
// the amend readings are taken.
func TestEnterConfirmSkipsTheAmendReadsOnAPlainCommit(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMessage, file("seed.txt"))
	m.amendMode = false
	m.amendSHA = "stale"
	m.amendPushed = true
	m.amendStaged = []string{"stale.txt"}

	m.enterConfirm()

	if m.amendSHA != "" || m.amendPushed || m.amendStaged != nil {
		t.Fatalf("stale amend state survived: sha=%q pushed=%v staged=%v",
			m.amendSHA, m.amendPushed, m.amendStaged)
	}
	if m.step != stepConfirm {
		t.Fatalf("step = %d", m.step)
	}
}

// ── Init ───────────────────────────────────────────────

// The first frame starts a background fetch — but only when there is a remote
// to fetch from, or the spinner spins forever over nothing.
func TestInitFetchesOnlyWhenThereIsARemote(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	m := wizardModel(t, stepMenu)
	m.hasRemote = false
	if cmd := m.Init(); cmd != nil {
		t.Fatal("Init started a fetch with no remote configured")
	}

	m.hasRemote = true
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init did not start the background fetch")
	}
}

// ── recoveryError ──────────────────────────────────────

// Recovery errors wrap; they must not hide what they wrap. A caller that
// checks for a sentinel with errors.Is has to keep working after a handler
// decides the message is one the user must act on.
func TestRecoveryErrorUnwrapsToTheOriginal(t *testing.T) {
	sentinel := errors.New("the original failure")
	wrapped := recoveryError{fmt.Errorf("restoring your changes failed: %w", sentinel)}

	if !errors.Is(wrapped, sentinel) {
		t.Fatal("errors.Is cannot see through recoveryError")
	}
	if !isRecoveryError(wrapped) {
		t.Fatal("isRecoveryError does not recognise its own type")
	}
	if !strings.Contains(wrapped.Error(), "the original failure") {
		t.Fatalf("Error() = %q, want the wrapped text", wrapped.Error())
	}
	if isRecoveryError(sentinel) {
		t.Fatal("a plain error was classified as a recovery error")
	}
	if isRecoveryError(nil) {
		t.Fatal("nil was classified as a recovery error")
	}
	// And nested one level down, which is how the pull and merge handlers
	// build them.
	if !errors.Is(fmt.Errorf("outer: %w", wrapped), sentinel) {
		t.Fatal("the chain breaks when a recoveryError is wrapped again")
	}
}

// ── Fixture helpers ────────────────────────────────────

func readHead(t *testing.T) string {
	t.Helper()
	return gitLine(t, "rev-parse", "HEAD")
}

// gitLine is gitOut trimmed to a string — these assertions compare against
// exact values, not substrings of a trailing newline.
func gitLine(t *testing.T, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(gitOut(t, args...)))
}
