package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Helpers ────────────────────────────────────────────

// stashEntry builds a list row without going near git.
func stashEntry(sha, branch, subject string) git.StashEntry {
	return git.StashEntry{
		Ref:     "stash@{0}",
		SHA:     sha,
		Branch:  branch,
		Subject: subject,
		Age:     "2h ago",
	}
}

// stashModel parks a model on the stash manager with n synthetic entries.
func stashModel(t *testing.T, n int) Model {
	t.Helper()
	m := wizardModel(t, stepStash)
	for i := 0; i < n; i++ {
		m.stashEntries = append(m.stashEntries, git.StashEntry{
			Ref:     fmt.Sprintf("stash@{%d}", i),
			SHA:     fmt.Sprintf("sha%04d", i),
			Branch:  "main",
			Subject: fmt.Sprintf("entry number %d", i),
			Age:     "2h ago",
		})
	}
	m.stashCount = n
	return m
}

// stashRepo is tempRepo plus n real stash entries, so the screens that read git
// (entry, preview, the async commands) run against a real stack.
func stashRepo(t *testing.T, n int) {
	t.Helper()
	tempRepo(t, "chore: seed", "")
	for i := 0; i < n; i++ {
		if err := os.WriteFile("seed.txt", []byte(fmt.Sprintf("change %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "stash", "push", "-q",
			"--include-untracked", "-m", fmt.Sprintf("entry %d", i)).CombinedOutput(); err != nil {
			t.Fatalf("git stash push: %v\n%s", err, out)
		}
	}
}

// ── Menu visibility ────────────────────────────────────

// Hidden at zero on purpose — a beginner who has never stashed anything should
// not be asked to wonder what the word means.
func TestMenuStashEntryAppearsOnlyWithAStash(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)

	for _, item := range m.menuItems() {
		if item.name == "Stash" {
			t.Fatal("the menu offers Stash with an empty stack")
		}
	}
	if strings.Contains(renderHelpRows(m.helpRows()), "stash") {
		t.Error("the footer advertises S with an empty stack")
	}

	m.stashCount = 3
	found := false
	for _, item := range m.menuItems() {
		if item.name == "Stash" {
			found = true
			if item.desc != "3 stashed" {
				t.Errorf("the Stash entry reads %q, want %q", item.desc, "3 stashed")
			}
		}
	}
	if !found {
		t.Fatal("the menu hides Stash even though the stack is not empty")
	}
	if !strings.Contains(renderHelpRows(m.helpRows()), "stash") {
		t.Error("the footer does not advertise S with a non-empty stack")
	}
	// The count comes off the dashboard snapshot like everything else on the
	// screen — never a git call inside Update or a View func.
	if !strings.Contains(m.View(), "3 stashed") {
		t.Errorf("the dashboard does not show the count:\n%s", m.View())
	}
}

// A stash belongs to no branch, so it is the one entry that survives the
// detached-HEAD menu — which is exactly the state uncommitted work most needs a
// way out of.
func TestDetachedMenuStillOffersTheStash(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)
	m.detached = true
	m.stashCount = 1

	names := []string{}
	for _, item := range m.menuItems() {
		names = append(names, item.name)
	}
	want := []string{"Branch", "Stash", "Config"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("detached menu is %v, want %v", names, want)
	}
}

func TestDashboardSnapshotCarriesTheStashCount(t *testing.T) {
	stashRepo(t, 2)
	snap := readDashboard("main", false)
	if snap.stashCount != 2 {
		t.Fatalf("snapshot stash count = %d, want 2", snap.stashCount)
	}
	m := wizardModel(t, stepMenu)
	m.applyDashboard(snap)
	if m.stashCount != 2 {
		t.Errorf("applyDashboard left stashCount at %d", m.stashCount)
	}
}

// While the manager is open it holds the authoritative list. A snapshot taken
// before a pop would put the popped entry back into the count under the user.
func TestSnapshotDoesNotClobberTheOpenStashManager(t *testing.T) {
	m := stashModel(t, 1)
	m.applyDashboard(dashboardSnapshot{branch: "main", stashCount: 7})
	if m.stashCount != 1 {
		t.Errorf("a stale snapshot overwrote the manager's count: %d", m.stashCount)
	}
}

// ── Routing ────────────────────────────────────────────

func TestCapitalSOpensTheStashManagerFromTheMenu(t *testing.T) {
	stashRepo(t, 2)
	m := wizardModel(t, stepMenu)
	m.stashCount = 2

	m, _ = key(t, m, "S")
	if m.step != stepStash {
		t.Fatalf("S left us on step %v", m.step)
	}
	if len(m.stashEntries) != 2 {
		t.Errorf("the manager opened with %d entries, want 2", len(m.stashEntries))
	}
	// Lowercase s is the sync shortcut and must not have been shadowed.
	back := wizardModel(t, stepMenu)
	back.stashCount = 2
	back, _ = key(t, back, "s")
	if back.step == stepStash {
		t.Error("lowercase s opened the stash manager — it belongs to sync")
	}
}

func TestSDoesNothingWithAnEmptyStack(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)
	m, _ = key(t, m, "S")
	if m.step != stepMenu {
		t.Errorf("S opened the manager with nothing in it (step %v)", m.step)
	}
}

// The loop-closer, end to end: the banner a failed auto-stash pop raises says
// "press S", the key is live on the screen the banner is on, and pressing it
// opens the manager.
func TestSRoutesOutOfARecoveryError(t *testing.T) {
	stashRepo(t, 1)
	for _, tc := range []struct {
		name string
		step step
	}{
		{"menu", stepMenu},
		{"branch manager", stepBranch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := wizardModel(t, tc.step)
			m.branchEntries = git.GetAllBranches()
			// Exactly what the branch-switch handler builds.
			m.err = recoveryError{fmt.Errorf(
				"switched to feature, but your stashed changes did not apply here — the working tree was reset clean and nothing was lost. %s",
				stashRecoveryHint("abc1234"))}
			m.stashCount = 1

			if !strings.Contains(m.err.Error(), "press S to open the stash manager") {
				t.Fatalf("the banner does not point at the stash manager:\n%s", m.err)
			}
			if strings.Contains(m.err.Error(), "git stash apply") {
				t.Errorf("the banner still tells the user to leave for a terminal:\n%s", m.err)
			}
			if !strings.Contains(renderHelpRows(m.helpRows()), "stash") {
				t.Errorf("S is not in the footer of the screen the banner is on:\n%s", m.View())
			}
			if !strings.Contains(m.View(), "abc1234") {
				t.Errorf("the banner never reaches the screen:\n%s", m.View())
			}

			next, _ := key(t, m, "S")
			if next.step != stepStash {
				t.Fatalf("S did not open the manager (step %v)", next.step)
			}
			// The instruction has been followed; leaving it on screen reads as a
			// fresh failure on the very screen it pointed at.
			if next.err != nil {
				t.Errorf("the recovery banner followed us in: %v", next.err)
			}
		})
	}
}

// Every message about an orphaned auto-stash goes through one sentence, and no
// code path in internal/ui may hand out a `git stash apply` command any more —
// that instruction is the round trip this whole screen exists to end. Comments
// are exempt: they are where the old behaviour is recorded.
func TestNoRecoveryMessageSendsTheUserToATerminal(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if strings.Contains(line, "git stash apply") {
				t.Errorf("%s:%d still tells the user to run git stash apply:\n%s", name, i+1, line)
			}
		}
	}
}

// esc always lands on the dashboard, whichever screen opened the manager: an
// apply changes the working tree, and returnToMenu is what re-reads it.
func TestEscFromTheStashManagerAlwaysReturnsToTheMenu(t *testing.T) {
	stashRepo(t, 1)
	m := stashModel(t, 1)
	m.branchStandalone = true // opened from `git-assist branch`
	next, cmd := key(t, m, "esc")
	if next.step != stepMenu {
		t.Fatalf("esc left us on step %v", next.step)
	}
	if cmd == nil {
		t.Error("esc did not ask for a dashboard refresh — restored files stay invisible")
	}
}

// ── The list ───────────────────────────────────────────

func TestStashListScrollsAndMarksWhatIsOffScreen(t *testing.T) {
	m := stashModel(t, 30)
	m.height = 24 // a small window, so most of the list is off screen
	rows := m.stashListRows()
	if rows >= 30 {
		t.Fatalf("the window (%d rows) is not smaller than the list", rows)
	}

	view := m.View()
	if !strings.Contains(view, "more") {
		t.Errorf("nothing tells the user the list continues:\n%s", view)
	}

	// Walk to the bottom; the window has to follow.
	for i := 0; i < 29; i++ {
		m, _ = key(t, m, "down")
	}
	if m.stashCursor != 29 {
		t.Fatalf("cursor stopped at %d", m.stashCursor)
	}
	if m.stashScroll+rows <= m.stashCursor {
		t.Errorf("the cursor (%d) is outside the window [%d,%d)",
			m.stashCursor, m.stashScroll, m.stashScroll+rows)
	}
	if !strings.Contains(m.View(), "sha0029") {
		t.Errorf("the last entry is unreachable:\n%s", m.View())
	}
	// And down at the end is a no-op rather than an index past the slice.
	m, _ = key(t, m, "down")
	if m.stashCursor != 29 {
		t.Errorf("down past the end moved the cursor to %d", m.stashCursor)
	}
}

func TestStashRowShowsShaAgeBranchAndSubject(t *testing.T) {
	m := stashModel(t, 1)
	m.stashEntries[0] = stashEntry("abc1234", "feat", "WIP message")
	view := m.View()
	for _, want := range []string{"abc1234", "2h ago", "on feat", "WIP message"} {
		if !strings.Contains(view, want) {
			t.Errorf("the row never says %q:\n%s", want, view)
		}
	}
}

func TestStashEmptyStateExplainsWhereEntriesComeFrom(t *testing.T) {
	m := stashModel(t, 0)
	view := m.View()
	for _, want := range []string{
		"No stashes.",
		"Branch switches and merges stash automatically when needed",
		"entries appear here if restoring fails",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the empty state never says %q:\n%s", want, view)
		}
	}
	// The per-entry keys are gone with nothing to point them at.
	footer := renderHelpRows(m.helpRows())
	for _, gone := range []string{"apply", "pop", "delete"} {
		if strings.Contains(footer, gone) {
			t.Errorf("the empty state still offers %q: %s", gone, footer)
		}
	}
}

func TestStashKeysOnAnEmptyListDoNotPanic(t *testing.T) {
	m := stashModel(t, 0)
	for _, k := range []string{"up", "down", "enter", "d", "a", "p", "x", "j", "k"} {
		m, _ = key(t, m, k)
	}
	if m.step != stepStash {
		t.Errorf("a key on the empty list navigated away (step %v)", m.step)
	}
	if m.stashConfirmDrop {
		t.Error("x opened a delete confirmation with nothing to delete")
	}
}

// ── Preview ────────────────────────────────────────────

func TestPreviewTogglesAndScrolls(t *testing.T) {
	stashRepo(t, 1)
	m := wizardModel(t, stepMenu)
	m.stashCount = 1
	m, _ = key(t, m, "S")

	m, _ = key(t, m, "enter")
	if !m.stashShowDiff {
		t.Fatal("enter did not open the preview")
	}
	if !strings.Contains(m.stashDiff, "seed.txt") {
		t.Errorf("the preview holds no patch:\n%s", m.stashDiff)
	}
	if !strings.Contains(m.View(), "Lines 1-") {
		t.Errorf("the preview has no line counter:\n%s", m.View())
	}

	// Long enough to scroll, so the bound is exercised rather than assumed.
	m.stashDiff = strings.Repeat("+line\n", 200)
	m, _ = key(t, m, "down")
	if m.stashDiffScroll != 1 {
		t.Errorf("down did not scroll the preview (%d)", m.stashDiffScroll)
	}
	m, _ = key(t, m, "up")
	m, _ = key(t, m, "up")
	if m.stashDiffScroll != 0 {
		t.Errorf("up scrolled past the top (%d)", m.stashDiffScroll)
	}

	m, _ = key(t, m, "d")
	if m.stashShowDiff {
		t.Error("d did not close the preview")
	}
}

// ── Apply / pop / drop ─────────────────────────────────

func TestApplyAndPopReportWhatHappened(t *testing.T) {
	cases := []struct {
		name string
		key  string
		msg  stashApplyResultMsg
		want []string
	}{
		{
			name: "apply keeps the entry",
			key:  "a",
			msg:  stashApplyResultMsg{sha: "abc1234", files: 3, entries: []git.StashEntry{stashEntry("abc1234", "main", "x")}},
			want: []string{"Applied stash abc1234", "3 files restored to the working tree", "still in the stash list"},
		},
		{
			name: "pop removes it",
			key:  "p",
			msg:  stashApplyResultMsg{sha: "abc1234", popped: true, files: 1, entries: nil},
			want: []string{"Popped stash abc1234", "1 file restored", "removed from the stash list"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := stashModel(t, 1)
			m.stashEntries[0] = stashEntry("abc1234", "main", "x")

			m, cmd := key(t, m, tc.key)
			if !m.stashApplying {
				t.Fatal("the operation was not marked in flight")
			}
			if cmd == nil {
				t.Fatal("no command was dispatched")
			}
			// Single-shot: a second press must not dispatch a second apply
			// against a stack the first one is about to renumber.
			if _, again := key(t, m, tc.key); again != nil {
				t.Error("a second keypress dispatched another operation")
			}

			next, _ := m.Update(tc.msg)
			out := next.(Model)
			if out.stashApplying {
				t.Error("the in-flight flag survived the result")
			}
			view := out.View()
			for _, want := range tc.want {
				if !strings.Contains(view, want) {
					t.Errorf("the note never says %q:\n%s", want, view)
				}
			}
			if out.stashCount != len(tc.msg.entries) {
				t.Errorf("stashCount is %d, want %d", out.stashCount, len(tc.msg.entries))
			}
		})
	}
}

// The three ways an apply can fail describe three different repositories, and
// the whole value of the screen is telling them apart truthfully. Each clause
// below is asserted against real git in internal/git/stash_test.go.
func TestApplyFailureMessagesAreTrue(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		want   []string
		reject []string
	}{
		{
			name: "conflict",
			err:  &git.StashConflictError{Files: []string{"a.txt", "b.txt"}},
			want: []string{
				"conflicted",
				// The files by name, not git's ten-line status report.
				"conflict markers are now in a.txt, b.txt",
				"the stash itself was kept",
			},
			reject: []string{"no changes added to commit"},
		},
		{
			name: "conflict without a file list",
			err:  fmt.Errorf("%w", git.ErrStashConflict),
			want: []string{"conflicted", "conflict markers are now in the files it touches"},
		},
		{
			name: "refused",
			err:  fmt.Errorf("%w: error: Your local changes would be overwritten by merge", git.ErrStashDirtyTree),
			want: []string{
				"uncommitted changes in your working tree cover the same files",
				"git stopped before touching anything",
			},
			// git refused before touching a file — there is nothing to resolve.
			reject: []string{"conflict markers"},
		},
		{
			name:   "gone",
			err:    git.ErrNoSuchStash,
			want:   []string{"no longer in the list"},
			reject: []string{"conflict markers"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := stashModel(t, 1)
			next, _ := m.Update(stashApplyResultMsg{err: tc.err, sha: "abc1234"})
			out := next.(Model)
			if out.err == nil {
				t.Fatal("the failure was not surfaced at all")
			}
			// Asserted on the message, not the rendered box: the box wraps long
			// lines, and this test is about what is said, not where it breaks.
			msg := out.err.Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("the error never says %q:\n%s", want, msg)
				}
			}
			for _, no := range tc.reject {
				if strings.Contains(msg, no) {
					t.Errorf("the error wrongly claims %q:\n%s", no, msg)
				}
			}
			// It has to reach the screen, whatever the wrapping does to it.
			if !strings.Contains(out.View(), "abc1234") {
				t.Errorf("the failure never names the entry on screen:\n%s", out.View())
			}
		})
	}
}

// A conflicted apply is a recovery error: the banner names files the user has
// to go and fix, and an arrow key must not wipe it.
func TestAConflictedApplyStaysOnScreen(t *testing.T) {
	m := stashModel(t, 1)
	next, _ := m.Update(stashApplyResultMsg{
		err: &git.StashConflictError{Files: []string{"a.txt"}}, sha: "abc1234",
	})
	out := next.(Model)
	if !isRecoveryError(out.err) {
		t.Fatalf("a conflicted apply is not a recovery error: %v", out.err)
	}
	out, _ = key(t, out, "down")
	if out.err == nil {
		t.Error("an arrow key wiped the conflict banner")
	}
	out, _ = key(t, out, "esc")
	if out.err != nil {
		t.Error("esc did not dismiss the banner")
	}
}

func TestDropAsksFirstAndSaysWhatItCosts(t *testing.T) {
	m := stashModel(t, 2)
	m.stashEntries[0] = stashEntry("abc1234", "main", "x")

	m, cmd := key(t, m, "x")
	if !m.stashConfirmDrop {
		t.Fatal("x did not open a confirmation")
	}
	if cmd != nil {
		t.Fatal("x dispatched the delete without asking")
	}
	view := m.View()
	for _, want := range []string{
		"Delete stash abc1234?",
		"permanently deletes these stashed changes",
		"not in any commit",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the confirmation never says %q:\n%s", want, view)
		}
	}

	// Anything but y cancels.
	cancelled, cmd := key(t, m, "n")
	if cancelled.stashConfirmDrop || cmd != nil {
		t.Error("a key other than y went ahead with the delete")
	}

	confirmed, cmd := key(t, m, "y")
	if cmd == nil {
		t.Fatal("y did not dispatch the delete")
	}
	if !confirmed.stashDropping {
		t.Fatal("the delete was not marked in flight")
	}
	if _, again := key(t, confirmed, "y"); again != nil {
		t.Error("a second y dispatched another delete")
	}

	next, _ := confirmed.Update(stashDropResultMsg{
		sha: "abc1234", entries: []git.StashEntry{stashEntry("def5678", "main", "y")},
	})
	out := next.(Model)
	if !strings.Contains(out.View(), "Deleted stash abc1234") {
		t.Errorf("the delete was not reported:\n%s", out.View())
	}
	if out.stashCount != 1 {
		t.Errorf("stashCount is %d, want 1", out.stashCount)
	}
}

// A pop empties the list under the cursor; the next frame indexes it.
func TestResultClampsTheCursorOntoTheShorterList(t *testing.T) {
	m := stashModel(t, 3)
	m.stashCursor = 2
	m.stashScroll = 2

	next, _ := m.Update(stashApplyResultMsg{sha: "sha0002", popped: true, entries: nil})
	out := next.(Model)
	if out.stashCursor != 0 || out.stashScroll != 0 {
		t.Fatalf("cursor/scroll left at %d/%d on an empty list", out.stashCursor, out.stashScroll)
	}
	// Rendering is where an unclamped cursor would panic.
	if !strings.Contains(out.View(), "No stashes.") {
		t.Errorf("the emptied list does not show the empty state:\n%s", out.View())
	}
}

// ── Against real git, end to end ───────────────────────

// The refs the manager operates on come from a fresh listing, never from the
// list on screen: dropping the top entry renumbers everything below it.
func TestManagerOperatesOnTheCurrentRefsNotTheOnesOnScreen(t *testing.T) {
	stashRepo(t, 3) // entries 0,1,2 — newest ("entry 2") on top

	m := wizardModel(t, stepMenu)
	m.stashCount = 3
	m, _ = key(t, m, "S")
	if len(m.stashEntries) != 3 {
		t.Fatalf("the manager opened with %d entries", len(m.stashEntries))
	}
	middle := m.stashEntries[1]
	if middle.Subject != "entry 1" {
		t.Fatalf("fixture order is wrong: %#v", m.stashEntries)
	}

	// Drop the top entry OUTSIDE the app, exactly as a second terminal would.
	// "entry 1" is stash@{0} now, but the model still calls it stash@{1}.
	if out, err := exec.Command("git", "stash", "drop", "stash@{0}").CombinedOutput(); err != nil {
		t.Fatalf("git stash drop: %v\n%s", err, out)
	}

	// Apply the middle entry through the app and run the command for real.
	m.stashCursor = 1
	m, cmd := key(t, m, "a")
	if cmd == nil {
		t.Fatal("a dispatched nothing")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected a batch, got %T", msg)
	}
	var res stashApplyResultMsg
	for _, c := range batch {
		if r, ok := c().(stashApplyResultMsg); ok {
			res = r
		}
	}
	if res.err != nil {
		t.Fatalf("apply failed: %v", res.err)
	}
	body, err := os.ReadFile("seed.txt")
	if err != nil {
		t.Fatal(err)
	}
	// stash@{1} at dispatch time is a different entry from stash@{1} now. Only
	// re-resolving by SHA restores the one the cursor was on.
	if string(body) != "change 1\n" {
		t.Errorf("the wrong entry was applied: seed.txt = %q, want %q", body, "change 1\n")
	}
	if len(res.entries) != 2 {
		t.Errorf("the result carries %d entries, want the 2 still in the stack", len(res.entries))
	}
}

func TestManagerReportsAStashThatVanished(t *testing.T) {
	stashRepo(t, 1)
	m := wizardModel(t, stepMenu)
	m.stashCount = 1
	m, _ = key(t, m, "S")

	if out, err := exec.Command("git", "stash", "drop", "stash@{0}").CombinedOutput(); err != nil {
		t.Fatalf("git stash drop: %v\n%s", err, out)
	}

	m, cmd := key(t, m, "p")
	batch := cmd().(tea.BatchMsg)
	var res stashApplyResultMsg
	for _, c := range batch {
		if r, ok := c().(stashApplyResultMsg); ok {
			res = r
		}
	}
	if !errors.Is(res.err, git.ErrNoSuchStash) {
		t.Fatalf("popping a vanished entry returned %v", res.err)
	}
	next, _ := m.Update(res)
	out := next.(Model)
	if !strings.Contains(out.View(), "no longer in the list") {
		t.Errorf("the disappearance was not explained:\n%s", out.View())
	}
	if out.stashCount != 0 {
		t.Errorf("stashCount is %d after the list came back empty", out.stashCount)
	}
}
