package ui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Fixtures ───────────────────────────────────────────

// conflictFixture builds a repository whose `feat` branch cannot be merged into
// `main` without two decisions:
//
//	shared.txt  UU — both sides rewrote the same line
//	gone.txt    UD — main changed it, feat deleted it
//
// Nothing is merged yet; the caller decides how the merge is started, because
// how it starts is exactly what several of these tests are about.
func conflictFixture(t *testing.T) {
	t.Helper()
	tempRepo(t, "chore: seed", "")
	writeFile(t, "shared.txt", "base\n")
	writeFile(t, "gone.txt", "base\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "add files")

	gitRun(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "shared.txt", "from feat\n")
	gitRun(t, "rm", "-q", "gone.txt")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "feat: rework")

	gitRun(t, "checkout", "-q", "main")
	writeFile(t, "shared.txt", "from main\n")
	writeFile(t, "gone.txt", "main changed it\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "chore: rework")
}

// conflictedModel runs the branch-manager merge for real and feeds its result
// through Update — i.e. it lands on the resolver the way a user does.
func conflictedModel(t *testing.T) Model {
	t.Helper()
	msg := doMergeBranch("feat")().(branchMergeResultMsg)
	if msg.err == nil {
		t.Fatal("the fixture merge did not conflict")
	}
	m := wizardModel(t, stepBranch)
	next, _ := m.Update(msg)
	out := next.(Model)
	if out.step != stepConflicts {
		t.Fatalf("a conflicting merge landed on step %v, want stepConflicts", out.step)
	}
	return out
}

// mergeInProgressOnDisk asks git, not the model.
func mergeInProgressOnDisk(t *testing.T) bool {
	t.Helper()
	return exec.Command("git", "rev-parse", "-q", "--verify", "MERGE_HEAD").Run() == nil
}

// drain runs a command (unwrapping a batch) and returns every message it
// produced, so a test can pick the one it cares about.
func drain(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command was dispatched")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		out = append(out, c())
	}
	return out
}

// press feeds a key and runs whatever it dispatched back through Update, which
// is what the Bubble Tea loop does. Returns the model after the result landed.
func press(t *testing.T, m Model, k string) Model {
	t.Helper()
	m, cmd := key(t, m, k)
	if cmd == nil {
		return m
	}
	for _, msg := range drain(t, cmd) {
		switch msg.(type) {
		case conflictResolveResultMsg, conflictSaveResultMsg,
			conflictFinishResultMsg, conflictAbortResultMsg:
			next, _ := m.Update(msg)
			m = next.(Model)
		}
	}
	return m
}

// pressAbort takes the two keys `a` costs now: the key itself, then the y of
// the confirmation it raises. Asserting the prompt appeared is half the point —
// every call site here would silently pass again if the confirm went away.
func pressAbort(t *testing.T, m Model) Model {
	t.Helper()
	m = press(t, m, "a")
	if !m.conflictConfirmAbort {
		t.Fatal("a did not raise the abort confirmation")
	}
	if m.conflictAborting {
		t.Fatal("a started the abort without asking")
	}
	return press(t, m, "y")
}

func conflictPaths(m Model) []string {
	var out []string
	for _, r := range m.conflictRows {
		mark := " "
		if r.resolved {
			mark = "+"
		}
		out = append(out, mark+r.file.Path)
	}
	return out
}

// ── The interception ───────────────────────────────────

// The regression this whole feature is: a conflicting merge used to run
// `git merge --abort` on the spot and tell the user to finish in a terminal.
// It must now stop in the resolver with the merge STILL on disk.
func TestConflictingMergeIsNotAbortedAnyMore(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)

	if !mergeInProgressOnDisk(t) {
		t.Fatal("the merge was aborted — the resolver has nothing to resolve")
	}
	if !m.mergeInProgress {
		t.Error("the model does not know a merge is in progress")
	}
	if got := len(m.conflictRows); got != 2 {
		t.Fatalf("the resolver lists %d files, want 2: %v", got, conflictPaths(m))
	}
	if m.conflictSource != "feat" || m.conflictTarget != "main" {
		t.Errorf("labels = %q → %q, want feat → main", m.conflictSource, m.conflictTarget)
	}

	// Everything the screen has to explain to someone who has never seen a
	// conflict before.
	out := m.View()
	for _, want := range []string{
		"Merging feat into main stopped",
		"2 files have conflicting changes",
		"git needs you to pick which version to keep",
		"0 of 2 resolved",
		"both changed it",                 // shared.txt
		"you changed it, feat deleted it", // gone.txt
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the resolver never says %q:\n%s", want, out)
		}
	}
	// The old advice must be gone with the old behavior.
	for _, gone := range []string{"merge aborted", "Resolve in your terminal", "git merge --continue"} {
		if strings.Contains(out, gone) {
			t.Errorf("the screen still tells the user to %q", gone)
		}
	}
}

// A conflicting pull is the same interception, worded for the operation the
// user actually asked for.
func TestConflictingPullRoutesToTheResolver(t *testing.T) {
	conflictFixture(t)
	// A pull is a merge from origin; the branch-manager merge produces the same
	// mid-merge state, so drive the handler with the message the pull sends.
	if _, err := git.MergeBranch("feat"); err == nil {
		t.Fatal("the fixture merge did not conflict")
	}
	m := wizardModel(t, stepSync)
	m.syncMainBranchName = "main"
	next, _ := m.Update(pullResultMsg{
		err:           errors.New("CONFLICT (content): Merge conflict in shared.txt"),
		conflictFiles: git.GetConflictFiles(),
		kind:          pullKindMain,
	})
	got := next.(Model)

	if got.step != stepConflicts {
		t.Fatalf("a conflicting sync landed on %v, want stepConflicts", got.step)
	}
	if !mergeInProgressOnDisk(t) {
		t.Error("the sync aborted the merge")
	}
	if got.conflictSource != "origin/main" {
		t.Errorf("source = %q, want origin/main", got.conflictSource)
	}
	if out := got.View(); !strings.Contains(out, "Syncing main with origin/main stopped") {
		t.Errorf("the header does not name the operation:\n%s", out)
	}
	// The dialog it came from must be gone, not merely covered.
	if got.syncSyncMain || got.syncPullCurrent {
		t.Error("the sync dialog's state survived the conflict")
	}
}

// ── Resolving ──────────────────────────────────────────

func TestKeepOursAndTakeTheirsRouteToTheRightSide(t *testing.T) {
	cases := []struct {
		key     string
		path    string
		content string // "" = the file must be gone
		note    string
	}{
		{"o", "shared.txt", "from main\n", "kept your version"},
		{"t", "shared.txt", "from feat\n", "took feat's version"},
		// The delete variant: main changed gone.txt, feat deleted it.
		{"o", "gone.txt", "main changed it\n", "kept your version"},
		{"t", "gone.txt", "", "deleted the file"},
	}
	for _, c := range cases {
		t.Run(c.key+" "+c.path, func(t *testing.T) {
			conflictFixture(t)
			m := conflictedModel(t)
			m.conflictCursor = indexOfConflict(t, m, c.path)

			m = press(t, m, c.key)
			if m.err != nil {
				t.Fatalf("resolving failed: %v", m.err)
			}
			row := conflictRowFor(t, m, c.path)
			if !row.resolved {
				t.Fatalf("%s is still unresolved after %q", c.path, c.key)
			}
			if row.how != c.note {
				t.Errorf("row says %q, want %q", row.how, c.note)
			}
			data, err := os.ReadFile(c.path)
			if c.content == "" {
				if err == nil {
					t.Errorf("%s survived (%q) — this side deletes it", c.path, data)
				}
			} else if err != nil || string(data) != c.content {
				t.Errorf("%s = %q (err %v), want %q", c.path, data, err, c.content)
			}
			if !strings.Contains(m.View(), symDone) {
				t.Error("the resolved row has no ✓")
			}
		})
	}
}

// Resolved files leave the decision list, keep their place in the count, and
// take their per-file keys with them: once a version is staged, the index no
// longer holds two sides and `git checkout --ours` fails.
func TestResolvedFilesMoveToTheirOwnSectionAndKeepCounting(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)
	m.conflictCursor = indexOfConflict(t, m, "gone.txt")
	m = press(t, m, "o")

	if got := conflictPaths(m); got[0] != " shared.txt" || got[1] != "+gone.txt" {
		t.Errorf("rows = %v, want the unresolved one first", got)
	}
	if m.conflictResolvedCount() != 1 || m.conflictUnresolved() != 1 {
		t.Errorf("counted %d resolved / %d left, want 1 / 1",
			m.conflictResolvedCount(), m.conflictUnresolved())
	}
	out := m.View()
	if !strings.Contains(out, "1 of 2 resolved") {
		t.Errorf("the header does not count down:\n%s", out)
	}
	for _, want := range []string{"Needs a decision", "Resolved"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list has no %q section:\n%s", want, out)
		}
	}

	// Park the cursor on the resolved row: o/t/e/m are neither offered nor live.
	m.conflictCursor = indexOfConflict(t, m, "gone.txt")
	if m.conflictActionable() {
		t.Error("a resolved row still claims to be actionable")
	}
	footer := renderHelpRows(m.helpRows())
	for _, gone := range []string{"keep yours", "take theirs", "mark resolved"} {
		if strings.Contains(footer, gone) {
			t.Errorf("the footer offers %q on a resolved file: %s", gone, footer)
		}
	}
	before := m.conflictRows
	if after, cmd := key(t, m, "o"); cmd != nil || len(after.conflictRows) != len(before) {
		t.Error("o on a resolved row dispatched something")
	}
}

// c is refused until every file has been decided — and says how many are left
// rather than doing nothing.
func TestContinueIsRefusedWhileFilesAreUndecided(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)

	m, cmd := key(t, m, "c")
	if cmd != nil {
		t.Fatal("c started the commit with conflicts still open")
	}
	if m.err == nil || !strings.Contains(m.err.Error(), "still to decide") {
		t.Errorf("err = %v, want a count of what is left", m.err)
	}
	if strings.Contains(renderHelpRows(m.helpRows()), "finish the merge") {
		t.Error("the footer offers c while files are undecided")
	}
	if !mergeInProgressOnDisk(t) {
		t.Error("a refused continue disturbed the merge")
	}
}

// ── Finishing and aborting ─────────────────────────────

// The whole point of the feature: a conflicting merge can now be COMPLETED.
func TestContinueCommitsTheMerge(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)
	m.conflictCursor = indexOfConflict(t, m, "shared.txt")
	m = press(t, m, "o")
	m.conflictCursor = indexOfConflict(t, m, "gone.txt")
	m = press(t, m, "t")
	if !m.conflictDone() {
		t.Fatalf("files still undecided: %v", conflictPaths(m))
	}
	if !strings.Contains(renderHelpRows(m.helpRows()), "finish the merge") {
		t.Error("the footer does not offer c once everything is resolved")
	}

	m = press(t, m, "c")
	if m.err != nil {
		t.Fatalf("finishing the merge failed: %v", m.err)
	}
	if mergeInProgressOnDisk(t) {
		t.Fatal("the merge is still in progress after c")
	}
	if m.mergeInProgress {
		t.Error("the model still thinks a merge is running")
	}
	if m.step != stepMenu {
		t.Errorf("step = %v after finishing, want the dashboard", m.step)
	}
	if !strings.Contains(m.statusNote, "Merged feat into main") ||
		!strings.Contains(m.statusNote, "2 conflicts resolved") {
		t.Errorf("note = %q, want the merge and the count", m.statusNote)
	}
	// Two parents, and the resolutions the user chose.
	parents := strings.Fields(string(gitOut(t, "rev-list", "--parents", "-n", "1", "HEAD")))
	if len(parents) != 3 {
		t.Errorf("HEAD has %d parent(s), want 2", len(parents)-1)
	}
	if got := readFile(t, "shared.txt"); got != "from main\n" {
		t.Errorf("shared.txt = %q, want the version kept with o", got)
	}
	if _, err := os.Stat("gone.txt"); err == nil {
		t.Error("gone.txt is back — t on a modify/delete conflict deletes it")
	}
	if out := string(gitOut(t, "status", "--porcelain")); strings.TrimSpace(out) != "" {
		t.Errorf("the tree is not clean after the merge commit:\n%s", out)
	}
}

// a is the old auto-abort, one keypress away, and it says the honest thing.
func TestAbortUndoesTheWholeMerge(t *testing.T) {
	conflictFixture(t)
	before := strings.TrimSpace(string(gitOut(t, "rev-parse", "HEAD")))
	m := conflictedModel(t)
	m.conflictCursor = indexOfConflict(t, m, "shared.txt")
	m = press(t, m, "o") // a decision that must be thrown away with the rest

	m = pressAbort(t, m)
	if m.err != nil {
		t.Fatalf("abort failed: %v", m.err)
	}
	if mergeInProgressOnDisk(t) {
		t.Fatal("the merge survived the abort")
	}
	if m.step != stepMenu || m.mergeInProgress {
		t.Errorf("step = %v, mergeInProgress = %v after aborting", m.step, m.mergeInProgress)
	}
	if m.statusNote != "Merge aborted — the repository is back to before the merge" {
		t.Errorf("note = %q", m.statusNote)
	}
	if got := strings.TrimSpace(string(gitOut(t, "rev-parse", "HEAD"))); got != before {
		t.Error("HEAD moved despite the abort")
	}
	if got := readFile(t, "shared.txt"); got != "from main\n" {
		t.Errorf("shared.txt = %q, want main's committed content", got)
	}
	if out := string(gitOut(t, "status", "--porcelain")); strings.TrimSpace(out) != "" {
		t.Errorf("the abort left the tree dirty:\n%s", out)
	}
}

// ── The auto-stash round trip ──────────────────────────

// The ordering decision, asserted end to end. A dirty tree is stashed before
// the merge; `git stash pop` is impossible while the index is unmerged, so the
// entry must sit untouched for the whole resolution and come back only after
// the merge commit.
func TestAutoStashSurvivesAConflictedMergeAndComesBackAfterContinue(t *testing.T) {
	conflictFixture(t)
	writeFile(t, "notes.txt", "in progress\n") // unrelated uncommitted work

	msg := doMergeBranch("feat")().(branchMergeResultMsg)
	if msg.err == nil || !msg.stashed {
		t.Fatalf("fixture: err=%v stashed=%v — want a conflict on a stashed tree", msg.err, msg.stashed)
	}
	m := wizardModel(t, stepBranch)
	next, _ := m.Update(msg)
	got := next.(Model)

	// Mid-merge: the stash is still in the stack, untouched, and the screen
	// says where the user's missing work went.
	if got.step != stepConflicts {
		t.Fatalf("step = %v, want stepConflicts", got.step)
	}
	if !got.conflictStashed || got.conflictStashRef == "" {
		t.Error("the pending auto-stash was not handed to the resolver")
	}
	if n := stashDepth(t); n != 1 {
		t.Fatalf("%d stash entries mid-merge, want the auto-stash still parked", n)
	}
	if _, err := os.Stat("notes.txt"); err == nil {
		t.Error("notes.txt is in the tree — it should be in the stash until the merge ends")
	}
	if out := got.View(); !strings.Contains(out, "parked in a stash") {
		t.Errorf("the screen does not explain where the uncommitted work went:\n%s", out)
	}

	got.conflictCursor = indexOfConflict(t, got, "shared.txt")
	got = press(t, got, "t")
	got.conflictCursor = indexOfConflict(t, got, "gone.txt")
	got = press(t, got, "o")
	got = press(t, got, "c")

	if got.err != nil {
		t.Fatalf("finishing failed: %v", got.err)
	}
	if n := stashDepth(t); n != 0 {
		t.Errorf("%d stash entries left after the merge — the pop did not run", n)
	}
	if content := readFile(t, "notes.txt"); content != "in progress\n" {
		t.Errorf("uncommitted work not restored: notes.txt = %q", content)
	}
	if !strings.Contains(got.statusNote, "stashed and restored") {
		t.Errorf("note = %q, want the stash round trip disclosed", got.statusNote)
	}
}

// The same round trip through the other exit.
func TestAutoStashComesBackAfterAbort(t *testing.T) {
	conflictFixture(t)
	writeFile(t, "notes.txt", "in progress\n")

	msg := doMergeBranch("feat")().(branchMergeResultMsg)
	if msg.err == nil || !msg.stashed {
		t.Fatal("fixture: want a conflict on a stashed tree")
	}
	m := wizardModel(t, stepBranch)
	next, _ := m.Update(msg)
	got := pressAbort(t, next.(Model))

	if got.err != nil {
		t.Fatalf("abort failed: %v", got.err)
	}
	if n := stashDepth(t); n != 0 {
		t.Errorf("%d stash entries left after aborting", n)
	}
	if content := readFile(t, "notes.txt"); content != "in progress\n" {
		t.Errorf("uncommitted work lost: notes.txt = %q", content)
	}
	if !strings.Contains(got.statusNote, "restored") {
		t.Errorf("note = %q, want the stash mentioned", got.statusNote)
	}
	if mergeInProgressOnDisk(t) {
		t.Error("the merge survived")
	}
}

// ── The editor ─────────────────────────────────────────

func TestEditorOpensTheConflictedFileWithItsMarkers(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)
	m.conflictCursor = indexOfConflict(t, m, "shared.txt")

	m, _ = key(t, m, "e")
	if !m.editMode || m.conflictEditPath != "shared.txt" {
		t.Fatalf("e did not open the editor (editMode=%v path=%q)", m.editMode, m.conflictEditPath)
	}
	if !git.HasConflictMarkers(m.editArea.Value()) {
		t.Fatalf("the buffer has no conflict markers:\n%s", m.editArea.Value())
	}
	// The legend, because the markers are the thing a beginner has never seen.
	out := m.View()
	for _, want := range []string{"<<<<<<< yours", ">>>>>>> theirs", "delete the markers"} {
		if !strings.Contains(out, want) {
			t.Errorf("the editor never explains %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "ctrl+s") {
		t.Errorf("the editor does not say how to save:\n%s", out)
	}
}

// A save is not a resolution: half-finished states are normal, and staging one
// silently would commit it. The trailing newline has to survive the round trip
// too — the editor's whole contract with the file on disk.
func TestSavingDoesNotMarkResolvedAndKeepsTheTrailingNewline(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)
	m.conflictCursor = indexOfConflict(t, m, "shared.txt")
	m, _ = key(t, m, "e")

	m.editArea.SetValue("merged by hand\n")
	m.editDirty = true
	m, cmd := key(t, m, "ctrl+s")
	if !m.saving {
		t.Fatal("ctrl+s did not start a save")
	}
	for _, msg := range drain(t, cmd) {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	if m.err != nil {
		t.Fatalf("save failed: %v", m.err)
	}
	if got := readFile(t, "shared.txt"); got != "merged by hand\n" {
		t.Errorf("shared.txt = %q — the trailing newline was not preserved", got)
	}
	if row := conflictRowFor(t, m, "shared.txt"); row.resolved {
		t.Error("saving marked the file resolved on its own")
	}
	if !strings.Contains(m.statusNote, "press m to mark it resolved") {
		t.Errorf("note = %q, want the next step spelled out", m.statusNote)
	}

	// m completes it — no marker warning, because the markers are gone.
	m = press(t, m, "m")
	if m.conflictMarkWarn {
		t.Fatal("a clean file raised the marker warning")
	}
	row := conflictRowFor(t, m, "shared.txt")
	if !row.resolved || row.how != "edited and marked resolved" {
		t.Errorf("row = %+v, want it resolved by hand", row)
	}
}

// Saving with the fences still in is allowed — and `m` then asks once before
// committing a file full of `<<<<<<<`.
func TestMarkWarnsWhileConflictMarkersAreStillPresent(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)
	m.conflictCursor = indexOfConflict(t, m, "shared.txt")

	m, cmd := key(t, m, "m")
	if cmd != nil {
		t.Fatal("m staged a file that still has markers without asking")
	}
	if !m.conflictMarkWarn || m.conflictMarkPath != "shared.txt" {
		t.Fatalf("no warning raised (warn=%v path=%q)", m.conflictMarkWarn, m.conflictMarkPath)
	}
	out := m.View()
	for _, want := range []string{"still has conflict markers", "Mark it resolved anyway?", "(y/N)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the warning never says %q:\n%s", want, out)
		}
	}
	if footer := renderHelpRows(m.helpRows()); !strings.Contains(footer, "mark resolved anyway") {
		t.Errorf("the footer does not offer the confirmation: %s", footer)
	}

	// Anything but y backs out, and the file stays unresolved.
	back, _ := key(t, m, "n")
	if back.conflictMarkWarn || conflictRowFor(t, back, "shared.txt").resolved {
		t.Error("n went through with the mark")
	}

	// y goes ahead, markers and all — the user was told.
	m = press(t, m, "y")
	if m.conflictMarkWarn {
		t.Error("the prompt is still up")
	}
	if !conflictRowFor(t, m, "shared.txt").resolved {
		t.Error("y did not mark the file resolved")
	}
}

// An empty list means two completely different things — "nothing is conflicted
// any more" and "git status failed" — and taking the second for the first would
// tick every file resolved and offer to commit an unmerged merge.
func TestAFailedListingIsNotMistakenForNothingLeft(t *testing.T) {
	m := conflictScreenModel(t, false)
	next, _ := m.Update(conflictResolveResultMsg{
		err:    errors.New("git status: exit status 128"),
		path:   "shared.txt",
		files:  nil,
		listed: false,
	})
	got := next.(Model)
	if got.conflictResolvedCount() != 0 {
		t.Errorf("%d files marked resolved by a failed listing", got.conflictResolvedCount())
	}
	if got.conflictDone() {
		t.Error("the screen would offer to commit the merge")
	}
	if got.err == nil {
		t.Error("the failure was swallowed")
	}
}

// ── Startup detection ──────────────────────────────────

// Quitting mid-resolution leaves the merge on disk. The next launch has to open
// on it rather than on a dashboard whose every entry is unsafe.
func TestRelaunchPicksUpAMergeLeftByThisApp(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)
	if !mergeInProgressOnDisk(t) {
		t.Fatal("fixture")
	}
	_ = m // the process "quits" here

	relaunched := NewModel(nil, "main")
	if relaunched.step != stepConflicts {
		t.Fatalf("a relaunch landed on %v, want stepConflicts", relaunched.step)
	}
	if len(relaunched.conflictRows) != 2 {
		t.Errorf("the resolver lists %v", conflictPaths(relaunched))
	}
	relaunched.width, relaunched.height = 120, 40
	if out := relaunched.View(); !strings.Contains(out, "unfinished merge from earlier") {
		t.Errorf("the banner does not say where this merge came from:\n%s", out)
	}
}

// The same detection for a merge git-assist never started: labels come from
// MERGE_MSG, which is the only record of what was being merged.
func TestStartupPicksUpAMergeStartedInATerminal(t *testing.T) {
	conflictFixture(t)
	// Exactly what the user would have typed, outside the app.
	exec.Command("git", "merge", "feat").Run()
	if !mergeInProgressOnDisk(t) {
		t.Fatal("the external merge did not conflict")
	}

	m := NewModel(nil, "main")
	m.width, m.height = 120, 40
	if m.step != stepConflicts {
		t.Fatalf("step = %v, want stepConflicts", m.step)
	}
	if m.conflictSource != "feat" {
		t.Errorf("source = %q, want the ref MERGE_MSG names", m.conflictSource)
	}
	if m.conflictTarget != "main" {
		t.Errorf("target = %q, want the current branch", m.conflictTarget)
	}
	if m.conflictStashed {
		t.Error("a merge this app did not start cannot have a pending auto-stash")
	}
	if out := m.View(); !strings.Contains(out, "Merging feat into main stopped") {
		t.Errorf("the header does not describe the external merge:\n%s", out)
	}
	// `git-assist branch` must not walk into the branch manager on top of it.
	if b := NewBranchModel("main"); b.step != stepConflicts {
		t.Errorf("the branch subcommand opened %v during a merge", b.step)
	}
}

// With no MERGE_MSG to read there is no name to give, and the screen says so
// rather than inventing one.
func TestUnnamedMergeFallsBackToAGenericLabel(t *testing.T) {
	conflictFixture(t)
	exec.Command("git", "merge", "feat").Run()
	path := strings.TrimSpace(string(gitOut(t, "rev-parse", "--git-path", "MERGE_MSG")))
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove MERGE_MSG: %v", err)
	}

	m := NewModel(nil, "main")
	m.width, m.height = 120, 40
	if m.step != stepConflicts {
		t.Fatalf("step = %v, want stepConflicts", m.step)
	}
	if out := m.View(); !strings.Contains(out, "Merging the incoming branch into main stopped") {
		t.Errorf("the fallback label is missing:\n%s", out)
	}
}

// A merge whose conflicts were resolved in a terminal is not a problem to
// solve — it is a commit waiting to be made, and refusing to make it would
// leave the user with a repository this app can neither finish nor explain.
func TestAMergeAlreadyResolvedElsewhereCanStillBeFinished(t *testing.T) {
	conflictFixture(t)
	exec.Command("git", "merge", "feat").Run()
	// What the user would have typed in the other window.
	gitRun(t, "checkout", "--ours", "--", "shared.txt")
	gitRun(t, "add", "--", "shared.txt")
	gitRun(t, "rm", "-q", "--", "gone.txt")

	m := NewModel(nil, "main")
	m.width, m.height = 120, 40
	if m.step != stepConflicts {
		t.Fatalf("step = %v, want stepConflicts", m.step)
	}
	if len(m.conflictRows) != 0 {
		t.Fatalf("rows = %v, want none left", conflictPaths(m))
	}
	out := m.View()
	for _, want := range []string{"ready to finish", "No conflicting files are left", "Press c to finish"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen never says %q:\n%s", want, out)
		}
	}
	if !strings.Contains(renderHelpRows(m.helpRows()), "finish the merge") {
		t.Error("the footer does not offer c")
	}

	m = press(t, m, "c")
	if m.err != nil {
		t.Fatalf("finishing failed: %v", m.err)
	}
	if mergeInProgressOnDisk(t) {
		t.Fatal("the merge is still in progress")
	}
	// No count to report, and "0 conflicts resolved" would read as a failure.
	if m.statusNote != "Merged feat into main" {
		t.Errorf("note = %q", m.statusNote)
	}
}

// ── Menu gating ────────────────────────────────────────

// While a merge is unfinished the dashboard carries one entry that leads back
// to the resolver, and everything that would write a commit or move HEAD
// refuses with a sentence saying why.
func TestMenuIsGatedWhileAMergeIsInProgress(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)
	m, _ = key(t, m, "esc") // the resolver's deliberate exit
	if m.step != stepMenu {
		t.Fatalf("esc went to %v, want the dashboard", m.step)
	}
	if !m.mergeInProgress {
		t.Fatal("leaving the resolver cleared the merge flag")
	}
	m.hasAnyCommit = true
	m.hasRemote = true
	m.aheadOrigin = 1
	m.hasUpstream = true

	items := m.menuItems()
	if items[0].name != "Resolve conflicts" {
		t.Fatalf("the merge entry is not first: %+v", items)
	}
	if !strings.Contains(items[0].desc, "merge in progress") {
		t.Errorf("entry desc = %q", items[0].desc)
	}

	for _, name := range []string{"Commit", "Amend", "Push", "Branch"} {
		blocked := m
		blocked.menuCursor = menuIndex(t, blocked, name)
		after, _ := key(t, blocked, "enter")
		if after.step != stepMenu {
			t.Errorf("%s opened %v during a merge", name, after.step)
		}
		if after.err == nil || !strings.Contains(after.err.Error(), "finish or abort the merge first") {
			t.Errorf("%s = %v, want the merge refusal", name, after.err)
		}
	}

	// The pull/sync shortcuts start a second merge, which git refuses outright.
	m.behindOrigin, m.behindMain, m.mainRef = 2, 2, "origin/main"
	if m.canPull() || m.canSyncMain() {
		t.Error("the dashboard still offers pull/sync during a merge")
	}
	if m.populateSyncDialog() {
		t.Error("the sync dialog would auto-open on top of an unresolved merge")
	}
	for _, k := range []string{"p", "s"} {
		if after, _ := key(t, m, k); after.step != stepMenu {
			t.Errorf("%q opened %v during a merge", k, after.step)
		}
	}
	if footer := renderHelpRows(m.helpRows()); strings.Contains(footer, "pull") || strings.Contains(footer, "sync") {
		t.Errorf("the footer advertises pull/sync during a merge: %s", footer)
	}

	// And the entry leads back in, with the labels and the stash it left with.
	m.menuCursor = menuIndex(t, m, "Resolve conflicts")
	back, _ := key(t, m, "enter")
	if back.step != stepConflicts || back.conflictSource != "feat" {
		t.Errorf("re-entry = step %v, source %q", back.step, back.conflictSource)
	}
}

// The entry survives a detached HEAD too: a merge can be started from one, and
// the detached menu's single "switch to a branch" exit is precisely the thing
// that must not happen first.
func TestTheMergeEntryIsOfferedOnADetachedHead(t *testing.T) {
	m := wizardModel(t, stepMenu)
	m.detached = true
	m.mergeInProgress = true
	items := m.menuItems()
	if items[0].name != "Resolve conflicts" {
		t.Errorf("detached menu = %+v, want the merge entry first", items)
	}
}

// A dashboard snapshot taken before the merge commit can land after it. The
// entry must not come back from the dead, and pressing it must not open an
// empty resolver.
func TestAStaleSnapshotCannotResurrectTheMergeEntry(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)
	snap := readDashboard("main", false)
	snap.mergeInProgress = true // as it was moments ago
	m.applyDashboard(snap)

	m.menuCursor = menuIndex(t, m, "Resolve conflicts")
	after, _ := key(t, m, "enter")
	if after.step != stepMenu {
		t.Errorf("an already-finished merge opened %v", after.step)
	}
	if after.mergeInProgress {
		t.Error("the flag survived a repository with no merge")
	}
	if !strings.Contains(after.statusNote, "already finished") {
		t.Errorf("note = %q", after.statusNote)
	}
}

// ── Small helpers ──────────────────────────────────────

// conflictScreenModel parks a model on the resolver with two synthetic rows —
// one both-modified, one modify/delete — without going near git. Used by the
// help-parity table, which walks every screen in the app.
func conflictScreenModel(t *testing.T, resolved bool) Model {
	t.Helper()
	m := wizardModel(t, stepConflicts)
	m.mergeInProgress = true
	m.conflictOrigin = conflictFromMerge
	m.conflictSource = "feat"
	m.conflictTarget = "main"
	m.conflictRows = []conflictRow{
		{file: git.ConflictFile{Path: "shared.txt", Code: "UU", Kind: git.ConflictBothModified}},
		{file: git.ConflictFile{Path: "gone.txt", Code: "UD", Kind: git.ConflictTheyDeleted}},
	}
	if resolved {
		for i := range m.conflictRows {
			m.conflictRows[i].resolved = true
			m.conflictRows[i].how = "kept your version"
		}
	}
	return m
}

func indexOfConflict(t *testing.T, m Model, path string) int {
	t.Helper()
	for i, r := range m.conflictRows {
		if r.file.Path == path {
			return i
		}
	}
	t.Fatalf("%s is not in the conflict list: %v", path, conflictPaths(m))
	return -1
}

func conflictRowFor(t *testing.T, m Model, path string) conflictRow {
	t.Helper()
	return m.conflictRows[indexOfConflict(t, m, path)]
}

// ── The parked stash, when the stack moves underneath ──
//
// The resolver deliberately lets the user leave (esc), and a merge can also be
// resolved across two sessions, so the stash stack is NOT frozen for the
// duration of a resolution — the app locks its own manager (see
// TestStashManagerIsReadOnlyMidMerge), but another terminal is still a thing.
// Both of these were reproduced end to end against real git before the fix:
// the pop was positional (`git stash pop` = stash@{0}, whatever that is now).

// dirtyMergeResolver runs the fixture merge on a dirty tree and lands on the
// resolver with the auto-stash parked, the way beginConflicts does.
func dirtyMergeResolver(t *testing.T) Model {
	t.Helper()
	msg := doMergeBranch("feat")().(branchMergeResultMsg)
	if msg.err == nil || !msg.stashed || msg.stashRef == "" {
		t.Fatalf("fixture: want a conflicting merge on a stashed tree (err=%v stashed=%v)", msg.err, msg.stashed)
	}
	m := wizardModel(t, stepBranch)
	next, _ := m.Update(msg)
	got := next.(Model)
	if got.step != stepConflicts || !got.conflictStashed {
		t.Fatalf("step = %v, stashed = %v", got.step, got.conflictStashed)
	}
	return got
}

// resolveEverything decides both conflicting files through the screen's keys.
func resolveEverything(t *testing.T, m Model) Model {
	t.Helper()
	m.conflictCursor = indexOfConflict(t, m, "shared.txt")
	m = press(t, m, "o")
	m.conflictCursor = indexOfConflict(t, m, "gone.txt")
	m = press(t, m, "o")
	if !m.conflictDone() {
		t.Fatalf("files left unresolved: %v", conflictPaths(m))
	}
	return m
}

// Variant A: the parked entry is gone and an UNRELATED one has taken its place
// at the top of the stack. The positional pop restored a stranger's work into
// the merged tree and reported it as "your uncommitted changes were restored".
func TestFinishNeverPopsAnUnrelatedStash(t *testing.T) {
	conflictFixture(t)
	// Someone's older stash, sitting in the stack from last week.
	writeFile(t, "seed.txt", "an unrelated stashed change\n")
	gitRun(t, "stash", "push", "-q", "-m", "unrelated")

	writeFile(t, "notes.txt", "in progress\n")
	m := dirtyMergeResolver(t)
	parked := m.conflictStashRef

	// The user (or another terminal) deletes the parked entry mid-merge. git
	// allows this, verified: stash@{0} is now the unrelated one.
	gitRun(t, "stash", "drop", "stash@{0}")
	if n := stashDepth(t); n != 1 {
		t.Fatalf("fixture: %d entries left, want the unrelated one", n)
	}

	m = resolveEverything(t, m)
	counted := m.stashCount
	m = press(t, m, "c")

	if m.err != nil {
		t.Fatalf("finishing failed: %v", m.err)
	}
	if mergeInProgressOnDisk(t) {
		t.Fatal("the merge was not committed")
	}
	if n := stashDepth(t); n != 1 {
		t.Errorf("%d stash entries left — the unrelated entry was popped into the merge", n)
	}
	if got := readFile(t, "seed.txt"); got != "seed\n" {
		t.Errorf("seed.txt = %q — somebody else's stashed work landed in the merged tree", got)
	}
	if _, err := os.Stat("notes.txt"); err == nil {
		t.Error("notes.txt is back, from an entry that was deleted")
	}
	if !strings.Contains(m.statusNote, "already applied or deleted") {
		t.Errorf("note = %q, want the missing stash disclosed", m.statusNote)
	}
	if strings.Contains(m.statusNote, "stashed and restored") {
		t.Errorf("note = %q claims a restore that did not happen", m.statusNote)
	}
	if m.stashCount != counted {
		t.Errorf("stashCount went %d → %d — a phantom orphan was counted", counted, m.stashCount)
	}
	_ = parked
}

// Variant B, the destructive one: the user resolves everything, walks out to
// the stash manager (or another terminal) and pops the parked entry themselves.
// The positional pop then failed with "No stash entries found" and
// CleanupFailedStashPop's `git checkout -- .` permanently destroyed the
// tracked-file edit that had JUST been restored — under a banner saying nothing
// was lost, naming a stash that no longer existed.
func TestFinishNeverDestroysWorkTheUserAlreadyRestored(t *testing.T) {
	conflictFixture(t)
	writeFile(t, "notes.txt", "committed content\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "add notes")
	writeFile(t, "notes.txt", "IMPORTANT UNCOMMITTED EDIT\n")

	m := resolveEverything(t, dirtyMergeResolver(t))

	// The pop the user performs themselves: it succeeds once the index has no
	// unmerged entries, and it drops the entry.
	gitRun(t, "stash", "pop")
	if got := readFile(t, "notes.txt"); got != "IMPORTANT UNCOMMITTED EDIT\n" {
		t.Fatalf("fixture: the manual pop did not restore the edit (%q)", got)
	}

	counted := m.stashCount
	m = press(t, m, "c")

	if got := readFile(t, "notes.txt"); got != "IMPORTANT UNCOMMITTED EDIT\n" {
		t.Fatalf("the finish destroyed the restored edit: notes.txt = %q", got)
	}
	if m.err != nil {
		t.Errorf("a stash that was already restored was reported as a failure: %v", m.err)
	}
	if isRecoveryError(m.err) {
		t.Error("the banner points at a stash that no longer exists")
	}
	if n := stashDepth(t); n != 0 {
		t.Errorf("%d stash entries left behind", n)
	}
	if m.stashCount != counted {
		t.Errorf("stashCount went %d → %d — the phantom orphan is back", counted, m.stashCount)
	}
	if !strings.Contains(m.statusNote, "already applied or deleted") {
		t.Errorf("note = %q, want the already-restored stash disclosed", m.statusNote)
	}
}

// The same rule on the other exit.
func TestAbortNeverDestroysWorkTheUserAlreadyRestored(t *testing.T) {
	conflictFixture(t)
	writeFile(t, "notes.txt", "committed content\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "add notes")
	writeFile(t, "notes.txt", "IMPORTANT UNCOMMITTED EDIT\n")

	m := resolveEverything(t, dirtyMergeResolver(t))
	gitRun(t, "stash", "pop")

	m = pressAbort(t, m)

	if got := readFile(t, "notes.txt"); got != "IMPORTANT UNCOMMITTED EDIT\n" {
		t.Fatalf("the abort destroyed the restored edit: notes.txt = %q", got)
	}
	if m.err != nil {
		t.Errorf("err = %v", m.err)
	}
	if !strings.Contains(m.statusNote, "already applied or deleted") {
		t.Errorf("note = %q", m.statusNote)
	}
}

// The ordinary path still restores, still says so, and still deletes nothing.
func TestFinishRestoresTheParkedEntryByItsSHA(t *testing.T) {
	conflictFixture(t)
	// A second, older entry to make the parked one NOT stash@{0}-by-luck once
	// the positional guess is gone.
	writeFile(t, "seed.txt", "unrelated\n")
	gitRun(t, "stash", "push", "-q", "-m", "unrelated")
	writeFile(t, "notes.txt", "in progress\n")

	m := dirtyMergeResolver(t)
	parked := m.conflictStashRef
	m = press(t, resolveEverything(t, m), "c")

	if m.err != nil {
		t.Fatalf("finishing failed: %v", m.err)
	}
	if got := readFile(t, "notes.txt"); got != "in progress\n" {
		t.Errorf("notes.txt = %q, want the parked work back", got)
	}
	if n := stashDepth(t); n != 1 {
		t.Errorf("%d entries left, want the unrelated one untouched", n)
	}
	if got := readFile(t, "seed.txt"); got != "seed\n" {
		t.Errorf("seed.txt = %q — the unrelated entry was applied too", got)
	}
	if !strings.Contains(m.statusNote, "stashed and restored") {
		t.Errorf("note = %q", m.statusNote)
	}
	if parked == "" {
		t.Error("the resolver never recorded which entry it parked")
	}
}

// ── Surviving a quit ───────────────────────────────────

// Quitting mid-resolution used to lose the association entirely: the recovered
// merge finished without restoring the parked changes and without mentioning
// them, so the previous session's on-screen promise was broken in silence.
func TestARecoveredMergePicksUpTheParkedStash(t *testing.T) {
	conflictFixture(t)
	writeFile(t, "notes.txt", "in progress\n")
	first := dirtyMergeResolver(t)
	parked := first.conflictStashRef

	// Quit. Everything the model knew is gone; the merge and the stash are not.
	relaunched := NewModel(nil, "main")
	relaunched.width, relaunched.height = 120, 40

	if relaunched.step != stepConflicts {
		t.Fatalf("the relaunch landed on %v, want the resolver", relaunched.step)
	}
	if !relaunched.conflictStashed || relaunched.conflictStashRef != parked {
		t.Fatalf("stashed = %v, ref = %q, want the parked %q",
			relaunched.conflictStashed, relaunched.conflictStashRef, parked)
	}
	if out := relaunched.View(); !strings.Contains(out, "parked in a stash") {
		t.Errorf("the recovered screen does not disclose the parked stash:\n%s", out)
	}
	if relaunched.conflictSource != "feat" {
		t.Errorf("source = %q, want the label the parking session recorded", relaunched.conflictSource)
	}

	done := press(t, resolveEverything(t, relaunched), "c")
	if done.err != nil {
		t.Fatalf("finishing the recovered merge failed: %v", done.err)
	}
	if got := readFile(t, "notes.txt"); got != "in progress\n" {
		t.Errorf("notes.txt = %q — the recovered merge did not restore the parked work", got)
	}
	if !strings.Contains(done.statusNote, "stashed and restored") {
		t.Errorf("note = %q", done.statusNote)
	}
	if n := stashDepth(t); n != 0 {
		t.Errorf("%d stash entries left after the recovered merge", n)
	}
}

// The record is repo-scoped state, so it has to be cleaned up like git's own.
func TestTheParkedStashRecordIsRemovedWhenTheMergeEnds(t *testing.T) {
	conflictFixture(t)
	writeFile(t, "notes.txt", "in progress\n")
	m := dirtyMergeResolver(t)

	path, err := conflictStatePath()
	if err != nil {
		t.Fatalf("conflictStatePath: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no record was written while the stash was parked: %v", err)
	}
	if !strings.HasSuffix(filepath.Dir(path), ".git") {
		t.Errorf("the record lives at %s, want it inside the git directory", path)
	}

	m = press(t, resolveEverything(t, m), "c")
	if m.err != nil {
		t.Fatalf("finishing failed: %v", m.err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the record outlived the merge it belonged to")
	}
}

// A record whose entry has since been applied or deleted must not put "your
// changes are parked in a stash" back on the screen.
func TestAStaleParkedStashRecordIsIgnored(t *testing.T) {
	conflictFixture(t)
	writeFile(t, "notes.txt", "in progress\n")
	dirtyMergeResolver(t)
	gitRun(t, "stash", "drop", "stash@{0}")

	relaunched := NewModel(nil, "main")
	relaunched.width, relaunched.height = 120, 40
	if relaunched.conflictStashed {
		t.Error("a stash that is no longer in the stack was adopted")
	}
	if out := relaunched.View(); strings.Contains(out, "parked in a stash") {
		t.Errorf("the screen promises a stash that is gone:\n%s", out)
	}
}

// ── The abort confirmation ─────────────────────────────

// `a` is select-all in the file selector and apply in the stash manager — the
// two screens that look exactly like this one. It used to throw the whole merge
// away on the first press, resolutions and all, with no confirmation anywhere.
func TestAbortAsksFirstAndSaysWhatItCosts(t *testing.T) {
	conflictFixture(t)
	m := conflictedModel(t)
	m.conflictCursor = indexOfConflict(t, m, "shared.txt")
	m = press(t, m, "o")

	confirming, cmd := key(t, m, "a")
	if cmd != nil {
		t.Fatal("a dispatched the abort instead of asking")
	}
	if !confirming.conflictConfirmAbort {
		t.Fatal("a did not raise a confirmation")
	}
	if !mergeInProgressOnDisk(t) {
		t.Fatal("the merge was aborted by the question")
	}

	out := confirming.View()
	for _, want := range []string{"Undo the whole merge?", "1 file", "no undo"} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirmation never says %q:\n%s", want, out)
		}
	}
	if footer := renderHelpRows(confirming.helpRows()); !strings.Contains(footer, "cancel") {
		t.Errorf("footer = %q, want the cancel spelled out", footer)
	}

	// Any other key cancels, and the merge (and the resolution) survive.
	cancelled, cmd := key(t, confirming, "j")
	if cmd != nil || cancelled.conflictConfirmAbort || cancelled.conflictAborting {
		t.Error("a stray key went through with the abort")
	}
	if !mergeInProgressOnDisk(t) {
		t.Fatal("the merge was aborted by a cancel")
	}
	if cancelled.conflictResolvedCount() != 1 {
		t.Errorf("%d resolutions survived the cancel, want 1", cancelled.conflictResolvedCount())
	}
}

// The parked stash is part of what the question has to disclose.
func TestAbortConfirmationDisclosesTheParkedStash(t *testing.T) {
	conflictFixture(t)
	writeFile(t, "notes.txt", "in progress\n")
	m := dirtyMergeResolver(t)
	m, _ = key(t, m, "a")
	if out := m.View(); !strings.Contains(out, "parked uncommitted changes are restored") {
		t.Errorf("the confirmation does not mention the parked stash:\n%s", out)
	}
}
