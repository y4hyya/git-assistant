package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"git-assist/internal/git"
	"git-assist/internal/types"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Helpers ────────────────────────────────────────────

// key feeds a single keypress through Update and returns the resulting model
// and command. Commands are never executed — they would run real git.
func key(t *testing.T, m Model, k string) (Model, tea.Cmd) {
	t.Helper()
	var msg tea.Msg
	switch k {
	case " ":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "ctrl+c":
		msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	next, cmd := m.Update(msg)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", next)
	}
	return out, cmd
}

func file(path string) types.FileEntry {
	return types.FileEntry{Path: path, Status: types.StatusModified}
}

// wizardModel returns a fully initialized model parked on a step. Struct
// literals leave the bubbles inputs zero-valued, and those nil-deref on
// Focus() — anything that walks the commit wizard needs the real constructor.
func wizardModel(t *testing.T, s step, files ...types.FileEntry) Model {
	t.Helper()
	m := NewModel(files, "main")
	m.step = s
	m.width = 120
	m.height = 40
	return m
}

// tempRepo builds a throwaway repo with a single commit and chdirs into it for
// the duration of the test. Anything that reads git state (the menu re-reads
// status now) must not depend on whatever the developer's own tree holds.
func tempRepo(t *testing.T, subject, body string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// The ctrl+c handler calls git.CancelNetworkOps(), which throws a
	// PROCESS-GLOBAL one-way latch: every later fetch, push and `gh repo
	// create` in this binary then fails with ErrNetworkCancelled. One test of
	// the force-quit path would otherwise break every remote operation that
	// -shuffle happens to schedule after it. Re-arm it per test.
	git.ResetNetworkOps()
	// Identity and config isolation go through the environment so the calls
	// internal/git makes (plain exec.Command, inherited env) see them too.
	t.Setenv("GIT_CONFIG_GLOBAL", isolatedGitConfig(t))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t.invalid")
	t.Chdir(t.TempDir())

	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile("seed.txt", []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "seed.txt")
	msg := subject
	if body != "" {
		msg += "\n\n" + body
	}
	run("commit", "-q", "-m", msg)
}

// menuIndex finds a menu entry by name so tests don't hardcode the order
// (conditional entries shift it).
func menuIndex(t *testing.T, m Model, name string) int {
	t.Helper()
	for i, item := range m.menuItems() {
		if item.name == name {
			return i
		}
	}
	t.Fatalf("%q entry missing from the menu", name)
	return -1
}

// ── Amend on a clean tree ──────────────────────────────

func TestAmendOnCleanTreeSkipsFileStep(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)
	m.hasAnyCommit = true
	m.menuCursor = menuIndex(t, m, "Amend")

	m, _ = key(t, m, "enter")
	if m.step != stepType {
		t.Fatalf("step = %v, want stepType (message-only amend of a conventional commit)", m.step)
	}
	if !m.amendMode || m.amendRaw {
		t.Fatalf("amendMode = %v, amendRaw = %v; want true / false", m.amendMode, m.amendRaw)
	}
	if got := m.msgInput.Value(); got != "seed" {
		t.Fatalf("subject prefill = %q, want %q", got, "seed")
	}

	// Back out of the type picker: must not land on the empty file list, and
	// must not leave the amend latched for the next ordinary commit.
	m, _ = key(t, m, "esc")
	if m.step != stepMenu {
		t.Fatalf("esc from type step went to %v, want stepMenu", m.step)
	}
	if m.amendMode || m.commitType != "" || m.msgInput.Value() != "" {
		t.Fatalf("abandoned amend left state behind: amendMode=%v type=%q subject=%q",
			m.amendMode, m.commitType, m.msgInput.Value())
	}
}

// ── Raw (non-conventional) amend ───────────────────────

func TestParseConventionalSubject(t *testing.T) {
	cases := []struct {
		in                 string
		cType, scope, rest string
		breaking           bool
	}{
		{"feat: add login", "feat", "", "add login", false},
		{"feat(auth): add login", "feat", "auth", "add login", false},
		{"feat(auth)!: add login", "feat", "auth", "add login", true},
		{"Initial commit", "", "", "Initial commit", false},
		{"Merge branch 'x' into main", "", "", "Merge branch 'x' into main", false},
		{": nothing before the colon", "", "", ": nothing before the colon", false},
		{"broken(scope: unbalanced", "", "", "broken(scope: unbalanced", false},
	}
	for _, c := range cases {
		cType, scope, breaking, rest := parseConventionalSubject(c.in)
		if cType != c.cType || scope != c.scope || breaking != c.breaking || rest != c.rest {
			t.Errorf("parse(%q) = (%q,%q,%v,%q), want (%q,%q,%v,%q)",
				c.in, cType, scope, breaking, rest, c.cType, c.scope, c.breaking, c.rest)
		}
	}
}

// The flagship regression: amending "Initial commit" with a body longer than
// the editor's char limit must round-trip byte-for-byte through git.
func TestRawAmendRoundTripsNonConventionalCommit(t *testing.T) {
	longBody := strings.TrimSpace(strings.Repeat(
		"this body is deliberately longer than the 500-character editor limit. ", 12)) +
		"\n\nSecond paragraph, still intact."
	if len(longBody) <= bodyCharLimit {
		t.Fatalf("test body is only %d chars — it must exceed bodyCharLimit (%d)", len(longBody), bodyCharLimit)
	}
	tempRepo(t, "Initial commit", longBody)

	m := wizardModel(t, stepMenu)
	m.hasAnyCommit = true
	m.menuCursor = menuIndex(t, m, "Amend")
	m, _ = key(t, m, "enter")

	if !m.amendRaw {
		t.Fatal("non-conventional subject did not enter raw amend mode")
	}
	if m.step != stepMessage {
		t.Fatalf("step = %v, want stepMessage (raw amends skip the type picker)", m.step)
	}
	if got := m.msgInput.Value(); got != "Initial commit" {
		t.Fatalf("subject prefill = %q, want the full raw subject", got)
	}
	if got := m.bodyInput.Value(); got != longBody {
		t.Fatalf("body prefill truncated: %d chars, want %d", len(got), len(longBody))
	}

	full := m.buildCommitMessage(strings.TrimSpace(m.msgInput.Value()))
	if want := "Initial commit\n\n" + longBody; full != want {
		t.Fatalf("commit message =\n%q\nwant\n%q", full, want)
	}

	// And through git itself: no prefix bolted on, no body lost.
	if err := git.Amend(nil, full); err != nil {
		t.Fatalf("amend failed: %v", err)
	}
	subject, body := git.GetLastCommitFull()
	if subject != "Initial commit" {
		t.Fatalf("amended subject = %q, want %q", subject, "Initial commit")
	}
	if body != longBody {
		t.Fatalf("amended body changed:\n got %q\nwant %q", body, longBody)
	}
}

func TestAmendPrefillParsesConventionalSubject(t *testing.T) {
	m := wizardModel(t, stepMenu)
	m = m.applyAmendPrefill("feat(auth)!: add login", "why it changed")

	if m.amendRaw {
		t.Fatal("conventional subject wrongly entered raw mode")
	}
	if m.commitType != "feat" || m.scope != "auth" || !m.breaking {
		t.Fatalf("type=%q scope=%q breaking=%v; want feat/auth/true", m.commitType, m.scope, m.breaking)
	}
	if m.typeIdx != 0 {
		t.Fatalf("typeIdx = %d, want 0 (feat is the first listed type)", m.typeIdx)
	}
	if m.msgInput.Value() != "add login" || !m.showBody || m.bodyInput.Value() != "why it changed" {
		t.Fatalf("prefill = %q / %q (showBody=%v)", m.msgInput.Value(), m.bodyInput.Value(), m.showBody)
	}
	if got := m.buildCommitMessage("add login"); got != "feat(auth)!: add login\n\nwhy it changed" {
		t.Fatalf("rebuilt message = %q", got)
	}
}

func TestAmendPrefillUnlistedTypeFillsCustomInput(t *testing.T) {
	m := wizardModel(t, stepMenu)
	m = m.applyAmendPrefill("wip(core): halfway there", "")

	if m.typeIdx != len(types.CommitTypes) {
		t.Fatalf("typeIdx = %d, want the custom slot (%d)", m.typeIdx, len(types.CommitTypes))
	}
	if m.customInput.Value() != "wip" {
		t.Fatalf("customInput = %q, want %q — enter-through would drop the prefix", m.customInput.Value(), "wip")
	}
	if got := m.buildCommitMessage("halfway there"); got != "wip(core): halfway there" {
		t.Fatalf("rebuilt message = %q", got)
	}
}

func TestRawAmendRoutingFromFileStep(t *testing.T) {
	raw := wizardModel(t, stepFiles, file("a.go"))
	raw = raw.applyAmendPrefill("Initial commit", "")
	raw.step = stepFiles
	raw, _ = key(t, raw, "enter") // amend mode allows an empty selection
	if raw.step != stepMessage {
		t.Fatalf("raw amend went to %v, want stepMessage", raw.step)
	}

	conv := wizardModel(t, stepFiles, file("a.go"))
	conv = conv.applyAmendPrefill("fix: a bug", "")
	conv.step = stepFiles
	conv, _ = key(t, conv, "enter")
	if conv.step != stepType {
		t.Fatalf("conventional amend went to %v, want stepType", conv.step)
	}
}

func TestRawAmendEscFromSubjectLeavesTheStep(t *testing.T) {
	tempRepo(t, "Initial commit", "")

	dirty := wizardModel(t, stepMessage, file("a.go"))
	dirty = dirty.applyAmendPrefill("Initial commit", "")
	dirty.step = stepMessage
	dirty, _ = key(t, dirty, "esc")
	if dirty.step != stepFiles {
		t.Fatalf("esc went to %v, want stepFiles (there was a file selector)", dirty.step)
	}

	clean := wizardModel(t, stepMessage)
	clean = clean.applyAmendPrefill("Initial commit", "")
	clean.step = stepMessage
	clean, _ = key(t, clean, "esc")
	if clean.step != stepMenu {
		t.Fatalf("esc went to %v, want stepMenu (no file selector to go back to)", clean.step)
	}
	if clean.amendMode || clean.amendRaw {
		t.Fatal("abandoned raw amend stayed latched")
	}
}

// ── Wizard reset on every abandon path ─────────────────

func TestResetWizardClearsPrefillAndRestoresLimits(t *testing.T) {
	m := wizardModel(t, stepMenu)
	m = m.applyAmendPrefill("feat(auth): add login", strings.Repeat("z", bodyCharLimit+200))
	m.resetWizard()

	if m.amendMode || m.amendRaw || m.showBody || m.breaking {
		t.Fatal("amend flags survived resetWizard")
	}
	if m.commitType != "" || m.scope != "" || m.typeIdx != 0 {
		t.Fatalf("type state survived: type=%q scope=%q idx=%d", m.commitType, m.scope, m.typeIdx)
	}
	if m.msgInput.Value() != "" || m.bodyInput.Value() != "" || m.scopeInput.Value() != "" || m.customInput.Value() != "" {
		t.Fatal("inputs still hold the old commit's text")
	}
	if m.msgInput.CharLimit != msgCharLimit || m.bodyInput.CharLimit != bodyCharLimit {
		t.Fatalf("char limits not restored: subject=%d body=%d", m.msgInput.CharLimit, m.bodyInput.CharLimit)
	}
}

func TestAbandonedAmendDoesNotLeakIntoTheNextCommit(t *testing.T) {
	tempRepo(t, "feat(auth): add login", "old body")

	m := wizardModel(t, stepMenu, file("a.go"))
	m = m.applyAmendPrefill("feat(auth): add login", "old body")
	m.step = stepFiles

	m, _ = key(t, m, "esc")
	if m.step != stepMenu {
		t.Fatalf("esc from files went to %v, want stepMenu", m.step)
	}
	if m.amendMode || m.amendRaw {
		t.Fatal("amend latch survived the abandon")
	}
	if m.commitType != "" || m.scope != "" || m.breaking || m.showBody {
		t.Fatalf("wizard state survived: type=%q scope=%q breaking=%v showBody=%v",
			m.commitType, m.scope, m.breaking, m.showBody)
	}
	if m.msgInput.Value() != "" || m.bodyInput.Value() != "" || m.scopeInput.Value() != "" {
		t.Fatalf("the old commit's message is still pre-filled: %q / %q",
			m.msgInput.Value(), m.bodyInput.Value())
	}
}

func TestDoneReturnsToMenuWithACleanWizard(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	m := wizardModel(t, stepDone)
	m = m.applyAmendPrefill("chore: seed", "body")
	m.step = stepDone
	m.pushed = true

	m, _ = key(t, m, "enter")
	if m.step != stepMenu {
		t.Fatalf("step = %v, want stepMenu", m.step)
	}
	if m.amendMode || m.pushed || m.msgInput.Value() != "" {
		t.Fatal("Done did not reset the wizard")
	}
}

func TestUndoClearsTheAmendLatch(t *testing.T) {
	tempRepo(t, "feat: two", "")

	m := wizardModel(t, stepFiles)
	m = m.applyAmendPrefill("feat: two", "body")
	m.step = stepFiles
	m.undoing = true

	next, _ := m.Update(undoResultMsg{files: []types.FileEntry{file("a.go")}})
	m = next.(Model)

	if m.amendMode {
		t.Fatal("amendMode still latched after undo — confirming would rewrite the wrong commit")
	}
	if m.msgInput.Value() != "" || m.bodyInput.Value() != "" {
		t.Fatal("the undone commit's message is still pre-filled")
	}
	if m.undoing || m.confirmUndo {
		t.Fatal("undo flags not cleared")
	}
}

// ── Undo warns about pushed commits ────────────────────

func TestUndoPromptWarnsWhenTheCommitIsPushed(t *testing.T) {
	tempRepo(t, "feat: pushed work", "")

	m := wizardModel(t, stepFiles)
	m.confirmUndo = true
	m.undoPushed = true
	if view := m.viewFiles(); !strings.Contains(view, "already pushed to origin") {
		t.Fatalf("undo prompt has no divergence warning:\n%s", view)
	}

	m.undoPushed = false
	if view := m.viewFiles(); strings.Contains(view, "already pushed to origin") {
		t.Fatal("warning shown for an unpushed commit")
	}
}

// ── Amend confirm discloses the staged sweep ───────────

func TestExtraStagedFilesExcludesTheSelection(t *testing.T) {
	sel := file("a.go")
	sel.Selected = true
	renamed := types.FileEntry{Path: "new.go", OrigPath: "old.go", Status: types.StatusRenamed, Selected: true}
	m := Model{
		files:       []types.FileEntry{sel, renamed, file("untouched.go")},
		amendStaged: []string{"a.go", "new.go", "old.go", "sneaky.go"},
	}
	extra := m.extraStagedFiles()
	if len(extra) != 1 || extra[0] != "sneaky.go" {
		t.Fatalf("extraStagedFiles = %v, want [sneaky.go]", extra)
	}
}

func TestMessageOnlyAmendConfirmDoesNotSayZeroFiles(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	m := wizardModel(t, stepConfirm)
	m.amendMode = true
	m.msgInput.SetValue("chore: reseed")

	view := m.viewConfirm()
	if strings.Contains(view, "0 file(s)") {
		t.Fatalf("confirm still reads like the commit will be emptied:\n%s", view)
	}
	if !strings.Contains(view, "Message-only amend") {
		t.Fatalf("no message-only explanation:\n%s", view)
	}
}

func TestAmendConfirmDisclosesAlreadyStagedFiles(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	sel := file("a.go")
	sel.Selected = true
	m := wizardModel(t, stepConfirm, sel)
	m.amendMode = true
	m.amendStaged = []string{"a.go", "staged-elsewhere.txt"}
	m.msgInput.SetValue("chore: seed")

	view := m.viewConfirm()
	if !strings.Contains(view, "1 file(s) to add to this commit") {
		t.Fatalf("selection count not reworded for amend:\n%s", view)
	}
	if !strings.Contains(view, "Also included (already staged)") || !strings.Contains(view, "staged-elsewhere.txt") {
		t.Fatalf("externally staged file not disclosed:\n%s", view)
	}
}

// ── Sticky recovery errors ─────────────────────────────

func TestRecoveryErrorSurvivesOrdinaryKeypresses(t *testing.T) {
	m := Model{
		step:   stepBranch,
		height: 30,
		width:  120,
		err:    recoveryError{fmt.Errorf("your changes are in stash abc1234; recover with: git stash apply abc1234")},
	}

	m, _ = key(t, m, "j")
	if m.err == nil {
		t.Fatal("a navigation key destroyed the only in-app copy of the stash ref")
	}
	if !strings.Contains(formatError(m.err), "dismiss") {
		t.Fatal("sticky banner has no dismiss hint")
	}

	m, _ = key(t, m, "esc")
	if m.err != nil {
		t.Fatal("esc did not dismiss the recovery error")
	}
	if m.step != stepBranch {
		t.Fatalf("the dismissing keypress also navigated (step = %v)", m.step)
	}
}

func TestOrdinaryErrorIsClearedOnAnyKeypress(t *testing.T) {
	m := Model{step: stepMenu, height: 30, width: 120, err: fmt.Errorf("boom")}
	m, _ = key(t, m, "j")
	if m.err != nil {
		t.Fatal("ordinary errors must still clear on the next keypress")
	}
}

// ── Pre-push sync check fires once per visit ───────────

func TestPushSyncCheckFiresOncePerVisit(t *testing.T) {
	m := wizardModel(t, stepPush)
	m.hasRemote = false
	m.pushHasUpstream = true

	if m.pushCheckDone {
		t.Fatal("behind-check latch pre-armed")
	}
	m, cmd := key(t, m, "enter")
	if cmd == nil {
		t.Fatal("push was not dispatched")
	}
	if !m.pushCheckDone {
		t.Fatal("latch not set — the check would re-fire on every enter")
	}

	// Replay the declined dialog: exitSyncDialog drops the user back on the
	// push step with the branch still behind. The next enter must push.
	m.pushing = false
	m.step = stepPush
	m.syncPullCurrent = true
	m, cmd = key(t, m, "enter")
	if m.step == stepSync {
		t.Fatal("declining the pull re-opened the dialog — push is unreachable")
	}
	if cmd == nil {
		t.Fatal("second enter did not push")
	}

	// Leaving the step re-arms it for the next wizard run.
	m.pushing = false
	m, _ = key(t, m, "esc")
	if m.pushCheckDone {
		t.Fatal("latch survived leaving the push step")
	}
}

func TestEmptyFileListKeysDoNotPanic(t *testing.T) {
	m := Model{step: stepFiles, height: 30, width: 100}
	for _, k := range []string{" ", "d", "a", "enter", "u"} {
		m, _ = key(t, m, k)
	}
	if m.step != stepFiles {
		t.Fatalf("step = %v, want stepFiles (enter must not advance without a selection)", m.step)
	}
}

func TestGitignoreSpaceOnEmptyListDoesNotPanic(t *testing.T) {
	m := Model{step: stepFiles, gitignoreMode: true, height: 30}
	m, _ = key(t, m, " ")
	if !m.gitignoreMode {
		t.Fatal("gitignore mode exited unexpectedly")
	}
}

// ── Gitignore exit cursor clamp (ROADMAP M11) ──────────

func TestGitignoreExitClampsCursor(t *testing.T) {
	m := Model{
		step:            stepFiles,
		files:           []types.FileEntry{file("a.go")},
		existingIgnored: []string{"node_modules/", "dist/"},
		removeIgnored:   map[string]bool{},
		gitignoreMode:   true,
		cursor:          2, // parked in the existing-ignored zone
		fileScroll:      2,
		height:          30,
	}
	m, _ = key(t, m, "g")
	if m.gitignoreMode {
		t.Fatal("gitignore mode still active after g")
	}
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (clamped to len(files)-1)", m.cursor)
	}
	if m.fileScroll != 0 {
		t.Fatalf("fileScroll = %d, want 0", m.fileScroll)
	}
	// The key that used to panic right after the exit.
	m, _ = key(t, m, " ")
	if !m.files[0].Selected {
		t.Fatal("space did not toggle the first file")
	}
}

// ── Binary files never reach the editor ────────────────

func TestDiffEditRefusesBinary(t *testing.T) {
	m := Model{
		step:        stepFiles,
		files:       []types.FileEntry{file("logo.png")},
		showDiff:    true,
		diffContent: "", // binary sentinel
		diffFile:    "logo.png",
		height:      30,
	}
	m, _ = key(t, m, "e")
	if m.editMode {
		t.Fatal("editor opened on a binary file")
	}
	if m.err == nil {
		t.Fatal("no error surfaced — the refusal must not be silent")
	}
}

// ── saveResultMsg cursor clamp ─────────────────────────

func TestSaveResultClampsCursor(t *testing.T) {
	m := Model{
		step:     stepFiles,
		files:    []types.FileEntry{file("a.go"), file("b.go")},
		cursor:   1,
		saving:   true,
		showDiff: true,
		editMode: true,
		height:   30,
	}
	next, _ := m.Update(saveResultMsg{files: []types.FileEntry{file("a.go")}, diff: "diff"})
	m = next.(Model)
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 after the list shrank", m.cursor)
	}

	// Editing the only file back to HEAD empties the list entirely.
	next, _ = m.Update(saveResultMsg{files: nil})
	m = next.(Model)
	if m.cursor != 0 || m.showDiff {
		t.Fatalf("cursor = %d, showDiff = %v; want 0 / false on an empty list", m.cursor, m.showDiff)
	}
	// viewFiles renders viewDiff when showDiff is set — must not panic now.
	_ = m.viewFiles()
}

// ── Empty pickers ──────────────────────────────────────

func TestConfigPickerEmptyEnterDoesNotPanic(t *testing.T) {
	m := Model{step: stepConfig, configPickMode: true, height: 30}
	m, _ = key(t, m, "enter")
	if m.configPickMode {
		t.Fatal("empty picker stayed open")
	}
}

func TestMergeTargetPickerEmptyEnterDoesNotPanic(t *testing.T) {
	m := Model{step: stepBranch, mergeTargetMode: true, mergeSource: "main", height: 30}
	m, cmd := key(t, m, "enter")
	if cmd != nil {
		t.Fatal("empty merge picker dispatched a command")
	}
	if m.branchMergeMode {
		t.Fatal("merge confirmation opened with no target")
	}
}

// ── ctrl+c during a mutation ───────────────────────────

func TestCtrlCRequiresConfirmationWhileBusy(t *testing.T) {
	m := Model{step: stepBranch, branchSwitching: true, height: 30}

	m, cmd := key(t, m, "ctrl+c")
	if m.quitting || cmd != nil {
		t.Fatal("first ctrl+c quit during an in-flight operation")
	}
	if !m.forceQuitArmed || m.err == nil {
		t.Fatal("no force-quit warning shown")
	}

	m, cmd = key(t, m, "ctrl+c")
	if !m.quitting || cmd == nil {
		t.Fatal("second ctrl+c did not force quit")
	}
}

func TestCtrlCQuitsImmediatelyWhenIdle(t *testing.T) {
	m := Model{step: stepMenu, height: 30}
	m, cmd := key(t, m, "ctrl+c")
	if !m.quitting || cmd == nil {
		t.Fatal("idle ctrl+c did not quit")
	}
}

func TestOtherKeypressDisarmsForceQuit(t *testing.T) {
	m := Model{step: stepMenu, forceQuitArmed: true, height: 30}
	m, _ = key(t, m, "j")
	if m.forceQuitArmed {
		t.Fatal("force-quit stayed armed after an unrelated keypress")
	}
}

// ── Double-fire guards ─────────────────────────────────

func TestUndoIsSingleShot(t *testing.T) {
	// A real model: the success path resets the wizard, and resetting the
	// body textarea nil-derefs on a zero-value bubbles widget.
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepFiles)
	m.confirmUndo = true

	m, cmd := key(t, m, "y")
	if cmd == nil {
		t.Fatal("undo was not dispatched")
	}
	if !m.undoing || m.confirmUndo {
		t.Fatalf("undoing = %v, confirmUndo = %v; want true / false", m.undoing, m.confirmUndo)
	}

	// Second 'y' while the reset runs must be swallowed.
	m, cmd = key(t, m, "y")
	if cmd != nil {
		t.Fatal("second y dispatched a second soft reset")
	}

	next, _ := m.Update(undoResultMsg{})
	if next.(Model).undoing {
		t.Fatal("undoing flag not cleared by undoResultMsg")
	}
}

func TestGitignoreApplyIsSingleShot(t *testing.T) {
	f := file("secret.env")
	f.Gitignored = true
	m := Model{
		step:          stepFiles,
		files:         []types.FileEntry{f},
		removeIgnored: map[string]bool{},
		gitignoreMode: true,
		height:        30,
	}

	m, cmd := key(t, m, "enter")
	if cmd == nil {
		t.Fatal("gitignore apply was not dispatched")
	}
	if !m.gitignoring {
		t.Fatal("gitignoring flag not set")
	}

	m, cmd = key(t, m, "enter")
	if cmd != nil {
		t.Fatal("second enter re-ran the gitignore pipeline")
	}

	next, _ := m.Update(gitignoreResultMsg{err: errTest})
	if next.(Model).gitignoring {
		t.Fatal("gitignoring flag not cleared by gitignoreResultMsg")
	}
}

// errTest keeps the gitignoreResultMsg handler on its error path so it does
// not shell out to git status.
var errTest = &testError{}

type testError struct{}

func (e *testError) Error() string { return "test" }

// ── Branch manager: merge routing ──────────────────────

// mergeModel parks a model on the branch list with a fixed set of branches:
// feat (current), main, release, and a remote-only "hotfix".
func mergeModel(current string) Model {
	entries := []types.BranchEntry{
		{Name: "feat", IsCurrent: current == "feat"},
		{Name: "main", IsCurrent: current == "main"},
		{Name: "release", IsCurrent: current == "release"},
		{Name: "hotfix", IsRemote: true},
	}
	return Model{
		step:          stepBranch,
		branch:        current,
		branchEntries: entries,
		width:         140,
		height:        60,
	}
}

func branchIndex(t *testing.T, m Model, name string) int {
	t.Helper()
	for i, e := range m.branchEntries {
		if e.Name == name {
			return i
		}
	}
	t.Fatalf("branch %q missing from the fixture", name)
	return -1
}

// "merge my feature into main" is the most common merge there is, and it used
// to be rejected outright with "cannot merge a branch into itself".
func TestMergeFromCurrentBranchOpensTargetPicker(t *testing.T) {
	m := mergeModel("feat")
	m.branchCursor = branchIndex(t, m, "feat")

	m, _ = key(t, m, "m")
	if m.err != nil {
		t.Fatalf("pressing m on the current branch errored: %v", m.err)
	}
	if !m.mergeTargetMode {
		t.Fatal("target picker did not open")
	}
	if m.mergeSource != "feat" {
		t.Fatalf("mergeSource = %q, want feat", m.mergeSource)
	}
	for _, tgt := range m.mergeTargets {
		if tgt.Name == "feat" {
			t.Error("the source was offered as its own merge target")
		}
		if tgt.IsRemote {
			t.Errorf("remote-only branch %q offered as a merge target", tgt.Name)
		}
	}
	// With the source excluded there is no "current" entry left, so the
	// picker should land on main.
	if got := m.mergeTargets[m.mergeTargetCursor].Name; got != "main" {
		t.Errorf("preselected target = %q, want main", got)
	}
}

// Source == current: confirming must switch to the target first, then merge.
func TestMergeIntoOtherBranchSwitchesFirst(t *testing.T) {
	m := mergeModel("feat")
	m.branchCursor = branchIndex(t, m, "feat")
	m, _ = key(t, m, "m")
	m, _ = key(t, m, "enter") // pick main

	if !m.branchMergeMode || m.mergeTarget != "main" {
		t.Fatalf("mergeMode = %v, target = %q; want true / main", m.branchMergeMode, m.mergeTarget)
	}

	m, cmd := key(t, m, "y")
	if cmd == nil {
		t.Fatal("confirming the merge dispatched nothing")
	}
	if !m.branchSwitching {
		t.Fatal("the switch leg did not start")
	}
	if m.branchMergePending != "feat" {
		t.Fatalf("branchMergePending = %q, want feat", m.branchMergePending)
	}
	if m.branchMerging {
		t.Fatal("merge started before the switch completed")
	}
}

// Source != current, target == current: merge directly, no switch.
func TestMergeIntoCurrentBranchMergesDirectly(t *testing.T) {
	m := mergeModel("main")
	m.branchCursor = branchIndex(t, m, "feat")
	m, _ = key(t, m, "m")

	if got := m.mergeTargets[m.mergeTargetCursor].Name; got != "main" {
		t.Fatalf("preselected target = %q, want the current branch (main)", got)
	}
	m, _ = key(t, m, "enter")
	m, cmd := key(t, m, "y")
	if cmd == nil {
		t.Fatal("confirming the merge dispatched nothing")
	}
	if !m.branchMerging {
		t.Fatal("merge did not start")
	}
	if m.branchSwitching || m.branchMergePending != "" {
		t.Fatalf("merging into the branch we're on took the switch path (switching=%v pending=%q)",
			m.branchSwitching, m.branchMergePending)
	}
}

func TestMergeConfirmDisclosesTheSwitch(t *testing.T) {
	m := mergeModel("feat")
	m.branchMergeMode = true
	m.mergeSource = "feat"
	m.mergeTarget = "main"

	out := m.viewBranch()
	if !strings.Contains(out, "switch to main and merge feat into it") {
		t.Errorf("merge confirmation hides the branch switch:\n%s", out)
	}

	// Merging into the branch you're already on must not claim a switch.
	m.mergeTarget = "feat"
	if out := m.viewBranch(); strings.Contains(out, "switch to") {
		t.Errorf("same-branch merge claimed a switch:\n%s", out)
	}
}

// ── Branch manager: force delete ───────────────────────

func TestForceDeleteConfirmationStateMachine(t *testing.T) {
	m := mergeModel("main")
	m.branchCursor = branchIndex(t, m, "feat")
	m.branchDeleting = true

	notMerged := fmt.Errorf("%w: error: the branch 'feat' is not fully merged", git.ErrBranchNotMerged)
	next, _ := m.Update(branchDeleteResultMsg{err: notMerged, name: "feat"})
	m = next.(Model)

	if !m.branchForceDeleteMode || m.branchForceDeleteName != "feat" {
		t.Fatalf("force prompt = %v (%q); want true / feat", m.branchForceDeleteMode, m.branchForceDeleteName)
	}
	if m.err != nil {
		t.Errorf("raw git output surfaced instead of the confirmation: %v", m.err)
	}
	if m.branchDeleting {
		t.Error("in-flight flag survived the result message")
	}
	if out := m.viewBranch(); !strings.Contains(out, "not merged into any other branch") {
		t.Errorf("force-delete screen does not explain itself:\n%s", out)
	}

	// Anything other than y cancels without deleting.
	cancelled, cmd := key(t, m, "n")
	if cancelled.branchForceDeleteMode || cmd != nil {
		t.Fatalf("n did not cancel the force delete (mode=%v cmd=%v)", cancelled.branchForceDeleteMode, cmd != nil)
	}

	forced, cmd := key(t, m, "y")
	if cmd == nil || !forced.branchDeleting {
		t.Fatal("y did not dispatch the force delete")
	}
	if forced.branchForceDeleteMode || forced.branchForceDeleteName != "" {
		t.Error("force-delete prompt state survived the confirmation")
	}
}

func TestOtherDeleteFailuresStillSurface(t *testing.T) {
	m := mergeModel("main")
	next, _ := m.Update(branchDeleteResultMsg{err: errors.New("branch 'x' not found"), name: "x"})
	got := next.(Model)
	if got.branchForceDeleteMode {
		t.Fatal("an unrelated delete failure opened the force-delete prompt")
	}
	if got.err == nil {
		t.Fatal("the delete error was swallowed")
	}
}

// ── Branch manager: single-shot create / delete ────────

func TestBranchCreateValidatesNameAndIsSingleShot(t *testing.T) {
	tempRepo(t, "chore: seed", "") // CheckRefFormatBranch shells out to git
	m := wizardModel(t, stepBranch)
	m.branchCreateMode = true
	m.branchCreateInput.SetValue("my new feature")

	m, cmd := key(t, m, "enter")
	if cmd != nil {
		t.Fatal("an invalid branch name was sent to git anyway")
	}
	if m.err == nil {
		t.Fatal("no inline error for an invalid branch name")
	}
	if !strings.Contains(m.err.Error(), "can't contain spaces") {
		t.Errorf("error = %q, want the beginner-readable line first", m.err)
	}
	if !strings.Contains(m.err.Error(), "not a valid branch name") {
		t.Errorf("error = %q, want git's own text kept as a second line", m.err)
	}
	if !m.branchCreateMode {
		t.Fatal("a rejected name kicked the user out of create mode")
	}
	if out := m.viewBranch(); !strings.Contains(out, "can't contain spaces") {
		t.Errorf("create screen does not render the validation error:\n%s", out)
	}

	m.branchCreateInput.SetValue("feat/ok")
	m, cmd = key(t, m, "enter")
	if cmd == nil || !m.branchCreating {
		t.Fatalf("a valid name did not dispatch a create (cmd=%v creating=%v)", cmd != nil, m.branchCreating)
	}

	m, cmd = key(t, m, "enter")
	if cmd != nil {
		t.Fatal("a second enter fired a second create")
	}

	next, _ := m.Update(branchCreateResultMsg{err: errTest})
	if next.(Model).branchCreating {
		t.Fatal("branchCreating not cleared by branchCreateResultMsg")
	}
}

func TestBranchDeleteIsSingleShot(t *testing.T) {
	m := mergeModel("main")
	m.branchCursor = branchIndex(t, m, "feat")
	m.branchDeleteMode = true

	m, cmd := key(t, m, "y")
	if cmd == nil || !m.branchDeleting {
		t.Fatalf("delete was not dispatched (cmd=%v deleting=%v)", cmd != nil, m.branchDeleting)
	}
	if m.branchDeleteMode {
		t.Error("delete confirmation stayed open")
	}

	// The list stays on screen while the delete runs, so enter on the dying
	// branch used to race a checkout against `git branch -d`.
	m, cmd = key(t, m, "enter")
	if cmd != nil {
		t.Fatal("input was live during an in-flight delete")
	}

	next, _ := m.Update(branchDeleteResultMsg{err: errTest, name: "feat"})
	if next.(Model).branchDeleting {
		t.Fatal("branchDeleting not cleared by branchDeleteResultMsg")
	}
}

// ── Branch manager: auto-stash around merges ───────────

func TestMergeAutoStashesAndRestoresADirtyTree(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	gitRun(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "feature.txt", "feature\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "feat: work")
	gitRun(t, "checkout", "-q", "main")

	// Uncommitted work that has nothing to do with the merge.
	writeFile(t, "seed.txt", "dirty\n")

	msg, ok := doMergeBranch("feat")().(branchMergeResultMsg)
	if !ok {
		t.Fatal("doMergeBranch returned the wrong message type")
	}
	if msg.err != nil {
		t.Fatalf("merge on a dirty tree failed: %v", msg.err)
	}
	if !msg.merged || !msg.stashRestored {
		t.Fatalf("merged=%v stashRestored=%v; want both true", msg.merged, msg.stashRestored)
	}
	if got := readFile(t, "seed.txt"); got != "dirty\n" {
		t.Errorf("uncommitted change lost: seed.txt = %q", got)
	}
	if _, err := os.Stat("feature.txt"); err != nil {
		t.Errorf("the merge did not land: %v", err)
	}
	if n := stashDepth(t); n != 0 {
		t.Errorf("%d stash entr(ies) left behind", n)
	}

	// The success note has to state what happened to the changes.
	m := wizardModel(t, stepBranch)
	next, _ := m.Update(msg)
	if note := next.(Model).statusNote; !strings.Contains(note, "stashed and restored") {
		t.Errorf("result note = %q, want the stash disclosed", note)
	}
	if out := next.(Model).viewBranch(); !strings.Contains(out, "stashed and restored") {
		t.Errorf("the branch manager does not render the note:\n%s", out)
	}
}

// The menu's sync shortcut merges from outside the branch manager. It used to
// have no note line to render into and smuggled the stash disclosure out
// through m.err; now both screens render the same one-shot note.
func TestMergeStashNoteReachesTheMenuToo(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)
	next, _ := m.Update(branchMergeResultMsg{source: "origin/main", merged: true, stashRestored: true})
	got := next.(Model)
	if !strings.Contains(got.statusNote, "stashed and restored") {
		t.Fatalf("the stash round-trip was not stated on the menu: %q", got.statusNote)
	}
	if !strings.Contains(got.statusNote, "Merged origin/main into") {
		t.Errorf("note = %q, want the merge itself reported too", got.statusNote)
	}
	if got.err != nil {
		t.Errorf("a successful merge still reports through the error banner: %v", got.err)
	}
	if out := got.viewMenu(); !strings.Contains(out, "stashed and restored") {
		t.Errorf("the dashboard does not render the note:\n%s", out)
	}
}

// Updated with the conflict resolver: this used to assert that a conflicting
// merge auto-aborted and popped the stash on the spot. It does neither now —
// the merge stays in progress, the stash stays parked (git refuses to pop into
// an unmerged index), and BOTH are undone by `a` on the resolver, which is the
// same abort the handler used to run. The round trip is still the assertion;
// what changed is who triggers it. See TestAutoStashComesBackAfterAbort for the
// same flow driven all the way through the resolver's keys.
func TestMergeConflictOnADirtyTreeParksTheStashUntilTheMergeEnds(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	writeFile(t, "shared.txt", "base\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "add shared")

	gitRun(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "shared.txt", "from feat\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "feat: change shared")

	gitRun(t, "checkout", "-q", "main")
	writeFile(t, "shared.txt", "from main\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "chore: change shared")

	// Unrelated uncommitted work that must survive the failed merge.
	writeFile(t, "notes.txt", "in progress\n")

	msg := doMergeBranch("feat")().(branchMergeResultMsg)
	if msg.err == nil {
		t.Fatal("the conflicting merge succeeded")
	}
	if !msg.stashed {
		t.Fatal("the dirty tree was not stashed before merging")
	}

	m := wizardModel(t, stepBranch)
	next, _ := m.Update(msg)
	got := next.(Model)

	// The merge is NOT aborted, and neither is it reported as a failure: it is
	// a screen now.
	if got.step != stepConflicts {
		t.Fatalf("step = %v, want stepConflicts", got.step)
	}
	if got.err != nil {
		t.Errorf("the conflict was reported as an error banner: %v", got.err)
	}
	if !got.conflictStashed || got.conflictStashRef == "" {
		t.Error("the pending auto-stash was not handed to the resolver")
	}
	if n := stashDepth(t); n != 1 {
		t.Fatalf("%d stash entries mid-merge, want the auto-stash parked until the merge ends", n)
	}
	if _, err := os.Stat("notes.txt"); err == nil {
		t.Error("notes.txt is in the working tree during the merge — it belongs in the stash")
	}

	// `a` is the old behavior, two keypresses away: it asks first (throwing a
	// merge away is destructive), then aborts and restores.
	confirming, cmd := key(t, got, "a")
	if cmd != nil || !confirming.conflictConfirmAbort {
		t.Fatal("a aborted without confirming")
	}
	m2, cmd := key(t, confirming, "y")
	if cmd == nil {
		t.Fatal("y dispatched nothing")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected a batch, got %T", cmd())
	}
	for _, c := range batch {
		if res, ok := c().(conflictAbortResultMsg); ok {
			out, _ := m2.Update(res)
			m2 = out.(Model)
		}
	}
	if m2.err != nil {
		t.Fatalf("the abort failed: %v", m2.err)
	}
	if content := readFile(t, "notes.txt"); content != "in progress\n" {
		t.Errorf("uncommitted work not restored: notes.txt = %q", content)
	}
	if n := stashDepth(t); n != 0 {
		t.Errorf("%d stash entr(ies) left behind after recovery", n)
	}
	if !strings.Contains(m2.statusNote, "restored") {
		t.Errorf("note = %q, want the stash round trip stated", m2.statusNote)
	}
}

// A checkout that fails after the auto-stash used to drop both the pop error
// and the stash ref, leaving the user's work in an unmentioned stash.
func TestSwitchFailureRestoresAndReportsTheStash(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	writeFile(t, "seed.txt", "dirty\n")

	msg := doSwitchBranch("no-such-branch", false)().(branchSwitchResultMsg)
	if msg.err == nil {
		t.Fatal("switching to a nonexistent branch succeeded")
	}
	if !strings.Contains(msg.err.Error(), "stashed and restored") {
		t.Errorf("error = %q, want the stash round-trip stated", msg.err)
	}
	if got := readFile(t, "seed.txt"); got != "dirty\n" {
		t.Errorf("uncommitted change lost: seed.txt = %q", got)
	}
	if n := stashDepth(t); n != 0 {
		t.Errorf("%d stash entr(ies) left behind", n)
	}
}

func TestFailedSwitchCancelsThePendingMerge(t *testing.T) {
	m := mergeModel("feat")
	m.branchSwitching = true
	m.branchMergePending = "feat"

	next, _ := m.Update(branchSwitchResultMsg{err: errors.New("checkout blew up")})
	got := next.(Model)
	if got.branchMergePending != "" {
		t.Error("pending merge survived a failed switch")
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "cancelled") {
		t.Errorf("error = %v, want the cancelled merge stated", got.err)
	}
}

// ── Sync dialog honesty ────────────────────────────────

func TestSyncDialogFlagsADivergedBranch(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	remote := t.TempDir()
	gitRun(t, "init", "-q", "--bare", remote)
	gitRun(t, "remote", "add", "origin", remote)
	gitRun(t, "push", "-q", "-u", "origin", "main")
	// Rewriting a pushed commit leaves the branch 1 ahead and 1 behind.
	gitRun(t, "commit", "-q", "--amend", "-m", "chore: seed (amended)")

	m := wizardModel(t, stepSync)
	m.branch = "main"
	m.hasRemote = true
	if !m.populateSyncDialog() {
		t.Fatal("the dialog did not fire on a diverged branch")
	}
	if !m.syncDiverged {
		t.Fatalf("syncDiverged = false (ahead %d / behind %d)", m.syncAhead, m.syncCurrTotal)
	}
	if m.syncAhead != 1 || m.syncCurrTotal != 1 {
		t.Fatalf("ahead/behind = %d/%d, want 1/1", m.syncAhead, m.syncCurrTotal)
	}
	if !m.syncPullCurrent {
		t.Error("pull was removed instead of being warned about")
	}

	out := m.viewSync()
	if !strings.Contains(out, "diverged from origin (1 ahead / 1 behind)") {
		t.Errorf("the dialog does not disclose the divergence:\n%s", out)
	}
	if !strings.Contains(out, "force-with-lease") {
		t.Errorf("the dialog does not point at force-push:\n%s", out)
	}
	if !strings.Contains(out, "pull anyway") {
		t.Errorf("the pull offer is not qualified:\n%s", out)
	}
}

func TestSyncDialogDoesNotWarnOnAPlainBehindBranch(t *testing.T) {
	m := Model{
		step: stepSync, width: 140, height: 60, branch: "main",
		syncPullCurrent:  true,
		syncIncomingCurr: []string{"one", "two"},
		syncCurrTotal:    2,
	}
	out := m.viewSync()
	if strings.Contains(out, "diverged") {
		t.Errorf("a behind-only branch got the divergence warning:\n%s", out)
	}
	if !strings.Contains(out, "(2 new)") {
		t.Errorf("count missing:\n%s", out)
	}
	if strings.Contains(out, "and 0 more") {
		t.Errorf("spurious truncation marker:\n%s", out)
	}
}

func TestSyncDialogReportsCommitsBeyondTheSample(t *testing.T) {
	m := Model{
		step: stepSync, width: 140, height: 60, branch: "main",
		syncPullCurrent:  true,
		syncIncomingCurr: []string{"one", "two", "three"},
		syncCurrTotal:    25,
	}
	out := m.viewSync()
	if !strings.Contains(out, "(25 new)") {
		t.Errorf("header understates the backlog:\n%s", out)
	}
	if !strings.Contains(out, "and 22 more") {
		t.Errorf("no marker for the commits it isn't listing:\n%s", out)
	}
}

// ── Init flow ──────────────────────────────────────────

func TestInitPickersQuitOnQ(t *testing.T) {
	for _, phase := range []initPhase{initPhasePickTemplate, initPhasePickVisibility} {
		m := Model{step: stepInit, initPhase: phase, width: 140, height: 60}
		m.initTemplateOptions = git.GitignoreTemplates()
		m, cmd := key(t, m, "q")
		if !m.quitting || cmd == nil {
			t.Errorf("q on init phase %d did nothing", phase)
		}
	}
}

func TestIsValidGitURL(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://github.com/u/r.git", true},
		{"http://example.com/r", true},
		{"ssh://git@example.com:22/u/r.git", true},
		{"git://example.com/r.git", true},
		{"git+ssh://example.com/r.git", true},
		{"file:///srv/git/r.git", true},
		{"git@github.com:u/r.git", true},
		{"github.com:u/r.git", true}, // scp-form without user@
		{"gh:u/r", true},             // ssh config alias
		{"/srv/git/r.git", true},
		{"./r.git", true},
		{"../other-repo", true},
		{"", false},
		{"origin", false},
		{"repo.git", false},
		{"https://", false},
		{"https://x y z", false},
		{"git@github.com:u/r .git", false},
		{`C:\repos\r`, false},
		{"C:/repos/r", false},
	}
	for _, c := range cases {
		if got := isValidGitURL(c.url); got != c.ok {
			t.Errorf("isValidGitURL(%q) = %v, want %v", c.url, got, c.ok)
		}
	}
}

func TestInitWarningIsStickyOnTheMenu(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepInit)
	m.initPhase = initPhaseWorking
	m.initWorking = true

	next, _ := m.Update(initResultMsg{
		branch:  "main",
		message: "Connected to git@example.com:u/r.git — commit then push from the menu.",
		warning: "this repository and origin/main have unrelated histories — pushing and pulling will be rejected.",
	})
	got := next.(Model)
	if got.step != stepMenu {
		t.Fatalf("step = %v, want stepMenu (the remote was still configured)", got.step)
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "unrelated histories") {
		t.Fatalf("warning not surfaced: %v", got.err)
	}
	if !isRecoveryError(got.err) {
		t.Error("the warning is not sticky — an arrow key would wipe it")
	}
	// An ordinary keypress must not clear it; esc must.
	after, _ := key(t, got, "j")
	if after.err == nil {
		t.Error("a navigation key cleared the warning")
	}
	dismissed, _ := key(t, after, "esc")
	if dismissed.err != nil {
		t.Error("esc did not dismiss the warning")
	}
}

// ── Small helpers ──────────────────────────────────────

func gitRun(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func stashDepth(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("git", "stash", "list").Output()
	if err != nil {
		t.Fatalf("git stash list: %v", err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

// ── Asynchronous dashboard refresh ─────────────────────

func fullSnapshot(branch string) dashboardSnapshot {
	return dashboardSnapshot{
		branch:       branch,
		graph:        "* feat: something",
		aheadBehind:  "2 ahead",
		behindOrigin: 2,
		behindMain:   3,
		mainName:     "main",
		mainRef:      "origin/main",
		branchCount:  4,
		hasAnyCommit: true,
		files:        []types.FileEntry{file("fresh.go")},
		filesOK:      true,
	}
}

func TestDashboardRefreshAppliesTheSnapshot(t *testing.T) {
	m := Model{step: stepMenu, branch: "main", width: 120, height: 40, refreshing: true}
	next, cmd := m.Update(dashboardRefreshMsg{snap: fullSnapshot("main")})
	got := next.(Model)

	if got.refreshing {
		t.Error("the in-flight flag survived the result")
	}
	if cmd != nil {
		t.Error("an unrequested follow-up refresh was dispatched")
	}
	if got.localGraph != "* feat: something" || got.aheadBehind != "2 ahead" {
		t.Errorf("graph/chip not applied: %q / %q", got.localGraph, got.aheadBehind)
	}
	if got.behindOrigin != 2 || got.behindMain != 3 || got.branchCount != 4 || !got.hasAnyCommit {
		t.Errorf("counters not applied: origin=%d main=%d branches=%d anyCommit=%v",
			got.behindOrigin, got.behindMain, got.branchCount, got.hasAnyCommit)
	}
	if got.mainName != "main" || got.mainRef != "origin/main" {
		t.Errorf("main resolution not applied: %q / %q", got.mainName, got.mainRef)
	}
	if len(got.files) != 1 || got.files[0].Path != "fresh.go" {
		t.Errorf("the working tree was not applied on the dashboard: %v", got.files)
	}
}

// A snapshot landing after the user has walked into the file selector must not
// reset their cursor or drop their selections — the wizard re-reads status for
// itself on the way in.
func TestDashboardRefreshDoesNotClobberTheFileSelector(t *testing.T) {
	sel := file("a.go")
	sel.Selected = true
	m := Model{step: stepFiles, branch: "main", files: []types.FileEntry{file("z.go"), sel}, cursor: 1, refreshing: true}

	next, _ := m.Update(dashboardRefreshMsg{snap: fullSnapshot("main")})
	got := next.(Model)
	if len(got.files) != 2 || !got.files[1].Selected {
		t.Fatalf("the selection was wiped by a background refresh: %v", got.files)
	}
	if got.cursor != 1 {
		t.Errorf("cursor = %d, want 1 — the refresh moved it", got.cursor)
	}
	// The dashboard half still applies.
	if got.behindMain != 3 {
		t.Errorf("behindMain = %d, want 3", got.behindMain)
	}
}

func TestDashboardRefreshDropsASnapshotFromAnotherBranch(t *testing.T) {
	m := Model{step: stepMenu, branch: "feat", refreshing: true}
	next, cmd := m.Update(dashboardRefreshMsg{snap: fullSnapshot("main")})
	got := next.(Model)

	if got.behindMain != 0 || got.branchCount != 0 {
		t.Errorf("a snapshot taken for another branch was applied: main=%d branches=%d",
			got.behindMain, got.branchCount)
	}
	if cmd == nil || !got.refreshing {
		t.Error("no fresh reading was taken after the stale one was dropped")
	}
}

// Two refreshes requested back to back must not run two fleets of git
// processes; the second is remembered and dispatched when the first lands.
func TestDashboardRefreshesCoalesce(t *testing.T) {
	m := Model{step: stepMenu, branch: "main"}

	first := m.requestDashboardRefresh()
	if first == nil || !m.refreshing {
		t.Fatal("the first refresh was not dispatched")
	}
	second := m.requestDashboardRefresh()
	if second != nil {
		t.Fatal("a second refresh was stacked on top of the in-flight one")
	}
	if !m.refreshPending {
		t.Fatal("the coalesced request was dropped instead of remembered")
	}

	next, cmd := m.Update(dashboardRefreshMsg{snap: fullSnapshot("main")})
	got := next.(Model)
	if cmd == nil {
		t.Fatal("the pending refresh was never dispatched")
	}
	if got.refreshPending {
		t.Error("refreshPending stayed set — every later refresh would be swallowed")
	}
	if !got.refreshing {
		t.Error("the re-dispatched refresh is not marked in flight")
	}
}

// The centerpiece: returning to the menu must not fork git inside Update. The
// sentinel values survive the call and only a command comes back.
func TestReturnToMenuDefersTheGitWork(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepFiles, file("a.go"))
	m.localGraph = "SENTINEL"
	m.branchCount = 99
	m.files = []types.FileEntry{file("stale.go")}

	cmd := m.returnToMenu()
	if cmd == nil {
		t.Fatal("returnToMenu dispatched no refresh at all")
	}
	if m.step != stepMenu {
		t.Fatalf("step = %v, want stepMenu", m.step)
	}
	if m.localGraph != "SENTINEL" || m.branchCount != 99 || m.files[0].Path != "stale.go" {
		t.Fatal("returnToMenu still read git synchronously — the menu blocks on every return")
	}
	if !m.refreshing {
		t.Error("the deferred refresh was not marked in flight")
	}
}

// The snapshot the dashboard renders and the ref the "s" shortcut merges come
// from the same resolution pass.
func TestDashboardSnapshotAgreesWithTheSyncShortcut(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	remote := t.TempDir()
	gitRun(t, "init", "-q", "--bare", remote)
	gitRun(t, "remote", "add", "origin", remote)
	gitRun(t, "push", "-q", "-u", "origin", "main")
	gitRun(t, "branch", "feat")
	writeFile(t, "a.txt", "a\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "feat: a")
	gitRun(t, "push", "-q", "origin", "main")
	// Local main falls behind the commit it just pushed.
	gitRun(t, "reset", "-q", "--hard", "HEAD~1")
	gitRun(t, "checkout", "-q", "feat")

	snap := readDashboard("feat", false)
	if snap.behindMain != 1 {
		t.Errorf("snapshot behindMain = %d, want 1", snap.behindMain)
	}
	if snap.mainRef != "origin/main" || snap.mainName != "main" {
		t.Errorf("snapshot main = %q / %q, want main / origin/main", snap.mainName, snap.mainRef)
	}

	m := Model{step: stepMenu, branch: "feat", width: 120, height: 40}
	m.applyDashboard(snap)
	if !m.canSyncMain() {
		t.Fatal("the badge is showing but the sync shortcut is gated off")
	}
	if out := m.viewMenu(); !strings.Contains(out, "1 behind main") {
		t.Errorf("badge missing from the dashboard:\n%s", out)
	}
}

func TestSyncShortcutNeedsAResolvedMainRef(t *testing.T) {
	m := Model{step: stepMenu, width: 120, height: 40, behindMain: 2}
	if m.canSyncMain() {
		t.Fatal("a sync was offered with no ref to merge")
	}
	m, cmd := key(t, m, "s")
	if cmd != nil || m.branchMerging {
		t.Fatal("pressing s merged something with no main ref resolved")
	}

	m.mainRef = "origin/main"
	m.behindMain = 2
	m, cmd = key(t, m, "s")
	if cmd == nil || !m.branchMerging {
		t.Fatal("s did not start the sync when the ref was known")
	}
}

// ── Status notes ───────────────────────────────────────

func TestStatusNoteSurvivesNavigationAndClearsOnAnAction(t *testing.T) {
	m := Model{step: stepMenu, width: 120, height: 40, statusNote: "Merged feat into main"}

	if out := m.viewMenu(); !strings.Contains(out, "Merged feat into main") {
		t.Fatalf("the note is not rendered on the dashboard:\n%s", out)
	}

	m, _ = key(t, m, "j")
	if m.statusNote == "" {
		t.Fatal("moving the cursor wiped the note")
	}
	m, _ = key(t, m, "down")
	if m.statusNote == "" {
		t.Fatal("an arrow key wiped the note")
	}

	m, _ = key(t, m, "s")
	if m.statusNote != "" {
		t.Fatal("the note survived a real action")
	}
}

// The .gitignore apply and the undo both finish in the file selector, so that
// is where their note has to appear — immediately, not once the user happens to
// walk back to the dashboard.
func TestStatusNoteIsShownOnTheFileSelector(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.statusNote = "Updated .gitignore — 2 entries added"

	if out := m.viewFiles(); !strings.Contains(out, "Updated .gitignore") {
		t.Fatalf("the file selector does not render the note:\n%s", out)
	}
	// Cursor movement is not an acknowledgement.
	m, _ = key(t, m, "j")
	if m.statusNote == "" {
		t.Fatal("moving the cursor wiped the note")
	}
	// A real action replaces it.
	m, _ = key(t, m, "d")
	if m.statusNote != "" {
		t.Fatalf("the note survived a real action: %q", m.statusNote)
	}
}

// A note that lands on a step with nowhere to render it still waits for one
// that does — the branch manager's result reaching the standalone menu, say.
func TestStatusNoteWaitsForAScreenThatShowsIt(t *testing.T) {
	m := Model{step: stepConfig, width: 120, height: 40, statusNote: "Deleted branch old-ui"}
	m, _ = key(t, m, "j")
	if m.statusNote == "" {
		t.Fatal("the note was cleared on a step that never displayed it")
	}
	m.step = stepMenu
	if out := m.viewMenu(); !strings.Contains(out, "Deleted branch old-ui") {
		t.Errorf("the dashboard never got to say what happened:\n%s", out)
	}
}

// One note per operation: the newest result owns it.
func TestANewResultDropsThePreviousNote(t *testing.T) {
	m := Model{step: stepMenu, statusNote: "Undid the last commit"}
	next, _ := m.Update(branchCreateResultMsg{newBranch: "feat"})
	got := next.(Model).statusNote
	if strings.Contains(got, "Undid the last commit") {
		t.Errorf("a stale note survived the next operation: %q", got)
	}
	// The create used to report through its own one-shot field, rendered a few
	// lines above the identical statusNote block.
	if !strings.Contains(got, "Created & switched to feat") {
		t.Errorf("the branch create reported nothing: %q", got)
	}
}

func TestBranchDeleteReportsSuccess(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepBranch)
	m.branchDeleting = true

	next, _ := m.Update(branchDeleteResultMsg{name: "old-ui"})
	got := next.(Model)
	if !strings.Contains(got.statusNote, "Deleted branch old-ui") {
		t.Errorf("a silent delete: note = %q", got.statusNote)
	}
	if out := got.viewBranch(); !strings.Contains(out, "Deleted branch old-ui") {
		t.Errorf("the branch manager does not render it:\n%s", out)
	}
}

func TestPullSuccessSaysWhatArrived(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepSync)
	m.pulling = true
	m.syncCurrTotal = 3
	m.syncReturnStep = stepMenu

	next, _ := m.Update(pullResultMsg{kind: pullKindCurrent})
	got := next.(Model)
	if !strings.Contains(got.statusNote, "Pulled 3 commits from origin/main") {
		t.Errorf("a silent pull: note = %q", got.statusNote)
	}
	if got.step != stepMenu {
		t.Fatalf("step = %v, want stepMenu", got.step)
	}
	if out := got.viewMenu(); !strings.Contains(out, "Pulled 3 commits") {
		t.Errorf("the dashboard does not render it:\n%s", out)
	}
}

func TestGitignoreApplyReportsWhatChanged(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	ignored := file("secret.env")
	ignored.Gitignored = true
	m := wizardModel(t, stepFiles, ignored, file("keep.go"))
	m.gitignoring = true
	m.gitignoreMode = true
	m.removeIgnored = map[string]bool{"dist/": true}
	m.gitignoreCached = []string{"secret.env"}

	next, _ := m.Update(gitignoreResultMsg{})
	got := next.(Model).statusNote
	for _, want := range []string{"Updated .gitignore", "1 entry added", "1 entry removed", "1 file untracked"} {
		if !strings.Contains(got, want) {
			t.Errorf("note %q is missing %q", got, want)
		}
	}
}

func TestSwitchDisclosesTheAutoStash(t *testing.T) {
	m := Model{step: stepBranch, branch: "main", width: 120, height: 40,
		files: []types.FileEntry{file("a.go"), file("b.go"), file("c.go")}, branchSwitching: true}

	next, _ := m.Update(branchSwitchResultMsg{newBranch: "feat", stashRef: "stash@{0}"})
	got := next.(Model)
	if !strings.Contains(got.statusNote, "Switched to feat") {
		t.Fatalf("note = %q", got.statusNote)
	}
	if !strings.Contains(got.statusNote, "3 changed files stashed and restored") {
		t.Errorf("the carried-over changes were not disclosed: %q", got.statusNote)
	}

	// A clean switch says so without inventing a stash.
	clean := Model{step: stepBranch, branch: "main", branchSwitching: true}
	next, _ = clean.Update(branchSwitchResultMsg{newBranch: "feat"})
	if note := next.(Model).statusNote; strings.Contains(note, "stash") {
		t.Errorf("a clean switch claimed a stash: %q", note)
	}
}

// ── Fetch failure handling (M28) ───────────────────────

func TestFailedFetchRetriesOnTheNextMenuReturn(t *testing.T) {
	m := Model{step: stepMenu, hasRemote: true, fetching: true, syncDialogShown: true}

	next, _ := m.Update(fetchResultMsg{err: errTest})
	got := next.(Model)
	if !got.lastFetch.IsZero() {
		t.Fatal("a failed fetch started the debounce clock — the retry is suppressed for 30s")
	}
	if !got.fetchStale {
		t.Fatal("the failure left no trace at all")
	}
	if cmd := got.maybeFetch(); cmd == nil {
		t.Fatal("the retry was debounced away")
	}

	// A later success clears the marker and does start the clock.
	got.fetching = true
	next, _ = got.Update(fetchResultMsg{})
	ok := next.(Model)
	if ok.fetchStale {
		t.Error("the stale marker survived a successful fetch")
	}
	if ok.lastFetch.IsZero() {
		t.Error("a successful fetch did not start the debounce clock")
	}
}

func TestMenuShowsAQuietStaleHint(t *testing.T) {
	m := Model{step: stepMenu, width: 120, height: 40, branch: "main", hasRemote: true, fetchStale: true}
	out := m.viewMenu()
	if !strings.Contains(out, "offline") {
		t.Errorf("the dashboard presents last-known refs as current:\n%s", out)
	}

	// While a fetch is running the spinner speaks for it.
	m.fetching = true
	if out := m.viewMenu(); strings.Contains(out, "offline") {
		t.Errorf("the stale hint shows next to the syncing spinner:\n%s", out)
	}
}

// ── Sync dialog gating ─────────────────────────────────

func TestSyncDialogDoesNotOpenOverAnInFlightOperation(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)
	m.hasRemote = true
	m.fetching = true
	m.branchMerging = true // the menu's "s" shortcut is already running

	next, _ := m.Update(fetchResultMsg{})
	got := next.(Model)
	if got.step == stepSync {
		t.Fatal("the dialog opened on top of an in-flight merge — two operations driving one repo")
	}
	if got.syncDialogShown {
		t.Error("the once-per-session latch was burned while the dialog was suppressed")
	}

	// Once the merge is done the dialog is still available.
	got.branchMerging = false
	got.fetching = true
	next, _ = got.Update(fetchResultMsg{})
	if !next.(Model).syncDialogShown {
		t.Error("the dialog never got its chance after the operation finished")
	}
}

// ── Confirm screen reads git once, not per frame ───────

func TestAmendConfirmUsesCachedGitState(t *testing.T) {
	// Deliberately not a repository: anything the view forks would come back
	// empty and the assertions below would fail.
	t.Chdir(t.TempDir())
	m := wizardModel(t, stepConfirm)
	m.branch = "main"
	m.amendMode = true
	m.amendSHA = "deadbee"
	m.amendPushed = true
	m.msgInput.SetValue("fix: a thing")

	out := m.viewConfirm()
	if !strings.Contains(out, "deadbee") {
		t.Errorf("the cached short SHA is not used:\n%s", out)
	}
	if !strings.Contains(out, "force-with-lease") {
		t.Errorf("the cached pushed-state is not used:\n%s", out)
	}
}

// ── Resize reaches every input ─────────────────────────

func TestWindowResizeReachesTheEditorAndInitInputs(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepFiles)
	m.editMode = true

	// textarea reserves cells for its prompt, so compare against a reference
	// sized the same way rather than against the raw numbers.
	want := func(w, h int) (int, int) {
		ref := textarea.New()
		ref.SetWidth(editAreaWidth(w))
		ref.SetHeight(editAreaHeight(h))
		return ref.Width(), ref.Height()
	}

	next, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	grown := next.(Model)
	if w, h := want(200, 60); grown.editArea.Width() != w || grown.editArea.Height() != h {
		t.Errorf("editor stayed at %dx%d after growing, want %dx%d",
			grown.editArea.Width(), grown.editArea.Height(), w, h)
	}

	next, _ = grown.Update(tea.WindowSizeMsg{Width: 50, Height: 16})
	shrunk := next.(Model)
	if _, h := want(50, 16); shrunk.editArea.Height() != h {
		t.Errorf("editor height = %d after shrinking, want %d — its bottom rows are clipped",
			shrunk.editArea.Height(), h)
	}
	if shrunk.initURLInput.Width > 50 || shrunk.initNameInput.Width > 50 {
		t.Errorf("init inputs still at their fixed width: %d / %d",
			shrunk.initURLInput.Width, shrunk.initNameInput.Width)
	}
}

// ── Phase-2 cluster: file selector, diff, confirm, config ──

// configIndex finds a config row by label so tests don't hardcode the order.
func configIndex(t *testing.T, m Model, label string) int {
	t.Helper()
	for i, item := range m.configItems {
		if item.label == label {
			return i
		}
	}
	t.Fatalf("%q row missing from the config editor", label)
	return -1
}

// gitCmd runs a git command in the current directory (test fixtures only).
func gitCmd(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func manyFiles(n int) []types.FileEntry {
	files := make([]types.FileEntry, n)
	for i := range files {
		files[i] = file(fmt.Sprintf("pkg/file%02d.go", i))
	}
	return files
}

func longDiff(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf(" line %d", i+1)
	}
	return strings.Join(lines, "\n")
}

// M14: `down` used to stop a full window short of the end, so the last hunk of
// every long diff was unreachable — and the counter admitted it.
func TestDiffScrollReachesTheLastLine(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.showDiff = true
	m.diffFile = "a.txt"
	m.diffContent = longDiff(100)

	for i := 0; i < 200; i++ {
		m, _ = key(t, m, "j")
	}

	want := 100 - m.listRows()
	if m.diffScroll != want {
		t.Fatalf("diffScroll = %d, want %d (the last line must be reachable)", m.diffScroll, want)
	}
	out := m.viewDiff()
	if !strings.Contains(out, "line 100") {
		t.Errorf("the last diff line is still unreachable:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("Lines %d-100 of 100", want+1)) {
		t.Errorf("counter does not end at the last line:\n%s", out)
	}
	// And it stops there.
	m, _ = key(t, m, "j")
	if m.diffScroll != want {
		t.Errorf("scrolled past the end: %d", m.diffScroll)
	}
}

// Reopening a diff inside one visit comes back where you were — but only for
// shallow positions; a deep one starts at the top instead of dumping the user
// hundreds of lines in.
func TestDiffScrollMemoryIsShallowOnly(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.diffFile = "a.txt"

	m.diffScroll = 6
	m.rememberDiffScroll()
	if got := m.rememberedDiffScroll("a.txt"); got != 6 {
		t.Errorf("shallow position not remembered: %d", got)
	}

	m.diffScroll = 400
	m.rememberDiffScroll()
	if got := m.rememberedDiffScroll("a.txt"); got != 0 {
		t.Errorf("deep position was restored: %d", got)
	}
	// Never carried across wizard runs.
	m.diffScroll = 6
	m.rememberDiffScroll()
	m.resetWizard()
	if got := m.rememberedDiffScroll("a.txt"); got != 0 {
		t.Errorf("scroll memory survived resetWizard: %d", got)
	}
}

// .gitignore mode renders changed files AND existing entries; that combined
// list had no window at all, so the cursor walked off the bottom of the box.
func TestGitignoreModeKeepsTheCursorOnScreen(t *testing.T) {
	m := wizardModel(t, stepFiles, manyFiles(15)...)
	m.height = 30
	m.gitignoreMode = true
	m.removeIgnored = map[string]bool{}
	for i := 0; i < 20; i++ {
		m.existingIgnored = append(m.existingIgnored, fmt.Sprintf("build/out%02d", i))
	}
	total := len(m.files) + len(m.existingIgnored)

	for i := 0; i < total+5; i++ {
		m, _ = key(t, m, "j")
		start, end := listWindow(total, m.fileScroll, m.gitignoreRows())
		if m.cursor < start || m.cursor >= end {
			t.Fatalf("cursor %d is outside the visible window [%d,%d)", m.cursor, start, end)
		}
	}
	// The cursor is on an existing entry by now, and it is on screen.
	if m.cursor < len(m.files) {
		t.Fatalf("cursor never reached the .gitignore section: %d", m.cursor)
	}
	out := m.viewFiles()
	entry := m.existingIgnored[m.cursor-len(m.files)]
	if !strings.Contains(out, entry) {
		t.Errorf("the row under the cursor (%s) is not rendered:\n%s", entry, out)
	}
	if !strings.Contains(out, symCursor) {
		t.Errorf("no cursor drawn anywhere:\n%s", out)
	}
}

// Jumping out of the filter moved the cursor without moving the window, so the
// selector came back with no visible cursor at all.
func TestFilterJumpSyncsTheScrollWindow(t *testing.T) {
	m := wizardModel(t, stepFiles, manyFiles(40)...)
	m.filterMode = true
	m.filterMatches = computeFilterMatches(m.files, "")
	m.filterCursor = 35

	m, _ = key(t, m, "enter")

	if m.cursor != 35 {
		t.Fatalf("cursor = %d, want 35", m.cursor)
	}
	start, end := listWindow(len(m.files), m.fileScroll, m.fileListRows())
	if m.cursor < start || m.cursor >= end {
		t.Fatalf("cursor %d is outside the window [%d,%d) after the jump", m.cursor, start, end)
	}
	if out := m.viewFiles(); !strings.Contains(out, m.files[35].Path) {
		t.Errorf("the jumped-to file is not on screen:\n%s", out)
	}
}

// Enter with nothing selected used to do nothing and say nothing.
func TestEnterWithNothingSelectedExplainsWhy(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"), file("b.txt"))

	m, _ = key(t, m, "enter")

	if m.step != stepFiles {
		t.Fatalf("step = %v, want stepFiles", m.step)
	}
	if m.err == nil {
		t.Fatal("enter with no selection is still silent")
	}
	out := m.viewFiles()
	if !strings.Contains(out, "press space") {
		t.Errorf("the hint does not name the key that fixes it:\n%s", out)
	}
	// Advisory, not a scary "Error:" banner.
	if strings.Contains(out, "Error:") {
		t.Errorf("an empty selection is not an error:\n%s", out)
	}
}

// Every key the footer advertises is handled, and every handled key is listed.
func TestFileSelectorFootersMatchTheHandlers(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"))
	out := m.viewFiles()
	for _, want := range []string{"select", "all", "filter", "diff", "branch", "ignore", "undo", "menu", "quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("commit-mode footer is missing %q:\n%s", want, out)
		}
	}

	m.gitignoreMode = true
	m.removeIgnored = map[string]bool{}
	out = m.viewFiles()
	for _, want := range []string{"toggle", "all", "confirm", "cancel", "quit"} {
		if !strings.Contains(out, want) {
			t.Errorf(".gitignore footer is missing %q:\n%s", want, out)
		}
	}
}

// The undo prompt says what undo does, warns when the commit is pushed, and
// reads both facts from the model — never from git inside the View.
func TestUndoPromptExplainsWhatUndoDoes(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.confirmUndo = true
	m.undoSubject = "feat: the commit in question"
	m.undoPushed = true
	t.Chdir(t.TempDir()) // not a repo: anything forked from the View returns nothing

	out := m.viewFiles()
	if !strings.Contains(out, "back to staged") {
		t.Errorf("the prompt never says where the changes go:\n%s", out)
	}
	if !strings.Contains(out, "nothing is lost") {
		t.Errorf("the prompt never reassures:\n%s", out)
	}
	if !strings.Contains(out, "feat: the commit in question") {
		t.Errorf("the cached subject is not rendered:\n%s", out)
	}
	if !strings.Contains(out, "diverge") {
		t.Errorf("the pushed-commit warning is gone:\n%s", out)
	}
}

// ── formatError ────────────────────────────────────────

func TestFormatErrorHints(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		rewrote bool
		want    string
		wantNot string
	}{
		{
			name: "git's real branch-name wording",
			// The old pattern looked for "invalid branch name", which git never says.
			msg:  "fatal: 'my branch' is not a valid branch name",
			want: "cannot contain spaces",
		},
		{
			name: "rejected push, ordinary case",
			msg:  "! [rejected] main -> main (non-fast-forward)",
			want: "git pull",
		},
		{
			name:    "rejected push after a rewrite",
			msg:     "! [rejected] main -> main (non-fast-forward)",
			rewrote: true,
			want:    "force-with-lease",
			wantNot: "git pull first",
		},
		{
			// The lease did its job: origin moved, so nothing was overwritten.
			// Answering that with the force advice would be a retry loop.
			name:    "force-with-lease refused because origin moved",
			msg:     "! [rejected] main -> main (stale info)",
			rewrote: true,
			want:    "Pull first",
			wantNot: "Press f",
		},
		{
			name: "merge would clobber local work",
			msg:  "error: Your local changes to the following files would be overwritten by merge:",
			want: "stash",
		},
		{
			// git's stash output contains the word CONFLICT, and the generic
			// hint for that ("before committing") answers a question nobody
			// asked — there is no commit in progress on the stash screen.
			// Traced against real git: `git checkout -- .` alone fails on
			// unmerged paths, so the cancel advice has to be the two-step.
			name:    "conflicted stash apply",
			msg:     "applying stash abc1234 conflicted — conflict markers are now in a.txt, and the stash itself was kept, so nothing is lost",
			want:    "git reset HEAD` then `git checkout -- .",
			wantNot: "before committing",
		},
		{
			// Same trap the other way: git's refusal literally says "would be
			// overwritten by merge", but nothing is merging here.
			name:    "stash refused by a dirty tree",
			msg:     "could not restore stash abc1234 — uncommitted changes in your working tree cover the same files, so git stopped before touching anything",
			want:    "Commit or stash your current changes first, then retry.",
			wantNot: "the merge would overwrite them",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatErrorCtx(errors.New(tc.msg), tc.rewrote)
			if !strings.Contains(got, tc.want) {
				t.Errorf("hint %q missing from:\n%s", tc.want, got)
			}
			if tc.wantNot != "" && strings.Contains(got, tc.wantNot) {
				t.Errorf("hint still contains %q:\n%s", tc.wantNot, got)
			}
		})
	}
}

// Amending a commit that is already on origin is what makes the next push fail;
// the advice has to follow that, not the generic "pull first".
func TestPushHintFollowsAnAmendOfAPushedCommit(t *testing.T) {
	m := wizardModel(t, stepConfirm, file("a.txt"))
	m.hasRemote = true
	m.amendMode = true
	m.amendPushed = true
	m.committing = true

	next, _ := m.Update(commitResultMsg{hash: "abc1234", stats: "1 file changed"})
	m = next.(Model)
	if !m.historyRewritten {
		t.Fatal("the amend of a pushed commit was not recorded")
	}

	m.step = stepPush
	m.err = errors.New("! [rejected] main -> main (non-fast-forward)")
	out := m.viewPush()
	if !strings.Contains(out, "force-with-lease") {
		t.Errorf("the push hint would undo the amend:\n%s", out)
	}
	if strings.Contains(out, "git pull first") {
		t.Errorf("the push hint still offers the pull that undoes the amend:\n%s", out)
	}
}

// ── Confirm ────────────────────────────────────────────

// M15: the review gate used to show five paths and "... and 25 more".
func TestConfirmListScrollsInsteadOfTruncating(t *testing.T) {
	files := manyFiles(30)
	for i := range files {
		files[i].Selected = true
	}
	m := wizardModel(t, stepConfirm, files...)
	m.height = 30
	m.msgInput.SetValue("something")

	out := m.viewConfirm()
	if strings.Contains(out, "and 25 more") {
		t.Errorf("still capped at five entries:\n%s", out)
	}
	if !strings.Contains(out, "of 30") {
		t.Errorf("no \"N of M\" indicator:\n%s", out)
	}

	rows := m.confirmListRows()
	if rows >= 30 {
		t.Fatalf("test needs a windowed list, rows = %d", rows)
	}
	// The last file is reachable.
	for i := 0; i < 40; i++ {
		m, _ = key(t, m, "j")
	}
	if want := 30 - rows; m.confirmScroll != want {
		t.Fatalf("confirmScroll = %d, want %d", m.confirmScroll, want)
	}
	if out := m.viewConfirm(); !strings.Contains(out, files[29].Path) {
		t.Errorf("the last selected file is unreachable:\n%s", out)
	}
	// And back up.
	for i := 0; i < 40; i++ {
		m, _ = key(t, m, "up")
	}
	if m.confirmScroll != 0 {
		t.Errorf("scrolling back up left an offset of %d", m.confirmScroll)
	}
}

// Non-amend commits cannot reach the confirm step with nothing selected — the
// file step refuses to advance — so "0 file(s):" is unreachable there.
func TestZeroFileConfirmIsUnreachableOutsideAmend(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"))
	m, _ = key(t, m, "enter")
	if m.step == stepConfirm || m.step == stepType {
		t.Fatal("the wizard advanced with nothing selected")
	}
	// The amend wording is the one that is reachable, and it is not "0 file(s)".
	m = wizardModel(t, stepConfirm)
	m.amendMode = true
	if out := m.viewConfirm(); strings.Contains(out, "0 file(s)") {
		t.Errorf("message-only amend still says 0 file(s):\n%s", out)
	}
}

// ── Custom commit type ─────────────────────────────────

func TestCustomTypeValidation(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "hotfix", want: "hotfix"},
		{in: "  hotfix  ", want: "hotfix"},
		{in: "HOTFIX", want: "hotfix"},
		{in: "", wantErr: "empty"},
		{in: "   ", wantErr: "empty"},
		{in: "Bug Fix", wantErr: "single word"},
		{in: "two\twords", wantErr: "single word"},
	}
	for _, tc := range cases {
		got, err := validateCustomType(tc.in)
		if tc.wantErr != "" {
			if err == nil {
				t.Errorf("validateCustomType(%q) accepted it", tc.in)
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("validateCustomType(%q) error = %v, want mention of %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("validateCustomType(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("validateCustomType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCustomTypeRejectionStaysOnTheStepAndSaysWhy(t *testing.T) {
	m := wizardModel(t, stepCustom)
	m.customInput.SetValue("Bug Fix")

	m, _ = key(t, m, "enter")

	if m.step != stepCustom {
		t.Fatalf("step = %v, want stepCustom", m.step)
	}
	if m.commitType != "" {
		t.Errorf("an invalid type reached the commit prefix: %q", m.commitType)
	}
	if out := m.viewCustom(); !strings.Contains(out, "single word") {
		t.Errorf("the custom-type screen renders no error at all:\n%s", out)
	}

	// A valid one is lowercased on the way through.
	m.customInput.SetValue("HotFix")
	m, _ = key(t, m, "enter")
	if m.commitType != "hotfix" {
		t.Errorf("commitType = %q, want hotfix", m.commitType)
	}
	if m.step != stepMessage {
		t.Errorf("step = %v, want stepMessage", m.step)
	}
}

func TestCustomTypeCounterAppearsNearTheLimit(t *testing.T) {
	m := wizardModel(t, stepCustom)
	m.customInput.SetValue("short")
	if out := m.viewCustom(); strings.Contains(out, "characters") {
		t.Errorf("counter shown far from the limit:\n%s", out)
	}
	m.customInput.SetValue(strings.Repeat("x", customTypeCounterAt))
	out := m.viewCustom()
	if !strings.Contains(out, fmt.Sprintf("%d/%d characters", customTypeCounterAt, customTypeLimit)) {
		t.Errorf("no counter near the limit:\n%s", out)
	}
	if m.customInput.CharLimit != customTypeLimit {
		t.Errorf("CharLimit = %d, want %d", m.customInput.CharLimit, customTypeLimit)
	}
}

// ── Message body navigation ────────────────────────────

// The arrow keys never reached the body textarea: up/down were swallowed by the
// field-navigation switch, leaving only the undiscoverable ctrl+p/ctrl+n.
func TestBodyArrowsNavigateTextAndLeaveOnlyFromTheTop(t *testing.T) {
	m := wizardModel(t, stepMessage, file("a.txt"))
	m.commitType = "feat"
	m.showBody = true
	m.bodyFocused = true
	m.bodyInput.SetValue("first\nsecond\nthird")
	m.bodyInput.Focus()
	if got := m.bodyInput.Line(); got != 2 {
		t.Fatalf("setup: cursor on line %d, want 2", got)
	}

	m, _ = key(t, m, "up")
	if !m.bodyFocused {
		t.Fatal("up in the middle of the body jumped out of the field")
	}
	if got := m.bodyInput.Line(); got != 1 {
		t.Fatalf("cursor on line %d, want 1", got)
	}

	m, _ = key(t, m, "down")
	if !m.bodyFocused || m.bodyInput.Line() != 2 {
		t.Fatalf("down inside the body left the field (focused=%v line=%d)", m.bodyFocused, m.bodyInput.Line())
	}

	// Walk to the top, then one more up leaves for the subject.
	m, _ = key(t, m, "up")
	m, _ = key(t, m, "up")
	if got := m.bodyInput.Line(); got != 0 {
		t.Fatalf("cursor on line %d, want 0", got)
	}
	if !m.bodyFocused {
		t.Fatal("left the body before reaching its first line")
	}
	m, _ = key(t, m, "up")
	if m.bodyFocused {
		t.Fatal("up on the first line did not move focus to the subject")
	}
	if !m.msgInput.Focused() {
		t.Error("the subject input was not focused")
	}
}

// ── Push / Done truthfulness ───────────────────────────

func TestFailedPushStaysOnTheStepAndDoneTellsTheTruth(t *testing.T) {
	m := wizardModel(t, stepPush, file("a.txt"))
	m.hasRemote = true
	m.pushing = true
	m.msgInput.SetValue("do things")
	m.commitType = "feat"

	next, _ := m.Update(pushResultMsg{err: errors.New("! [rejected] main -> main (fetch first)")})
	m = next.(Model)
	if m.step != stepPush {
		t.Fatalf("step = %v, want stepPush — a failed push must not walk forward", m.step)
	}
	if !m.pushFailed || m.pushErr == nil {
		t.Fatal("the failure was not remembered")
	}

	// The keypress that leaves clears m.err; the Done screen must still know.
	m, _ = key(t, m, "n")
	if m.step != stepDone {
		t.Fatalf("step = %v, want stepDone", m.step)
	}
	out := m.viewDone()
	if strings.Contains(out, "Push skipped") {
		t.Errorf("a failed push is reported as a deliberate skip:\n%s", out)
	}
	if !strings.Contains(out, "Push failed") {
		t.Errorf("Done never mentions the failure:\n%s", out)
	}
	if !strings.Contains(out, "rejected") {
		t.Errorf("Done drops the reason entirely:\n%s", out)
	}
	if strings.Contains(out, "All done!") {
		t.Errorf("\"All done!\" after a failed push:\n%s", out)
	}
	// And the breadcrumb does not check-mark a push that failed.
	if strings.Contains(out, symDone+" Push") {
		t.Errorf("breadcrumb check-marks the failed push:\n%s", out)
	}
}

func TestEscOnPushIsLabelledAsASkip(t *testing.T) {
	m := wizardModel(t, stepPush, file("a.txt"))
	m.hasRemote = true
	m.pushHasUpstream = true
	m.msgInput.SetValue("do things")
	m.commitType = "feat"

	out := m.viewPush()
	if !strings.Contains(out, "n/esc") {
		t.Errorf("esc's meaning is still undiscoverable:\n%s", out)
	}
	if !strings.Contains(out, "commit is already made") {
		t.Errorf("the footer does not say why skipping is safe:\n%s", out)
	}

	m, _ = key(t, m, "esc")
	if m.step != stepDone {
		t.Fatalf("step = %v, want stepDone", m.step)
	}
	if out := m.viewDone(); !strings.Contains(out, "Push skipped") {
		t.Errorf("a real skip should say so:\n%s", out)
	}
}

// The breadcrumb used to check-mark a Push step that amends never take.
func TestAmendBreadcrumbDropsPushAndStatesTheNextStep(t *testing.T) {
	m := wizardModel(t, stepDone, file("a.txt"))
	m.hasRemote = true
	m.amendMode = true
	m.amendPushed = true
	m.commitHash = "abc1234"
	m.msgInput.SetValue("fix things")
	m.commitType = "fix"

	names, _ := m.progressPlan()
	for _, n := range names {
		if n == "Push" {
			t.Fatalf("Push is still in the amend breadcrumb: %v", names)
		}
	}
	out := m.viewDone()
	if strings.Contains(out, "Push skipped") {
		t.Errorf("an amend does not \"skip\" a push:\n%s", out)
	}
	if !strings.Contains(out, "force-with-lease") {
		t.Errorf("Done never names the push this state needs:\n%s", out)
	}

	// Without a remote, the breadcrumb drops Push for ordinary commits too.
	m2 := wizardModel(t, stepConfirm, file("a.txt"))
	m2.hasRemote = false
	names, _ = m2.progressPlan()
	for _, n := range names {
		if n == "Push" {
			t.Fatalf("Push is in the breadcrumb of a repo with no remote: %v", names)
		}
	}
}

// R6: the Push and Done screens read the commit facts off the model. Rendered
// outside a repository, anything they forked would come back empty.
func TestPushAndDoneRenderCachedCommitFacts(t *testing.T) {
	m := wizardModel(t, stepDone, file("a.txt"))
	m.hasRemote = true
	m.commitHash = "deadbee"
	m.commitStats = "2 files changed, 4 insertions(+)"
	m.msgInput.SetValue("do things")
	m.commitType = "feat"
	t.Chdir(t.TempDir()) // not a git repo

	done := m.viewDone()
	if !strings.Contains(done, "deadbee") || !strings.Contains(done, "2 files changed") {
		t.Errorf("Done does not render the cached commit facts:\n%s", done)
	}
	m.step = stepPush
	push := m.viewPush()
	if !strings.Contains(push, "2 files changed") {
		t.Errorf("Push does not render the cached stats:\n%s", push)
	}
}

// ── Config editor ──────────────────────────────────────

func TestConfigHeaderShowsTheAppTitleAndBranch(t *testing.T) {
	m := wizardModel(t, stepConfig)
	m.branch = "feature/x"
	m.loadConfigItems()
	out := m.viewConfig()
	if !strings.Contains(out, "git-assist") {
		t.Errorf("config screen has no app title:\n%s", out)
	}
	if !strings.Contains(out, "feature/x") {
		t.Errorf("config screen never says which repo/branch it is editing:\n%s", out)
	}
}

func TestConfigEmptyValueUnsetsTheKey(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	gitCmd(t, "config", "--local", "user.name", "Someone")

	m := wizardModel(t, stepConfig)
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "User name")

	m, _ = key(t, m, "enter")
	if !m.configEditMode {
		t.Fatal("enter did not open the inline editor")
	}
	m.configEditInput.SetValue("")
	m, _ = key(t, m, "enter")

	if err := exec.Command("git", "config", "--local", "user.name").Run(); err == nil {
		t.Error("the key was written empty instead of unset")
	}
	if item := m.configItems[configIndex(t, m, "User name")]; item.set {
		t.Errorf("the editor still shows the key as set: %+v", item)
	}
	if out := m.viewConfig(); !strings.Contains(out, "not set") {
		t.Errorf("the \"not set\" hint never came back:\n%s", out)
	}
}

func TestConfigRemoteDeleteAsksFirst(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	gitCmd(t, "remote", "add", "origin", "https://example.invalid/x.git")

	m := wizardModel(t, stepConfig)
	m.hasRemote = true
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "Remote URL")

	m, _ = key(t, m, "enter")
	m.configEditInput.SetValue("")
	m, _ = key(t, m, "enter")

	if !m.configRemoveRemote {
		t.Fatal("an emptied remote URL deleted origin with no confirmation")
	}
	if git.GetRemoteURL() == "" {
		t.Fatal("origin was already gone before the confirmation")
	}
	if out := m.viewConfig(); !strings.Contains(out, "Remove remote origin?") {
		t.Errorf("the confirmation is not rendered:\n%s", out)
	}

	// Declining keeps it.
	m, _ = key(t, m, "n")
	if m.configRemoveRemote {
		t.Error("the prompt stayed up after declining")
	}
	if git.GetRemoteURL() == "" {
		t.Fatal("declining still removed origin")
	}

	// Confirming removes it.
	m, _ = key(t, m, "enter")
	m.configEditInput.SetValue("")
	m, _ = key(t, m, "enter")
	m, _ = key(t, m, "y")
	if git.GetRemoteURL() != "" {
		t.Error("confirming did not remove origin")
	}
	if m.hasRemote {
		t.Error("hasRemote was not refreshed after the removal")
	}
}

func TestConfigTabDuringEditCancelsTheEdit(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	gitCmd(t, "config", "--local", "user.name", "Local Person")

	m := wizardModel(t, stepConfig)
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "User name")
	m, _ = key(t, m, "enter")
	m.configEditInput.SetValue("Half Typed")

	m, _ = key(t, m, "tab")

	if m.configEditMode {
		t.Fatal("the editor stayed open across a scope switch")
	}
	if !m.configGlobal {
		t.Fatal("tab did not switch scope")
	}
	if m.err == nil {
		t.Error("the cancelled edit was not disclosed")
	}
	// The half-typed value went nowhere.
	out, _ := exec.Command("git", "config", "--local", "user.name").Output()
	if strings.TrimSpace(string(out)) != "Local Person" {
		t.Errorf("local user.name = %q, want the untouched original", strings.TrimSpace(string(out)))
	}
}

func TestConfigGPGToggleUnderstandsGitSpellings(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	gitCmd(t, "config", "--local", "commit.gpgsign", "1")

	m := wizardModel(t, stepConfig)
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "GPG signing")

	if out := m.viewConfig(); !strings.Contains(out, "on") {
		t.Errorf("commit.gpgsign = 1 is rendered as off:\n%s", out)
	}

	// One press turns it off — it used to write "true" over "1" and need two.
	m, _ = key(t, m, "enter")
	out, err := exec.Command("git", "config", "--local", "commit.gpgsign").Output()
	if err != nil {
		t.Fatalf("git config: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "false" {
		t.Errorf("commit.gpgsign = %q after one toggle, want false", got)
	}
}

// ── Merge / pull reporting (R1, R2) ────────────────────

func TestMergeWithNothingToMergeIsNotReportedAsAMerge(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepBranch)
	m.branchMerging = true

	next, _ := m.Update(branchMergeResultMsg{source: "feature", merged: true, upToDate: true})
	got := next.(Model)
	if strings.Contains(got.statusNote, "Merged feature into") {
		t.Errorf("a no-op merge claims to have merged: %q", got.statusNote)
	}
	if !strings.Contains(got.statusNote, "Already up to date") {
		t.Errorf("note = %q, want an \"already up to date\" report", got.statusNote)
	}

	// A real merge still reads as one.
	next, _ = m.Update(branchMergeResultMsg{source: "feature", merged: true})
	if note := next.(Model).statusNote; !strings.Contains(note, "Merged feature into") {
		t.Errorf("a real merge lost its note: %q", note)
	}
}

func TestPullReportsUpToDateAndTheStashRoundTrip(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	m := wizardModel(t, stepSync)
	m.pulling = true
	m.syncCurrTotal = 3 // pre-operation counts must not invent an arrival
	next, _ := m.Update(pullResultMsg{kind: pullKindCurrent, upToDate: true})
	if note := next.(Model).statusNote; !strings.Contains(note, "Already up to date") {
		t.Errorf("note = %q, want an \"already up to date\" report", note)
	}

	m = wizardModel(t, stepSync)
	m.pulling = true
	m.syncCurrTotal = 2
	next, _ = m.Update(pullResultMsg{kind: pullKindCurrent, stashRestored: true})
	note := next.(Model).statusNote
	if !strings.Contains(note, "Pulled") {
		t.Errorf("note = %q, want the pull reported", note)
	}
	if !strings.Contains(note, "stashed and restored") {
		t.Errorf("the auto-stash round trip is still silent: %q", note)
	}
}

// ── The push loop ──────────────────────────────────────
//
// stepPush used to be reachable ONLY in the seconds after a commit, it offered
// a branch PICKER whose every non-current entry pushed the current commits onto
// somebody else's remote branch, and the one push it could never run — the
// force-with-lease an amend makes necessary — was printed as a command to type
// somewhere else.

// tempRepoWithOrigin is tempRepo plus a bare repository wired up as origin,
// with the seed commit already published and tracking set up. Returns origin's
// path. The push loop is a relationship between two repositories; there is no
// honest way to exercise it against one.
func tempRepoWithOrigin(t *testing.T, subject string) string {
	t.Helper()
	tempRepo(t, subject, "")
	origin := t.TempDir()
	gitRun(t, "init", "-q", "--bare", origin)
	gitRun(t, "remote", "add", "origin", origin)
	gitRun(t, "push", "-q", "-u", "origin", "main")
	return origin
}

// gitOut runs a read-only git command and returns its output.
func gitOut(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

// hasMenuItem reports whether the dashboard is currently offering an entry,
// and what it says about it.
func hasMenuItem(m Model, name string) (menuItem, bool) {
	for _, it := range m.menuItems() {
		if it.name == name {
			return it, true
		}
	}
	return menuItem{}, false
}

func TestMenuPushEntryVisibility(t *testing.T) {
	base := func() Model {
		return Model{hasRemote: true, hasAnyCommit: true, hasUpstream: true}
	}
	cases := []struct {
		name string
		tune func(*Model)
		want string // "" = the entry must not be offered
	}{
		{
			name: "commits waiting to go out",
			tune: func(m *Model) { m.aheadOrigin = 3 },
			want: "3 ahead",
		},
		{
			name: "branch origin has never seen",
			tune: func(m *Model) { m.hasUpstream = false },
			want: "publish branch",
		},
		{
			name: "history rewritten under a pushed commit",
			tune: func(m *Model) { m.historyRewritten = true; m.aheadOrigin = 1 },
			want: "force required",
		},
		{
			name: "a rewrite outranks a plain backlog",
			tune: func(m *Model) { m.historyRewritten = true; m.aheadOrigin = 4; m.hasUpstream = false },
			want: "force required",
		},
		{
			name: "in sync — nothing to push",
			tune: func(m *Model) {},
		},
		{
			// Behind is the PULL case. Offering Push here is how a beginner
			// ends up force-pushing over somebody else's work.
			name: "merely behind origin",
			tune: func(m *Model) { m.behindOrigin = 5 },
		},
		{
			name: "no remote at all",
			tune: func(m *Model) { m.hasRemote = false; m.aheadOrigin = 2 },
		},
		{
			name: "repository with no commits yet",
			tune: func(m *Model) { m.hasAnyCommit = false; m.hasUpstream = false },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base()
			tc.tune(&m)
			item, ok := hasMenuItem(m, "Push")
			if tc.want == "" {
				if ok {
					t.Fatalf("Push offered as %q when it should not be", item.desc)
				}
				return
			}
			if !ok {
				t.Fatalf("Push entry missing; menu = %v", m.menuItems())
			}
			if !strings.Contains(item.desc, tc.want) {
				t.Errorf("Push desc = %q, want it to mention %q", item.desc, tc.want)
			}
		})
	}
}

// Everything the screen shows is read on entry — never from the View, which
// runs on every keypress, resize and spinner tick.
func TestEnterPushCachesWhatTheScreenShows(t *testing.T) {
	tempRepoWithOrigin(t, "chore: seed")
	writeFile(t, "a.txt", "a\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "feat: one")
	writeFile(t, "b.txt", "b\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "feat: two")

	m := wizardModel(t, stepMenu)
	m.hasRemote = true
	m.enterPush(true)

	if m.step != stepPush {
		t.Fatalf("step = %v, want stepPush", m.step)
	}
	if !m.pushReturnToMenu {
		t.Error("the menu entry point was not recorded")
	}
	if m.pushOutgoingTotal != 2 {
		t.Errorf("pushOutgoingTotal = %d, want 2", m.pushOutgoingTotal)
	}
	if len(m.pushOutgoing) != 2 {
		t.Errorf("pushOutgoing = %v, want both subjects", m.pushOutgoing)
	}
	if !m.pushHasUpstream || m.pushAhead != 2 || m.pushBehind != 0 {
		t.Errorf("upstream=%v ahead=%d behind=%d, want true/2/0", m.pushHasUpstream, m.pushAhead, m.pushBehind)
	}
	if m.pushForce {
		t.Error("force offered on a branch whose history was never rewritten")
	}
	if m.pushBranch != m.branch {
		t.Errorf("pushBranch = %q, want the current branch %q", m.pushBranch, m.branch)
	}

	// Rendered from a directory that is not a repository: anything the View
	// forked would come back empty.
	t.Chdir(t.TempDir())
	out := m.viewPush()
	for _, want := range []string{"Push 2 commits to origin/main", "feat: one", "feat: two"} {
		if !strings.Contains(out, want) {
			t.Errorf("push screen never says %q:\n%s", want, out)
		}
	}
	// The picker is gone: no branch list, no navigation.
	if strings.Contains(out, "Select branch") || strings.Contains(out, symArrows) {
		t.Errorf("the branch picker survived:\n%s", out)
	}
}

func TestEnterPushOnAnUnpublishedBranchIsAPublish(t *testing.T) {
	tempRepoWithOrigin(t, "chore: seed")
	gitRun(t, "checkout", "-q", "-b", "feat")
	writeFile(t, "a.txt", "a\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "feat: work")

	m := wizardModel(t, stepMenu)
	m.branch = "feat"
	m.hasRemote = true
	m.enterPush(true)

	if m.pushHasUpstream {
		t.Fatal("a branch origin has never seen reports an upstream")
	}
	out := m.viewPush()
	if !strings.Contains(out, "Publish branch feat") {
		t.Errorf("the publish case is not named:\n%s", out)
	}
	if !strings.Contains(out, "tracking") {
		t.Errorf("nothing says the push sets tracking up:\n%s", out)
	}
}

// The safety property of the whole feature: force is offered only after THIS
// session rewrote a commit origin already had.
func TestForcePushIsNeverOfferedWithoutARewrite(t *testing.T) {
	cases := []struct {
		name string
		m    Model
		want bool
	}{
		{
			name: "diverged after an amend of a pushed commit",
			m:    Model{historyRewritten: true, pushAhead: 1, pushBehind: 1, pushHasUpstream: true},
			want: true,
		},
		{
			name: "the amend flow knows before the counts do",
			m:    Model{historyRewritten: true, amendPushed: true, pushHasUpstream: true},
			want: true,
		},
		{
			// Diverged, but nothing here rewrote anything: the branch forked
			// or someone else pushed. Forcing deletes their commits.
			name: "diverged without a rewrite",
			m:    Model{pushAhead: 2, pushBehind: 3, pushHasUpstream: true},
			want: false,
		},
		{
			name: "merely behind",
			m:    Model{pushBehind: 4, pushHasUpstream: true},
			want: false,
		},
		{
			name: "amendPushed left over from an earlier amend, no rewrite recorded",
			m:    Model{amendPushed: true, pushHasUpstream: true, pushBehind: 1},
			want: false,
		},
		{
			name: "rewritten, but origin agrees again (already force-pushed)",
			m:    Model{historyRewritten: true, pushAhead: 0, pushBehind: 0, pushHasUpstream: true},
			want: false,
		},
		{
			// The dangerous one. We rewrote a commit origin had, and then
			// somebody pushed on top of it — which our own background fetch
			// dutifully imported. A force here deletes their work, and git's
			// default lease would not stop it.
			name: "somebody pushed on top of the commit we rewrote",
			m: Model{
				historyRewritten: true, amendPushed: true, pushHasUpstream: true,
				pushAhead: 1, pushBehind: 2,
				rewriteBaseSHA: "aaaa111", pushLeaseSHA: "bbbb222",
			},
			want: false,
		},
		{
			name: "origin still holds exactly the commit we rewrote",
			m: Model{
				historyRewritten: true, pushHasUpstream: true,
				pushAhead: 1, pushBehind: 1,
				rewriteBaseSHA: "aaaa111", pushLeaseSHA: "aaaa111",
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.forcePushRequired(); got != tc.want {
				t.Errorf("forcePushRequired() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Force mode is entered with `f`, deliberately not with enter: replacing a
// commit on origin is not something to walk into with the key that means next.
func TestForcePushNeedsItsOwnKey(t *testing.T) {
	m := wizardModel(t, stepPush)
	m.hasRemote = true
	m.pushForce = true
	m.pushHasUpstream = true
	m.pushAhead, m.pushBehind = 1, 1

	next, cmd := key(t, m, "enter")
	if cmd != nil || next.pushing {
		t.Fatal("enter ran the force push")
	}
	next, cmd = key(t, m, "f")
	if cmd == nil || !next.pushing {
		t.Fatal("f did not dispatch the force push")
	}

	out := m.viewPush()
	for _, want := range []string{"Force push required", "rewritten", "rejected", "nobody else pushed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the force explainer never says %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "f force-push") {
		t.Errorf("the help bar does not offer f:\n%s", out)
	}

	// And f is inert on an ordinary push, where there is nothing to force.
	plain := wizardModel(t, stepPush)
	plain.hasRemote = true
	plain.pushHasUpstream = true
	if next, cmd := key(t, plain, "f"); cmd != nil || next.pushing {
		t.Fatal("f force-pushed a branch that never rewrote anything")
	}
}

func TestMenuInitiatedPushReturnsToTheMenuWithANote(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	cases := []struct {
		name  string
		tune  func(*Model)
		msg   pushResultMsg
		wants []string
	}{
		{
			name:  "ordinary push",
			tune:  func(m *Model) { m.pushHasUpstream = true; m.pushOutgoingTotal = 3 },
			wants: []string{"Pushed 3 commits to origin/main"},
		},
		{
			name:  "publish",
			tune:  func(m *Model) { m.pushHasUpstream = false; m.branch = "feat" },
			wants: []string{"Published branch feat to origin"},
		},
		{
			name:  "force-with-lease",
			tune:  func(m *Model) { m.pushHasUpstream = true; m.pushOutgoingTotal = 1; m.historyRewritten = true },
			msg:   pushResultMsg{forced: true},
			wants: []string{"Force-pushed 1 commit to origin/main", "replaced"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := wizardModel(t, stepPush)
			m.hasRemote = true
			m.pushReturnToMenu = true
			m.pushing = true
			tc.tune(&m)

			next, _ := m.Update(tc.msg)
			out := next.(Model)
			if out.step != stepMenu {
				t.Fatalf("step = %v, want stepMenu — a push started from the dashboard belongs back there", out.step)
			}
			for _, want := range tc.wants {
				if !strings.Contains(out.statusNote, want) {
					t.Errorf("note = %q, want it to contain %q", out.statusNote, want)
				}
			}
			if out.historyRewritten {
				t.Error("a successful push left the rewrite flag set — the menu keeps offering a force")
			}
		})
	}
}

// The wizard's own push still ends on the summary screen it was written for.
func TestWizardPushStillEndsOnDone(t *testing.T) {
	m := wizardModel(t, stepPush, file("a.txt"))
	m.hasRemote = true
	m.pushing = true
	m.pushHasUpstream = true
	m.pushOutgoingTotal = 1
	m.pushBranch = "main"

	next, _ := m.Update(pushResultMsg{})
	out := next.(Model)
	if out.step != stepDone {
		t.Fatalf("step = %v, want stepDone", out.step)
	}
	if out.statusNote != "" {
		t.Errorf("the wizard path invented a menu note: %q", out.statusNote)
	}
	if !strings.Contains(out.viewDone(), "Pushed to") {
		t.Errorf("Done does not report the push:\n%s", out.viewDone())
	}
}

// Skipping follows the same rule as succeeding: back where you came from.
func TestPushSkipRoutingFollowsTheEntryPoint(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	fromMenu := wizardModel(t, stepPush)
	fromMenu.hasRemote = true
	fromMenu.pushReturnToMenu = true
	if next, _ := key(t, fromMenu, "esc"); next.step != stepMenu {
		t.Errorf("step = %v, want stepMenu", next.step)
	}
	if !strings.Contains(fromMenu.viewPush(), "back to menu") {
		t.Errorf("the footer mislabels the exit:\n%s", fromMenu.viewPush())
	}

	fromWizard := wizardModel(t, stepPush, file("a.txt"))
	fromWizard.hasRemote = true
	if next, _ := key(t, fromWizard, "esc"); next.step != stepDone {
		t.Errorf("step = %v, want stepDone", next.step)
	}
}

// A menu-initiated push bypasses the wizard entirely, so the wizard's own reset
// never runs: the second push of a session used to open on the first one's
// error banner and its "already pushed" verdict.
func TestEnterPushClearsThePreviousAttempt(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	m := wizardModel(t, stepMenu)
	m.hasRemote = true
	m.pushed = true
	m.pushFailed = true
	m.pushErr = errors.New("! [rejected] main -> main (fetch first)")
	m.pushCheckDone = true

	m.enterPush(true)
	if m.pushed || m.pushFailed || m.pushErr != nil {
		t.Errorf("a fresh visit inherited the previous push's verdict: pushed=%v failed=%v err=%v",
			m.pushed, m.pushFailed, m.pushErr)
	}
	if m.pushCheckDone {
		t.Error("the behind-origin check was still latched from the last visit")
	}
}

// The end of the amend loop: Done offers the force-with-lease as a keypress
// instead of printing a command for the user to run in a terminal.
func TestDoneOffersTheForcePushAfterAmendingAPushedCommit(t *testing.T) {
	tempRepoWithOrigin(t, "chore: seed")
	writeFile(t, "seed.txt", "amended\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "--amend", "-m", "chore: seed amended")

	m := wizardModel(t, stepDone)
	m.hasRemote = true
	m.amendMode = true
	m.amendPushed = true
	m.historyRewritten = true
	m.commitHash = "abc1234"

	out := m.viewDone()
	if strings.Contains(out, "git push --force-with-lease") {
		t.Errorf("Done still tells the user to run the command by hand:\n%s", out)
	}
	if !strings.Contains(out, "p push now (force-with-lease)") {
		t.Errorf("Done does not offer the in-app push:\n%s", out)
	}

	m, _ = key(t, m, "p")
	if m.step != stepPush {
		t.Fatalf("step = %v, want stepPush", m.step)
	}
	if !m.pushForce {
		t.Fatal("the push screen did not come up in force mode after a rewrite")
	}
	if m.pushReturnToMenu {
		t.Error("a push started from Done should return to Done")
	}
	if m.pushAhead != 1 || m.pushBehind != 1 {
		t.Errorf("ahead=%d behind=%d, want the divergence the amend created", m.pushAhead, m.pushBehind)
	}
	// The lease is pinned to the commit the screen is describing. Leaving it to
	// git's default would lease against the remote-tracking ref, which this app
	// moves on its own every 30 seconds — see git.PushForceWithLease.
	originTip := strings.TrimSpace(string(gitOut(t, "rev-parse", "refs/remotes/origin/main")))
	if m.pushLeaseSHA == "" || m.pushLeaseSHA != originTip {
		t.Errorf("pushLeaseSHA = %q, want origin's tip %q", m.pushLeaseSHA, originTip)
	}

	// And the force push itself clears the rewrite, so neither the menu nor a
	// later push keeps offering one.
	m.pushing = true
	next, _ := m.Update(pushResultMsg{forced: true})
	after := next.(Model)
	if after.historyRewritten || after.pushForce {
		t.Error("a completed force push left the rewrite state behind")
	}
	if after.step != stepDone {
		t.Errorf("step = %v, want stepDone", after.step)
	}
	if _, ok := hasMenuItem(after, "Push"); ok && after.aheadOrigin == 0 && after.hasUpstream {
		t.Error("the menu still offers a push after everything was pushed")
	}
}

// A plain push rejected as non-fast-forward AFTER a rewrite is exactly what
// force-with-lease exists for — the screen must offer it rather than ending the
// session with advice about another program.
func TestRejectedPushAfterARewriteOffersTheForce(t *testing.T) {
	m := wizardModel(t, stepPush)
	m.hasRemote = true
	m.pushing = true
	m.pushHasUpstream = true
	m.historyRewritten = true
	m.pushAhead, m.pushBehind = 1, 1

	next, _ := m.Update(pushResultMsg{err: errors.New("! [rejected] main -> main (non-fast-forward)")})
	out := next.(Model)
	if !out.pushForce {
		t.Fatal("the rejection did not open the force-with-lease path")
	}
	if out.step != stepPush {
		t.Fatalf("step = %v, want stepPush", out.step)
	}

	// Without a rewrite the same rejection means origin has work we don't —
	// the answer is a pull, and force must stay unreachable.
	plain := wizardModel(t, stepPush)
	plain.hasRemote = true
	plain.pushing = true
	plain.pushHasUpstream = true
	plain.pushAhead, plain.pushBehind = 1, 1
	next, _ = plain.Update(pushResultMsg{err: errors.New("! [rejected] main -> main (fetch first)")})
	if next.(Model).pushForce {
		t.Fatal("a plain rejection unlocked a force push")
	}

	// And a rejection cannot unlock a force the entry gate would have refused:
	// origin has moved past the commit we rewrote.
	moved := wizardModel(t, stepPush)
	moved.hasRemote = true
	moved.pushing = true
	moved.pushHasUpstream = true
	moved.historyRewritten = true
	moved.pushAhead, moved.pushBehind = 1, 2
	moved.rewriteBaseSHA = "aaaa111"
	moved.pushLeaseSHA = "bbbb222" // somebody else pushed on top
	next, _ = moved.Update(pushResultMsg{err: errors.New("! [rejected] main -> main (non-fast-forward)")})
	if next.(Model).pushForce {
		t.Fatal("a rejection unlocked a force that would delete somebody else's commit")
	}
}

// The lease git is given is the commit we rewrote, not "whatever origin had
// when the screen opened". They differ exactly when it matters: git-assist
// fetches on its own every 30 seconds, and the default lease would quietly
// come to expect the other person's commit.
func TestForcePushLeasesAgainstTheRewrittenCommit(t *testing.T) {
	m := Model{rewriteBaseSHA: "aaaa111", pushLeaseSHA: "bbbb222"}
	if got := m.forceLeaseSHA(); got != "aaaa111" {
		t.Errorf("forceLeaseSHA() = %q, want the rewritten commit", got)
	}
	// No recorded rewrite: fall back to what origin held on entry rather than
	// leaving the lease off entirely.
	m = Model{pushLeaseSHA: "bbbb222"}
	if got := m.forceLeaseSHA(); got != "bbbb222" {
		t.Errorf("forceLeaseSHA() = %q, want the entry reading", got)
	}
}

// Withholding the force is only honest if the screen says why.
func TestPushScreenExplainsAWithheldForce(t *testing.T) {
	m := wizardModel(t, stepPush)
	m.hasRemote = true
	m.pushHasUpstream = true
	m.historyRewritten = true
	m.pushAhead, m.pushBehind = 1, 2
	m.pushOutgoingTotal = 1
	m.rewriteBaseSHA = "aaaa111"
	m.pushLeaseSHA = "bbbb222"
	m.pushForce = m.forcePushRequired()

	if m.pushForce {
		t.Fatal("force offered although origin moved past the rewritten commit")
	}
	out := m.viewPush()
	if !strings.Contains(out, "Someone pushed on top of the commit you rewrote") {
		t.Errorf("the screen does not say why the force is missing:\n%s", out)
	}
	if strings.Contains(out, "f force-push") {
		t.Errorf("the help bar still offers the force:\n%s", out)
	}
}

// The sync dialog used to print the raw command for the diverged case, for the
// same reason Done did: there was no in-app path to point at.
func TestSyncDialogPointsAtTheInAppForcePush(t *testing.T) {
	m := wizardModel(t, stepSync)
	m.syncDiverged = true
	m.syncPullCurrent = true
	m.syncAhead, m.syncCurrTotal = 1, 1
	out := m.viewSync()
	if strings.Contains(out, "git push --force-with-lease") {
		t.Errorf("the dialog still prints a command to run elsewhere:\n%s", out)
	}
	if !strings.Contains(out, "Push from the menu") {
		t.Errorf("the dialog does not point at the in-app path:\n%s", out)
	}
}

// ── Discarding one file's changes (x) ──────────────────

func entry(path string, status types.FileStatus) types.FileEntry {
	return types.FileEntry{Path: path, Status: status}
}

// The prompt in front of an unrecoverable operation has to say what it costs,
// and the four statuses cost four different things. The deleted case is the
// sharp one: x there RESTORES, and wording it as a delete would be a lie
// pointed at the one file the key can bring back.
func TestDiscardPromptWordsMatchTheStatus(t *testing.T) {
	cases := []struct {
		name     string
		entry    types.FileEntry
		keyLabel string
		want     []string
		wantNot  []string
	}{
		{
			name:     "modified",
			entry:    entry("app.go", types.StatusModified),
			keyLabel: "discard",
			want:     []string{"Discard changes to app.go?", "permanently lost"},
		},
		{
			name:     "untracked",
			entry:    entry("notes.txt", types.StatusUntracked),
			keyLabel: "delete",
			want:     []string{"Delete notes.txt?", "never committed", "cannot be undone"},
		},
		{
			name:     "untracked directory",
			entry:    entry("vendor/", types.StatusUntracked),
			keyLabel: "delete",
			want:     []string{"Delete vendor/ and everything in it?"},
		},
		{
			name:     "staged but never committed",
			entry:    entry("new.go", types.StatusAdded),
			keyLabel: "delete",
			want:     []string{"Delete new.go?", "never committed"},
		},
		{
			name:     "deleted",
			entry:    entry("gone.go", types.StatusDeleted),
			keyLabel: "restore",
			want:     []string{"Restore gone.go?", "Brings the deleted file back"},
			// Nothing on this screen may read as "delete it harder".
			wantNot: []string{"Delete gone.go", "permanently lost", "cannot be undone"},
		},
		{
			name:     "renamed",
			entry:    types.FileEntry{Path: "new.go", OrigPath: "old.go", Status: types.StatusRenamed},
			keyLabel: "undo rename",
			want:     []string{"Undo the rename of old.go", "goes back to old.go"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := wizardModel(t, stepFiles, tc.entry)
			m, _ = key(t, m, "x")
			if !m.confirmDiscard {
				t.Fatal("x did not open the confirmation")
			}
			out := m.viewFiles()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("prompt does not say %q:\n%s", want, out)
				}
			}
			for _, unwanted := range tc.wantNot {
				if strings.Contains(out, unwanted) {
					t.Errorf("prompt says %q, which describes the wrong operation:\n%s", unwanted, out)
				}
			}
			// The footer's own label for x has to agree with the prompt.
			if got := m.discardKeyLabel(); got != tc.keyLabel {
				t.Errorf("footer labels x %q, want %q", got, tc.keyLabel)
			}
		})
	}
}

// x on an empty list must not index it, and the prompt must not fire twice.
func TestDiscardIsSingleShotAndReportsWhatHappened(t *testing.T) {
	m := wizardModel(t, stepFiles)
	m, _ = key(t, m, "x")
	if m.confirmDiscard {
		t.Error("x opened a confirmation with no file under the cursor")
	}

	m = wizardModel(t, stepFiles, entry("a.txt", types.StatusModified), entry("b.txt", types.StatusUntracked))
	m, _ = key(t, m, "x")
	m, cmd := key(t, m, "y")
	if cmd == nil || !m.discarding {
		t.Fatal("y did not dispatch the discard")
	}
	// Input is blocked while it runs — a second y must not queue another one.
	before := m
	m, cmd = key(t, m, "y")
	if cmd != nil || m.discarding != before.discarding {
		t.Error("a second y dispatched a second discard")
	}

	next, _ := m.Update(discardResultMsg{
		entry: entry("a.txt", types.StatusModified),
		files: []types.FileEntry{entry("b.txt", types.StatusUntracked)},
	})
	m = next.(Model)
	if m.discarding || m.confirmDiscard {
		t.Error("the result did not close the prompt")
	}
	if len(m.files) != 1 || m.files[0].Path != "b.txt" {
		t.Errorf("the file list was not refreshed: %+v", m.files)
	}
	if m.statusNote != "Discarded changes to a.txt" {
		t.Errorf("status note = %q", m.statusNote)
	}
}

// Cancelling is any key that is not y — n and esc among them.
func TestDiscardCancels(t *testing.T) {
	for _, k := range []string{"n", "esc"} {
		m := wizardModel(t, stepFiles, entry("a.txt", types.StatusModified))
		m, _ = key(t, m, "x")
		m, cmd := key(t, m, k)
		if m.confirmDiscard || m.discarding || cmd != nil {
			t.Errorf("%q did not cancel the discard", k)
		}
	}
}

// A discard that lands on the last file leaves the cursor past the end of the
// refreshed list — the next frame reads files[cursor] for the status badge and
// for the footer's own x label.
func TestDiscardClampsTheCursor(t *testing.T) {
	m := wizardModel(t, stepFiles, entry("a.txt", types.StatusModified), entry("b.txt", types.StatusModified))
	m.cursor = 1
	next, _ := m.Update(discardResultMsg{
		entry: entry("b.txt", types.StatusModified),
		files: []types.FileEntry{entry("a.txt", types.StatusModified)},
	})
	m = next.(Model)
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	m.viewFiles() // must not panic
}

// ── Revert: the pushed-aware second exit from undo ─────

func TestUndoPromptOffersRevertOnlyForPushedCommits(t *testing.T) {
	t.Chdir(t.TempDir()) // not a repo: nothing forked from the prompt answers

	// Unpushed: one path, and the extra keys are neither offered nor live.
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.confirmUndo = true
	m.undoSubject = "feat: thing"
	out := m.viewFiles()
	if strings.Contains(out, "revert instead") {
		t.Errorf("revert is offered for an unpushed commit:\n%s", out)
	}
	m2, cmd := key(t, m, "r")
	if cmd != nil || m2.reverting {
		t.Error("r started a revert on an unpushed commit")
	}
	if m2.confirmUndo {
		t.Error("r should fall through to 'any key cancels' when revert is not on offer")
	}

	// Pushed: both exits, spelled out and both live.
	m.undoPushed = true
	m.undoSHA = "deadbeef"
	out = m.viewFiles()
	for _, want := range []string{
		"undo anyway", "rewrites history", "force-push",
		"revert instead", "adds a new commit that undoes it", "safe for pushed work",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the pushed prompt never says %q:\n%s", want, out)
		}
	}
	if mr, cmd := key(t, m, "r"); cmd == nil || !mr.reverting || mr.confirmUndo {
		t.Errorf("r did not start the revert (reverting=%v, cmd=%v)", mr.reverting, cmd != nil)
	}
	if mu, cmd := key(t, m, "u"); cmd == nil || !mu.undoing {
		t.Errorf("u did not start the undo (undoing=%v, cmd=%v)", mu.undoing, cmd != nil)
	}
	// y is NOT a third exit here. The footer says "any cancel", and the whole
	// point of this prompt is that a pushed commit has two different answers —
	// the app-wide "y confirms" reflex used to pick the history-rewriting one
	// without the user ever engaging with the choice.
	my, cmd := key(t, m, "y")
	if cmd != nil || my.undoing || my.reverting {
		t.Error("y ran the undo on a pushed commit instead of cancelling")
	}
	if my.confirmUndo {
		t.Error("y left the prompt up — it must cancel like any other key")
	}
	// On an UNPUSHED commit it is still the confirmation it always was.
	plain := wizardModel(t, stepFiles, file("a.txt"))
	plain.confirmUndo = true
	if mp, cmd := key(t, plain, "y"); cmd == nil || !mp.undoing {
		t.Error("y no longer confirms the undo of an unpushed commit")
	}
}

// A revert ADDS a commit. Marking the session as history-rewritten there would
// offer a force-push for a branch that never diverged.
func TestRevertReportsWithoutMarkingHistoryRewritten(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.reverting = true
	m.confirmUndo = true
	m.undoPushed = true
	m.amendMode = true // a latched amend must not survive a moved HEAD

	next, cmd := m.Update(revertResultMsg{
		subject: "feat: the bad one",
		files:   []types.FileEntry{file("b.txt")},
	})
	m = next.(Model)

	if m.reverting || m.confirmUndo || m.undoPushed {
		t.Error("the result did not close the prompt")
	}
	if m.historyRewritten {
		t.Error("a revert marked the session as having rewritten history")
	}
	if m.amendMode {
		t.Error("amend mode survived the revert — the next confirm would rewrite the revert commit")
	}
	if want := "Reverted feat: the bad one — a new commit undoes it"; m.statusNote != want {
		t.Errorf("status note = %q, want %q", m.statusNote, want)
	}
	if cmd == nil {
		t.Error("the dashboard was not refreshed after HEAD moved")
	}
	if len(m.files) != 1 || m.files[0].Path != "b.txt" {
		t.Errorf("the file list was not refreshed: %+v", m.files)
	}
}

func TestRevertFailureKeepsTheError(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.reverting = true
	next, _ := m.Update(revertResultMsg{err: errors.New("revert conflict — 2 conflicting files")})
	m = next.(Model)
	if m.reverting {
		t.Error("the spinner is still running after the failure")
	}
	if m.err == nil || !strings.Contains(m.err.Error(), "conflicting files") {
		t.Errorf("err = %v, want the conflict reported", m.err)
	}
	if m.statusNote != "" {
		t.Errorf("a failed revert reported success: %q", m.statusNote)
	}
}

// ── Branch rename (r) ──────────────────────────────────

func renameModel(t *testing.T) Model {
	t.Helper()
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepBranch)
	m.branchEntries = []types.BranchEntry{
		{Name: "main", IsCurrent: true},
		{Name: "feature"},
		{Name: "upstream-only", IsRemote: true},
	}
	return m
}

func TestBranchRenameValidatesBeforeTouchingGit(t *testing.T) {
	m := renameModel(t)
	m.branchCursor = 1 // feature

	m, _ = key(t, m, "r")
	if !m.branchRenameMode {
		t.Fatal("r did not open the rename input")
	}
	if got := m.branchRenameInput.Value(); got != "feature" {
		t.Errorf("input prefilled with %q, want the current name", got)
	}
	if m.branchRenameFrom != "feature" {
		t.Errorf("renaming %q, want feature", m.branchRenameFrom)
	}

	// Enter-through on the prefilled name is easy to hit by accident.
	same, cmd := key(t, m, "enter")
	if cmd != nil || same.branchRenaming {
		t.Error("renaming a branch to its own name was dispatched")
	}
	if same.err == nil || !strings.Contains(same.err.Error(), "already this branch's name") {
		t.Errorf("err = %v, want an explanation", same.err)
	}

	// A name git itself rejects.
	bad := m
	bad.branchRenameInput.SetValue("has spaces")
	bad, cmd = key(t, bad, "enter")
	if cmd != nil || bad.branchRenaming {
		t.Error("an invalid branch name was dispatched to git")
	}
	if bad.err == nil || !strings.Contains(bad.err.Error(), "can't contain spaces") {
		t.Errorf("err = %v, want the name rules", bad.err)
	}

	// A name that is already taken (main exists in the fixture repo).
	taken := m
	taken.branchRenameInput.SetValue("main")
	taken, cmd = key(t, taken, "enter")
	if cmd != nil || taken.branchRenaming {
		t.Error("renaming onto an existing branch was dispatched")
	}
	if taken.err == nil || !strings.Contains(taken.err.Error(), "already exists") {
		t.Errorf("err = %v, want the collision named", taken.err)
	}

	// A good name goes through, once.
	ok := m
	ok.branchRenameInput.SetValue("feature-2")
	ok, cmd = key(t, ok, "enter")
	if cmd == nil || !ok.branchRenaming {
		t.Fatal("a valid rename was not dispatched")
	}
	blocked, cmd2 := key(t, ok, "enter")
	if cmd2 != nil || !blocked.branchRenameMode {
		t.Error("a second enter dispatched a second rename")
	}
}

func TestBranchRenameRefusesRemoteOnlyBranches(t *testing.T) {
	m := renameModel(t)
	m.branchCursor = 2 // remote-only
	m, _ = key(t, m, "r")
	if m.branchRenameMode {
		t.Error("a remote-only branch opened the rename input")
	}
	if m.err == nil || !strings.Contains(m.err.Error(), "only exists on origin") {
		t.Errorf("err = %v, want an explanation", m.err)
	}
}

func TestBranchRenameOfTheCurrentBranchFollowsThroughAndDisclosesOrigin(t *testing.T) {
	m := renameModel(t)
	m.branchRenaming = true
	m.branchRenameMode = true
	m.branchRenameFrom = "main"

	next, cmd := m.Update(branchRenameResultMsg{
		from: "main", to: "trunk", wasCurrent: true, hadUpstream: true,
	})
	m = next.(Model)

	if m.branch != "trunk" {
		t.Errorf("m.branch = %q, want the new name", m.branch)
	}
	if m.branchRenameMode || m.branchRenaming {
		t.Error("the rename prompt is still open")
	}
	for _, want := range []string{"Renamed main to trunk", "origin still has main"} {
		if !strings.Contains(m.statusNote, want) {
			t.Errorf("note %q does not say %q", m.statusNote, want)
		}
	}
	if cmd == nil {
		t.Error("the dashboard was not refreshed after the branch changed name")
	}

	// No upstream: nothing to disclose, and the note must not invent one.
	m2 := renameModel(t)
	m2.branchRenaming = true
	next, _ = m2.Update(branchRenameResultMsg{from: "feature", to: "feature-2"})
	m2 = next.(Model)
	if m2.statusNote != "Renamed feature to feature-2" {
		t.Errorf("note = %q", m2.statusNote)
	}
	if m2.branch == "feature-2" {
		t.Error("renaming another branch moved the current-branch label")
	}
}

func TestBranchRenameFailureStaysInTheInput(t *testing.T) {
	m := renameModel(t)
	m.branchRenameMode = true
	m.branchRenaming = true
	m.branchRenameFrom = "feature"
	next, _ := m.Update(branchRenameResultMsg{err: errors.New("boom"), from: "feature", to: "x"})
	m = next.(Model)
	if !m.branchRenameMode {
		t.Error("a failed rename closed the input, losing the typed name")
	}
	if m.err == nil {
		t.Error("the failure was swallowed")
	}
	if out := m.viewBranch(); !strings.Contains(out, "boom") {
		t.Errorf("the rename screen does not render its error:\n%s", out)
	}
}

func TestBranchRenameScreenDisclosesThatOriginKeepsTheOldName(t *testing.T) {
	m := renameModel(t)
	m.branchRenameMode = true
	m.branchRenameFrom = "feature"
	m.branchRenameUpstream = true
	out := m.viewBranch()
	for _, want := range []string{"Rename", "feature", "origin still has feature", "local only"} {
		if !strings.Contains(out, want) {
			t.Errorf("the rename screen never says %q:\n%s", want, out)
		}
	}
}

// ── The ? overlay ──────────────────────────────────────

// helpScreens enumerates one model per screen the overlay can appear on, so the
// parity check below covers all of them rather than the two that are easy.
func helpScreens(t *testing.T) []struct {
	name string
	m    Model
} {
	t.Helper()
	mk := func(s step, f func(m *Model)) Model {
		m := wizardModel(t, s, file("a.txt"))
		m.width = 200
		m.height = 60
		if f != nil {
			f(&m)
		}
		return m
	}
	return []struct {
		name string
		m    Model
	}{
		{"menu", mk(stepMenu, nil)},
		{"menu with remote work", mk(stepMenu, func(m *Model) {
			m.hasRemote, m.behindOrigin, m.behindMain, m.mainRef = true, 2, 1, "origin/main"
		})},
		// S joins the footer only when there is a stack to open, so both
		// states need covering — on the dashboard and on the branch manager,
		// which is where a failed auto-stash pop leaves its banner.
		{"menu with a stash", mk(stepMenu, func(m *Model) { m.stashCount = 2 })},
		{"branch with a stash", mk(stepBranch, func(m *Model) {
			m.branchEntries = []types.BranchEntry{{Name: "main", IsCurrent: true}}
			m.stashCount = 1
		})},
		{"files", mk(stepFiles, nil)},
		{"files deleted entry", mk(stepFiles, func(m *Model) {
			m.files = []types.FileEntry{entry("gone.go", types.StatusDeleted)}
		})},
		{"files gitignore", mk(stepFiles, func(m *Model) {
			m.gitignoreMode = true
			m.removeIgnored = map[string]bool{}
		})},
		{"files undo prompt", mk(stepFiles, func(m *Model) { m.confirmUndo = true })},
		{"files undo prompt pushed", mk(stepFiles, func(m *Model) {
			m.confirmUndo, m.undoPushed = true, true
		})},
		{"files discard prompt", mk(stepFiles, func(m *Model) {
			m.confirmDiscard = true
			m.discardEntry = entry("a.txt", types.StatusModified)
		})},
		{"files conflicted commit gate", mk(stepFiles, func(m *Model) {
			m.files = []types.FileEntry{entry("marked.txt", types.StatusConflicted)}
			m.files[0].Selected = true
			m.confirmConflictedCommit = true
		})},
		{"diff", mk(stepFiles, func(m *Model) {
			m.showDiff, m.diffContent, m.diffFile = true, "@@ -1 +1 @@\n-a\n+b\n", "a.txt"
		})},
		{"diff binary", mk(stepFiles, func(m *Model) {
			m.showDiff, m.diffFile = true, "a.bin"
		})},
		{"branch", mk(stepBranch, func(m *Model) {
			m.branchEntries = []types.BranchEntry{{Name: "main", IsCurrent: true}}
		})},
		{"branch standalone", mk(stepBranch, func(m *Model) {
			m.branchStandalone = true
			m.branchEntries = []types.BranchEntry{{Name: "main", IsCurrent: true}}
		})},
		{"branch delete", mk(stepBranch, func(m *Model) {
			m.branchEntries = []types.BranchEntry{{Name: "main", IsCurrent: true}}
			m.branchDeleteMode = true
		})},
		{"branch force delete", mk(stepBranch, func(m *Model) {
			m.branchForceDeleteMode, m.branchForceDeleteName = true, "feature"
		})},
		{"branch merge target", mk(stepBranch, func(m *Model) {
			m.mergeTargetMode, m.mergeSource = true, "feature"
		})},
		{"branch merge confirm", mk(stepBranch, func(m *Model) {
			m.branchMergeMode, m.mergeSource, m.mergeTarget = true, "feature", "main"
		})},
		{"config", mk(stepConfig, func(m *Model) { m.loadConfigItems() })},
		{"config pick", mk(stepConfig, func(m *Model) {
			m.loadConfigItems()
			m.configCursor = 2
			m.configPickMode = true
			m.configPickItems = []string{"main"}
		})},
		{"config remove remote", mk(stepConfig, func(m *Model) {
			m.loadConfigItems()
			m.configRemoveRemote = true
		})},
		{"type", mk(stepType, nil)},
		// The wizard's own text screens. filesHelp branches on editMode and
		// filterMode exactly as viewFiles does, so both modes are screens in
		// their own right and the parity check has to reach them.
		{"message", mk(stepMessage, nil)},
		{"files edit", mk(stepFiles, func(m *Model) {
			m.editMode = true
			m.editArea.SetValue("line one\nline two\n")
		})},
		{"files edit dirty", mk(stepFiles, func(m *Model) {
			m.editMode, m.editDirty = true, true
			m.editArea.SetValue("edited\n")
		})},
		{"files edit exit prompt", mk(stepFiles, func(m *Model) {
			m.editMode, m.editDirty, m.confirmExit = true, true, true
		})},
		{"files filter", mk(stepFiles, func(m *Model) {
			m.filterMode = true
			m.filterInput.SetValue("a")
			m.filterMatches = []int{0}
		})},
		{"confirm", mk(stepConfirm, func(m *Model) { m.files[0].Selected = true })},
		{"push", mk(stepPush, func(m *Model) {
			m.pushHasUpstream, m.pushOutgoingTotal = true, 2
		})},
		{"push force", mk(stepPush, func(m *Model) { m.pushForce = true })},
		{"push from menu", mk(stepPush, func(m *Model) { m.pushReturnToMenu = true })},
		{"done", mk(stepDone, func(m *Model) { m.hasRemote = true })},
		{"sync", mk(stepSync, func(m *Model) {
			m.syncPullCurrent, m.syncSyncMain, m.syncMainBranchName = true, true, "main"
		})},
		// The pull is withheld here, so the footer has to lose its p.
		{"sync rewrite hold", mk(stepSync, func(m *Model) {
			m.syncRewriteHold, m.syncMainBranchName = true, "main"
			m.syncCurrTotal = 1
		})},
		// A recovery banner puts S on whatever screen it lands on.
		{"push with a recovery banner", mk(stepPush, func(m *Model) {
			m.pushReturnToMenu, m.stashCount = true, 1
			m.err = recoveryError{fmt.Errorf("pull failed. %s", stashRecoveryHint("abc1234"))}
		})},
		{"files with a recovery banner", mk(stepFiles, func(m *Model) {
			m.stashCount = 1
			m.err = recoveryError{fmt.Errorf("switch failed. %s", stashRecoveryHint("abc1234"))}
		})},
		{"init", mk(stepInit, func(m *Model) { m.initPhase = initPhasePickOption })},
		{"init template", mk(stepInit, func(m *Model) { m.initPhase = initPhasePickTemplate })},
		{"init visibility", mk(stepInit, func(m *Model) { m.initPhase = initPhasePickVisibility })},
		{"init gh auth", mk(stepInit, func(m *Model) { m.initPhase = initPhaseConfirmGHAuth })},
		{"stash list", mk(stepStash, func(m *Model) { *m = stashModel(t, 2) })},
		{"stash empty", mk(stepStash, func(m *Model) { *m = stashModel(t, 0) })},
		{"stash preview", mk(stepStash, func(m *Model) {
			*m = stashModel(t, 1)
			m.stashShowDiff, m.stashDiff = true, "@@ -1 +1 @@\n-a\n+b\n"
		})},
		{"stash delete", mk(stepStash, func(m *Model) {
			*m = stashModel(t, 1)
			m.stashConfirmDrop = true
		})},
		// Mid-merge the mutating keys are locked and the footer says so.
		{"stash mid-merge", mk(stepStash, func(m *Model) {
			*m = stashModel(t, 2)
			m.mergeInProgress = true
		})},
		// The history browser's four states. Its reads are async, so "still
		// loading" is a screen the user sees and has to be able to leave.
		{"history list", mk(stepHistory, func(m *Model) { *m = historyModel(t, 3) })},
		{"history loading", mk(stepHistory, func(m *Model) {
			*m = historyModel(t, 0)
			m.historyLoading = true
		})},
		{"history detail", mk(stepHistory, func(m *Model) {
			*m = historyModel(t, 3)
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			m.historyDetail = git.CommitDetail{SHA: "abc1234", Subject: "feat: x", Author: "Yahya"}
		})},
		{"history detail loading", mk(stepHistory, func(m *Model) {
			*m = historyModel(t, 3)
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			m.historyDetailLoading = true
		})},
		{"history patch", mk(stepHistory, func(m *Model) {
			*m = historyModel(t, 3)
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			m.historyPatchLoaded, m.historyShowPatch = true, true
			m.historyPatch = git.CommitPatchInfo{Patch: "@@ -1 +1 @@\n-a\n+b\n"}
		})},
		// The conflict resolver. Its per-file keys change LABEL with the row
		// under the cursor (a modify/delete conflict's `t` deletes the file)
		// and disappear on a resolved one, so every one of those states is a
		// separate screen as far as the footer is concerned.
		{"conflicts", mk(stepConflicts, func(m *Model) { *m = conflictScreenModel(t, false) })},
		{"conflicts deleted variant", mk(stepConflicts, func(m *Model) {
			c := conflictScreenModel(t, false)
			c.conflictCursor = 1 // the modify/delete row
			*m = c
		})},
		{"conflicts all resolved", mk(stepConflicts, func(m *Model) { *m = conflictScreenModel(t, true) })},
		{"conflicts marker warning", mk(stepConflicts, func(m *Model) {
			c := conflictScreenModel(t, false)
			c.conflictMarkWarn, c.conflictMarkPath = true, "shared.txt"
			*m = c
		})},
		{"conflicts abort confirmation", mk(stepConflicts, func(m *Model) {
			c := conflictScreenModel(t, false)
			c.conflictConfirmAbort = true
			*m = c
		})},
		{"conflicts abort confirmation with a parked stash", mk(stepConflicts, func(m *Model) {
			c := conflictScreenModel(t, true)
			c.conflictConfirmAbort = true
			c.conflictStashed, c.conflictStashRef = true, "abc1234"
			*m = c
		})},
		{"conflicts editor", mk(stepConflicts, func(m *Model) {
			c := conflictScreenModel(t, false)
			c.editMode, c.conflictEditPath = true, "shared.txt"
			c.editArea.SetValue("<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> feat\n")
			*m = c
		})},
		{"conflicts editor unsaved", mk(stepConflicts, func(m *Model) {
			c := conflictScreenModel(t, false)
			c.editMode, c.conflictEditPath = true, "shared.txt"
			c.editDirty, c.confirmExit = true, true
			*m = c
		})},
		{"conflicts recovered at startup", mk(stepConflicts, func(m *Model) {
			c := conflictScreenModel(t, false)
			c.conflictOrigin, c.conflictSource = conflictFromRecovered, ""
			*m = c
		})},
		// Both panes drop their scroll keys when there is nothing to scroll,
		// so both states need covering.
		{"history detail scrollable", mk(stepHistory, func(m *Model) {
			*m = historyModel(t, 3)
			m.height = 24
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			m.historyDetail = git.CommitDetail{
				SHA: "abc1234", Subject: "feat: x",
				Body: strings.TrimRight(strings.Repeat("body line\n", 60), "\n"),
			}
		})},
		{"history patch scrollable", mk(stepHistory, func(m *Model) {
			*m = historyModel(t, 3)
			m.height = 24
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			m.historyPatchLoaded, m.historyShowPatch = true, true
			m.historyPatch = git.CommitPatchInfo{
				Patch: strings.TrimRight(strings.Repeat("+line\n", 200), "\n"),
			}
		})},
	}
}

// The overlay and the footer are the same list, on every screen. A second
// hand-maintained key list is the thing this refactor exists to prevent, so the
// test asserts the footer the screen draws IS the rows helpRows returns, and
// that the overlay lists every one of them.
func TestHelpOverlayMirrorsTheFooterOnEveryScreen(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	for _, tc := range helpScreens(t) {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.m
			rows := m.helpRows()
			if len(rows) == 0 {
				t.Fatalf("%s has no keys at all", tc.name)
			}
			// Row by row: the box pads every line to its own width, so the
			// joined footer is not contiguous in the rendered output — each
			// row is.
			view := m.View()
			for i, row := range rows {
				if bar := renderHelp(row); !strings.Contains(view, bar) {
					t.Errorf("footer row %d on screen is not the one helpRows returns:\nwant %q\nin:\n%s", i, bar, view)
				}
			}

			m.showHelp = true
			overlay := m.View()
			if !strings.Contains(overlay, m.screenName()) {
				t.Errorf("the overlay does not name the screen (%s)", m.screenName())
			}
			for _, e := range m.helpEntries() {
				if !strings.Contains(overlay, helpKeyStyle.Render(padKey(e.key))) {
					t.Errorf("the overlay omits the %q key", e.key)
				}
				if !strings.Contains(overlay, helpStyle.Render(e.desc)) {
					t.Errorf("the overlay omits %q (the %q key)", e.desc, e.key)
				}
			}
			if !strings.Contains(overlay, "close") {
				t.Errorf("the overlay does not say how to close itself:\n%s", overlay)
			}
		})
	}
}

func TestHelpOverlayTogglesAndSwallowsKeys(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)
	m.statusNote = "Merged feature into main"
	m.menuCursor = 0

	m, _ = key(t, m, "?")
	if !m.showHelp {
		t.Fatal("? did not open the overlay")
	}
	// It is a read-only side channel: the note underneath survives it.
	if m.statusNote == "" {
		t.Error("opening the overlay cleared the status note")
	}
	// No key of the screen underneath fires while it is up.
	m, _ = key(t, m, "down")
	if m.menuCursor != 0 {
		t.Error("a key reached the menu through the overlay")
	}
	m, _ = key(t, m, "?")
	if m.showHelp {
		t.Error("? did not close the overlay")
	}
	m, _ = key(t, m, "?")
	m, _ = key(t, m, "esc")
	if m.showHelp {
		t.Error("esc did not close the overlay")
	}
}

// On a screen with a focused text field, `?` is a character. Eating it would
// make the key unavailable in branch names and commit subjects.
func TestHelpOverlayStaysOutOfTextFields(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	cases := []struct {
		name string
		m    Model
	}{
		{"commit subject", wizardModel(t, stepMessage, file("a.txt"))},
		{"custom type", wizardModel(t, stepCustom, file("a.txt"))},
		{"file filter", func() Model {
			m := wizardModel(t, stepFiles, file("a.txt"))
			m.filterMode = true
			m.filterInput.Focus()
			return m
		}()},
		{"branch create", func() Model {
			m := wizardModel(t, stepBranch)
			m.branchCreateMode = true
			m.branchCreateInput.Focus()
			return m
		}()},
		{"branch rename", func() Model {
			m := wizardModel(t, stepBranch)
			m.branchRenameMode = true
			m.branchRenameInput.Focus()
			return m
		}()},
		{"config edit", func() Model {
			m := wizardModel(t, stepConfig)
			m.loadConfigItems()
			m.configEditMode = true
			m.configEditInput.Focus()
			return m
		}()},
		{"init url", func() Model {
			m := wizardModel(t, stepInit)
			m.initPhase = initPhaseInputURL
			m.initURLInput.Focus()
			return m
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := key(t, tc.m, "?")
			if m.showHelp {
				t.Error("? opened the overlay instead of typing a question mark")
			}
		})
	}
}

// Covering a spinner with a key list hides the one thing the user is waiting on.
func TestHelpOverlayStaysClosedDuringAnOperation(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.undoing = true
	m, _ = key(t, m, "?")
	if m.showHelp {
		t.Error("? opened the overlay over an in-flight operation")
	}
}

// ── Detached HEAD ──────────────────────────────────────

func TestDetachedHeadMenuOffersOnlyTheWayOut(t *testing.T) {
	m := wizardModel(t, stepMenu, file("a.txt"))
	m.hasRemote = true
	m.hasAnyCommit = true
	m.aheadOrigin = 3
	m.behindMain = 2
	m.mainRef = "origin/main"
	m.behindOrigin = 1
	m.branch = git.DetachedLabel
	m.detached = true

	names := []string{}
	for _, item := range m.menuItems() {
		names = append(names, item.name)
	}
	// Branch is the way out; History reads HEAD and writes nothing, so it stays
	// (see TestDetachedMenuKeepsTheHistoryBrowser). Everything that assumes a
	// branch — Commit, Amend, Push, Sync — is gone.
	if strings.Join(names, ",") != "Branch,History,Config" {
		t.Fatalf("menu = %v, want just Branch, History and Config", names)
	}
	if desc := m.menuItems()[0].desc; !strings.Contains(desc, "switch to a branch") {
		t.Errorf("the Branch entry does not point at the way out: %q", desc)
	}
	if m.canPush() {
		t.Errorf("Push is offered while detached — it would try to publish a branch called %q", m.branch)
	}
	if m.canSyncMain() {
		t.Error("Sync is offered while detached")
	}

	out := m.viewMenu()
	for _, want := range []string{"detached HEAD", "not on any branch", "can be lost", "switch to a branch"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dashboard never says %q:\n%s", want, out)
		}
	}
	// The keys that assume a branch are gone from the footer too.
	if strings.Contains(renderHelpRows(m.helpRows()), "sync") {
		t.Error("the sync shortcut is still advertised while detached")
	}
	if strings.Contains(renderHelpRows(m.helpRows()), "pull") {
		t.Error("the pull shortcut is still advertised while detached")
	}

	// And "p" does nothing rather than opening a dialog about a branch we are
	// not on.
	m2, cmd := key(t, m, "p")
	if cmd != nil || m2.step != stepMenu {
		t.Error("p opened the sync dialog while detached")
	}
	// Enter on Branch still works: that is the cure.
	m.menuCursor = 0
	m3, _ := key(t, m, "enter")
	if m3.step != stepBranch {
		t.Errorf("enter on Branch went to step %v, want the branch manager", m3.step)
	}
}

func TestSwitchingBranchClearsTheDetachedFlag(t *testing.T) {
	m := wizardModel(t, stepBranch)
	m.detached = true
	m.branch = git.DetachedLabel
	next, _ := m.Update(branchSwitchResultMsg{newBranch: "main"})
	m = next.(Model)
	if m.detached {
		t.Error("switching to a branch left the detached flag set")
	}
	if m.branch != "main" {
		t.Errorf("branch = %q, want main", m.branch)
	}
}

func TestDashboardSnapshotCarriesDetachedState(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	gitRun(t, "checkout", "-q", "--detach", "HEAD")
	snap := readDashboard(git.DetachedLabel, false)
	if !snap.detached {
		t.Fatal("the snapshot missed a detached HEAD")
	}
	m := wizardModel(t, stepMenu)
	m.applyDashboard(snap)
	if !m.detached {
		t.Error("applyDashboard did not carry the flag onto the model")
	}
}

// ── Undoing a pushed commit and NOT re-committing ──────
//
// The delete-a-pushed-commit case (a leaked secret is the canonical one). It
// leaves the branch 0 ahead / N behind, which every gate here used to read as
// "you are merely behind": the menu said "force required", the push screen said
// "origin already has everything", `f` was inert, and the only live path — the
// offered pull — fast-forwarded the undone commit straight back.

// runAll executes a dispatched command (batches included) and feeds every
// message it produced back through Update, the way the Bubble Tea loop does.
func runAll(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range drain(t, cmd) {
		if _, tick := msg.(spinner.TickMsg); tick {
			continue
		}
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

// undidAPushedCommit builds a repo whose origin holds a commit the local branch
// has just undone, through the app's own undo prompt. Returns the model and
// origin's path.
func undidAPushedCommit(t *testing.T) (Model, string) {
	t.Helper()
	origin := tempRepoWithOrigin(t, "chore: seed")
	writeFile(t, "secret.txt", "AKIA-oops\n")
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-q", "-m", "feat: leak a secret")
	gitRun(t, "push", "-q", "origin", "main")

	m := wizardModel(t, stepFiles, file("secret.txt"))
	m.hasRemote = true
	m.hasAnyCommit = true
	m, _ = key(t, m, "u")
	if !m.undoPushed {
		t.Fatal("the prompt did not notice the commit is on origin")
	}
	m2, cmd := key(t, m, "u") // "undo anyway"
	if cmd == nil {
		t.Fatal("u did not start the undo")
	}
	out := runAll(t, m2, cmd)
	if out.err != nil {
		t.Fatalf("the undo failed: %v", out.err)
	}
	if !out.historyRewritten {
		t.Fatal("undoing a pushed commit was not recorded as a rewrite")
	}
	return out, origin
}

func originTip(t *testing.T, origin, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "--git-dir="+origin, "rev-parse", branch).Output()
	if err != nil {
		t.Fatalf("origin rev-parse %s: %v", branch, err)
	}
	return strings.TrimSpace(string(out))
}

func TestUndoOfAPushedCommitOffersTheForceThatRemovesIt(t *testing.T) {
	m, origin := undidAPushedCommit(t)
	removed := originTip(t, origin, "main")

	m.enterPush(true)
	if m.pushAhead != 0 || m.pushBehind == 0 {
		t.Fatalf("ahead = %d, behind = %d, want the pure-deletion shape", m.pushAhead, m.pushBehind)
	}
	if !m.forcePushRequired() || !m.pushForce {
		t.Fatal("no force offered for a pushed commit that was undone and not replaced")
	}

	out := m.viewPush()
	for _, want := range []string{"Force push required", "DELETES it from origin", "will be removed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the screen never says %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "already has everything") {
		t.Errorf("the screen still claims origin is up to date:\n%s", out)
	}
	if footer := renderHelpRows(m.helpRows()); !strings.Contains(footer, "force-push") {
		t.Errorf("footer = %q, want f offered", footer)
	}

	// And `f` actually removes it, under the lease that names the commit the
	// user was shown.
	pushed, cmd := key(t, m, "f")
	if cmd == nil {
		t.Fatal("f is still inert on the screen whose hint says to press it")
	}
	after := runAll(t, pushed, cmd)
	if after.err != nil {
		t.Fatalf("the force push failed: %v", after.err)
	}
	local := strings.TrimSpace(string(gitOut(t, "rev-parse", "HEAD")))
	if got := originTip(t, origin, "main"); got != local {
		t.Errorf("origin/main = %s, want the local tip %s", got, local)
	}
	if originTip(t, origin, "main") == removed {
		t.Error("the undone commit is still origin's tip")
	}
	if after.historyRewritten || after.rewriteBaseSHA != "" {
		t.Error("the rewrite pin survived the push that published it")
	}
}

// The sync dialog is the other half: pulling here restores exactly what the
// user removed, and with ahead == 0 there is no divergence for the existing
// warning to fire on.
func TestTheSyncDialogWillNotOfferAPullThatRestoresAnUndoneCommit(t *testing.T) {
	m, _ := undidAPushedCommit(t)
	m.step = stepMenu
	m.hasRemote = true

	if !m.populateSyncDialog() {
		t.Fatal("the dialog had nothing to say about a branch that is behind")
	}
	if m.syncPullCurrent {
		t.Fatal("the pull that resurrects the undone commit is still on offer")
	}
	if !m.syncRewriteHold {
		t.Fatal("the hold was not recorded")
	}

	m.step = stepSync
	out := m.viewSync()
	for _, want := range []string{
		"still has the commit you rewrote",
		"pulling would bring back",
		"force-with-lease",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dialog never says %q:\n%s", want, out)
		}
	}
	footer := renderHelpRows(m.helpRows())
	if strings.Contains(footer, "pull") {
		t.Errorf("footer = %q, want no pull key", footer)
	}
	if !strings.Contains(footer, "quit") {
		t.Errorf("footer = %q, want q listed — it exits the app from here", footer)
	}
	// p is dead on the dialog too, not merely unlisted.
	if after, cmd := key(t, m, "p"); cmd != nil || after.pulling {
		t.Error("p pulled anyway")
	}
	// The dashboard's own p opens this dialog rather than doing nothing.
	menu := m
	menu.step = stepMenu
	menu.behindOrigin = 1
	opened, _ := key(t, menu, "p")
	if opened.step != stepSync {
		t.Errorf("p on the dashboard went to %v, want the dialog that explains the hold", opened.step)
	}
}

// ── The rewrite pin ────────────────────────────────────

// A second rewrite while one is still pending must keep the ORIGINAL base: the
// pin names what ORIGIN holds, and origin did not move because we amended
// again. Overwriting it made the force gate compare two commits that were never
// meant to match, and refuse with "someone pushed on top of the commit you
// rewrote" on a branch nobody else had touched.
func TestASecondRewriteKeepsTheOriginalBase(t *testing.T) {
	m := wizardModel(t, stepConfirm, file("a.txt"))
	m.branch = "main"

	m.rewritePendingSHA = "aaaaaaa" // origin's tip, before the undo
	m.rememberRewrite(m.rewritePendingSHA)

	m.rewritePendingSHA = "bbbbbbb" // the commit BELOW it, amended next
	m.rememberRewrite(m.rewritePendingSHA)

	if m.rewriteBaseSHA != "aaaaaaa" {
		t.Fatalf("rewriteBaseSHA = %q, want the commit origin is holding", m.rewriteBaseSHA)
	}
	if !m.historyRewritten {
		t.Error("the rewrite stopped being recorded")
	}
	// And the force gate still recognises origin's tip.
	m.pushLeaseSHA = "aaaaaaa"
	m.pushAhead, m.pushBehind = 1, 2
	if !m.forcePushRequired() {
		t.Error("the force was withheld from a branch only this session rewrote")
	}
}

// The same rule through the handlers that promote it: undo, then an amend of
// the commit underneath (which IS reported as pushed, because it is an ancestor
// of origin's tip).
func TestTheAmendAfterAnUndoDoesNotMoveThePin(t *testing.T) {
	m := wizardModel(t, stepFiles, file("a.txt"))
	m.branch = "main"
	m.undoPushed = true
	m.undoing = true
	m.rewritePendingSHA = "origin-tip"
	next, _ := m.Update(undoResultMsg{})
	m = next.(Model)

	m.amendMode, m.amendPushed, m.committing = true, true, true
	m.rewritePendingSHA = "the-commit-below"
	next, _ = m.Update(commitResultMsg{hash: "cccdddd"})
	m = next.(Model)

	if m.rewriteBaseSHA != "origin-tip" {
		t.Errorf("rewriteBaseSHA = %q, want origin-tip", m.rewriteBaseSHA)
	}
}

// The pin belongs to a branch. It used to be cleared on every switch and never
// re-derived, so a round trip disarmed the force machinery on a branch that was
// still diverged — and no later amend could re-arm it, because the rewritten
// commit is on no remote any more.
func TestTheRewritePinSurvivesABranchRoundTrip(t *testing.T) {
	m := wizardModel(t, stepBranch)
	m.branch = "feat"
	m.rememberRewrite("origin-tip-of-feat")

	next, _ := m.Update(branchSwitchResultMsg{newBranch: "main"})
	m = next.(Model)
	if m.historyRewritten || m.rewriteBaseSHA != "" {
		t.Fatalf("the rewrite followed us onto main (rewritten=%v base=%q)",
			m.historyRewritten, m.rewriteBaseSHA)
	}

	next, _ = m.Update(branchSwitchResultMsg{newBranch: "feat"})
	m = next.(Model)
	if !m.historyRewritten || m.rewriteBaseSHA != "origin-tip-of-feat" {
		t.Fatalf("coming back left rewritten=%v base=%q", m.historyRewritten, m.rewriteBaseSHA)
	}

	// A dashboard snapshot is the other way the current branch changes.
	m.applyDashboard(dashboardSnapshot{branch: "main"})
	if m.historyRewritten {
		t.Error("a snapshot for another branch kept the flag set")
	}
	m.applyDashboard(dashboardSnapshot{branch: "feat"})
	if !m.historyRewritten || m.rewriteBaseSHA != "origin-tip-of-feat" {
		t.Error("the snapshot for the rewritten branch did not re-derive the pin")
	}
}

// A pull ENDS the rewrite: the pre-rewrite commit is back in the branch, so the
// pin must not survive to arm a force against it.
func TestAPullDropsTheRewritePin(t *testing.T) {
	m := wizardModel(t, stepSync)
	m.branch = "main"
	m.rememberRewrite("origin-tip")
	m.pulling = true

	next, _ := m.Update(pullResultMsg{kind: pullKindCurrent})
	m = next.(Model)
	if m.historyRewritten || m.rewriteBaseSHA != "" {
		t.Errorf("rewritten = %v, base = %q after a pull", m.historyRewritten, m.rewriteBaseSHA)
	}
	m.applyDashboard(dashboardSnapshot{branch: "main"})
	if m.historyRewritten {
		t.Error("the next dashboard snapshot re-derived a rewrite the pull undid")
	}
}

// gitRunIn / writeFileIn are gitRun and writeFile against another working
// directory — the "someone else pushed" half of a remote test needs a second
// clone, and the app's own helpers are all cwd-relative.
func gitRunIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func writeFileIn(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.WriteFile(dir+"/"+path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ── S from wherever the banner lands ───────────────────
//
// Every recovery banner this app raises ends in "press S to open the stash
// manager", and those banners land wherever the failed operation finished — the
// push step and the file selector included, where S was handled by nobody. The
// key is global for exactly the frames the banner is up.
func TestSOpensTheStashManagerFromAnyRecoveryBanner(t *testing.T) {
	for _, s := range []step{stepPush, stepFiles, stepMenu, stepBranch, stepDone} {
		t.Run(fmt.Sprint(s), func(t *testing.T) {
			stashRepo(t, 1)
			m := wizardModel(t, s, file("a.txt"))
			m.stashCount = 1
			m.err = recoveryError{fmt.Errorf(
				"pulled, but restoring your uncommitted changes conflicted — the working tree was reset clean and nothing was lost. %s",
				stashRecoveryHint("abc1234"))}

			if footer := renderHelpRows(m.helpRows()); !strings.Contains(footer, "S") {
				t.Errorf("footer = %q, want S listed while the banner says to press it", footer)
			}
			after, _ := key(t, m, "S")
			if after.step != stepStash {
				t.Fatalf("S went to %v, want the stash manager the banner names", after.step)
			}
			if after.err != nil {
				t.Errorf("the banner followed the user onto the screen it pointed at: %v", after.err)
			}
			if len(after.stashEntries) != 1 {
				t.Errorf("%d entries loaded", len(after.stashEntries))
			}
		})
	}
}

// It is the banner that makes S global, not the screen: with no recovery error
// up, S stays what it always was (a dashboard/branch key) and is not advertised
// anywhere else.
func TestSIsNotAGlobalKeyWithoutTheBanner(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepPush)
	m.stashCount = 1

	if footer := renderHelpRows(m.helpRows()); strings.Contains(footer, "stash") {
		t.Errorf("footer = %q offers the stash manager on an ordinary push screen", footer)
	}
	after, _ := key(t, m, "S")
	if after.step != stepPush {
		t.Errorf("S left the push screen with no banner up (%v)", after.step)
	}
}

// A banner with an empty stack must not advertise a key that would open an
// empty list — same gate as everywhere else S appears.
func TestSIsNotOfferedWithNothingStashed(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepPush)
	m.err = recoveryError{fmt.Errorf("something failed. %s", stashRecoveryHint("abc1234"))}
	m.stashCount = 0

	if footer := renderHelpRows(m.helpRows()); strings.Contains(footer, "stash") {
		t.Errorf("footer = %q, want no S with an empty stack", footer)
	}
	if after, _ := key(t, m, "S"); after.step != stepPush {
		t.Error("S opened an empty stash manager")
	}
}

// ── The dashboard's scroll window ──────────────────────

// v1.3 grew the menu to nine entries and it was the only list in the app with
// no window: styledBox cut the overflow, `down` walked the cursor onto rows
// nobody could see, and enter opened them blind. The error banner sat below the
// list, so it was the first thing to go — on the exact screen where it was the
// only feedback.
func fullMenuModel(t *testing.T, height int) Model {
	t.Helper()
	m := wizardModel(t, stepMenu, file("a.txt"))
	m.width = 100
	m.height = height
	m.hasRemote = true
	m.hasAnyCommit = true
	m.hasUpstream = true
	m.aheadOrigin = 2
	m.stashCount = 3
	m.historyTotal = 12
	m.mergeInProgress = true // the "Resolve conflicts" entry, first in the list
	m.branchCount = 4
	return m
}

func TestMenuWindowsItsListOnAShortTerminal(t *testing.T) {
	for _, h := range []int{10, 12, 14, 18, 24} {
		m := fullMenuModel(t, h)
		items := m.menuItems()
		if len(items) < 8 {
			t.Fatalf("fixture: %d entries, want the full list", len(items))
		}
		for cursor := range items {
			m.menuCursor = cursor
			m.followMenuCursor(len(items))
			view := m.viewMenu()
			if !strings.Contains(view, items[cursor].name) {
				t.Errorf("height %d: the cursor is on %q and its row is not drawn:\n%s",
					h, items[cursor].name, view)
			}
			if !strings.Contains(view, symCursor) {
				t.Errorf("height %d: no cursor is visible at all:\n%s", h, view)
			}
			if lines := strings.Split(view, "\n"); len(lines) > h {
				t.Errorf("height %d: the box is %d rows tall", h, len(lines))
			}
		}
	}
}

func TestMenuSaysHowManyEntriesAreOffScreen(t *testing.T) {
	m := fullMenuModel(t, 12)
	items := m.menuItems()
	m.menuCursor = len(items) - 1
	m.followMenuCursor(len(items))

	view := m.viewMenu()
	if !strings.Contains(view, "more") {
		t.Errorf("a clipped menu does not say how much is above it:\n%s", view)
	}
	if !strings.Contains(view, symArrowUp) {
		t.Errorf("no scroll-up marker on a windowed list:\n%s", view)
	}

	m.menuCursor = 0
	m.followMenuCursor(len(items))
	if view := m.viewMenu(); !strings.Contains(view, symArrowDown) {
		t.Errorf("no scroll-down marker with entries below:\n%s", view)
	}
}

// Feedback is reserved before the list is sized: an error on a short terminal
// used to be the first thing dropped.
func TestMenuKeepsItsErrorBannerOnAShortTerminal(t *testing.T) {
	for _, h := range []int{10, 12, 14} {
		m := fullMenuModel(t, h)
		m.err = fmt.Errorf("nothing to commit — working tree clean")
		if view := m.viewMenu(); !strings.Contains(view, "nothing to commit") {
			t.Errorf("height %d: the error banner was clipped away:\n%s", h, view)
		}
	}
}

// A tall terminal is unchanged: the whole list, no markers, and the graph still
// gets what is left.
func TestMenuDoesNotWindowWhenEverythingFits(t *testing.T) {
	m := fullMenuModel(t, 40)
	view := m.viewMenu()
	for _, item := range m.menuItems() {
		if !strings.Contains(view, item.name) {
			t.Errorf("%q is missing from a 40-row terminal:\n%s", item.name, view)
		}
	}
	if strings.Contains(view, "more") {
		t.Errorf("a list that fits was windowed anyway:\n%s", view)
	}
}

// ── Branch delete moved to x ───────────────────────────
//
// v1.3's two new list screens teach enter/d = LOOK (stash preview, commit
// details). The branch manager renders an identical cursor list, and `d` there
// opened a delete confirmation — one habitual `y` from destroying a branch.
// `x` is what destroys things everywhere else in this app.
func TestBranchDeleteIsOnXAndDIsInert(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepBranch)
	m.branchEntries = []types.BranchEntry{
		{Name: "main", IsCurrent: true},
		{Name: "feature"},
	}
	m.branchCursor = 1

	after, cmd := key(t, m, "d")
	if cmd != nil || after.branchDeleteMode {
		t.Error("d still opens the delete confirmation")
	}
	if after.err != nil {
		t.Errorf("d raised an error banner instead of doing nothing: %v", after.err)
	}

	deleting, _ := key(t, m, "x")
	if !deleting.branchDeleteMode {
		t.Fatal("x did not open the delete confirmation")
	}
	// Still confirmed, and still y.
	confirmed, cmd := key(t, deleting, "y")
	if cmd == nil || !confirmed.branchDeleting {
		t.Error("y did not go through with the delete")
	}

	footer := renderHelpRows(m.helpRows())
	if !strings.Contains(footer, "x") || !strings.Contains(footer, "delete") {
		t.Errorf("footer = %q, want x labelled delete", footer)
	}
	if strings.Contains(footer, "d delete") {
		t.Errorf("footer = %q still advertises d", footer)
	}
	if !strings.Contains(footer, "quit") {
		t.Errorf("footer = %q, want q listed — it exits the app from here", footer)
	}
}

// The protections that lived on d have to live on x now.
func TestBranchDeleteRefusalsFollowedTheKey(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	cases := []struct {
		name  string
		entry types.BranchEntry
		want  string
	}{
		{"current", types.BranchEntry{Name: "feature", IsCurrent: true}, "current branch"},
		{"remote", types.BranchEntry{Name: "feature", IsRemote: true}, "remote branch"},
		{"main", types.BranchEntry{Name: "main"}, "protected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := wizardModel(t, stepBranch)
			m.branchEntries = []types.BranchEntry{tc.entry}
			after, _ := key(t, m, "x")
			if after.branchDeleteMode {
				t.Fatal("the confirmation opened on a branch that cannot be deleted")
			}
			if after.err == nil || !strings.Contains(after.err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", after.err, tc.want)
			}
		})
	}
}

// ── Files that still hold conflict markers ─────────────
//
// A stash pop that conflicts leaves unmerged entries with NO merge in progress,
// so nothing gated on MERGE_HEAD can see them — and the wizard stages with
// reset → add → commit, which removes git's own "you have unmerged files"
// refusal. Without this the markers went into a commit under an innocent
// subject.
func conflictedFilesModel(t *testing.T) Model {
	t.Helper()
	m := wizardModel(t, stepFiles,
		entry("clean.txt", types.StatusModified),
		entry("marked.txt", types.StatusConflicted),
	)
	return m
}

func TestConflictedFilesAreMarkedInTheSelector(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := conflictedFilesModel(t)
	out := m.viewFiles()
	if !strings.Contains(out, conflictedMark) {
		t.Errorf("the conflicted entry is not marked:\n%s", out)
	}
	if !strings.Contains(out, "marked.txt") {
		t.Errorf("the conflicted file is not listed:\n%s", out)
	}
}

func TestSelectAllSkipsConflictedFiles(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := conflictedFilesModel(t)
	m, _ = key(t, m, "a")

	if !m.files[0].Selected {
		t.Error("select-all skipped an ordinary file")
	}
	if m.files[1].Selected {
		t.Error("select-all swept a file full of conflict markers into the commit")
	}
	// A second press still toggles the rest off, and still leaves it alone.
	m, _ = key(t, m, "a")
	if m.files[0].Selected || m.files[1].Selected {
		t.Error("the second press did not deselect")
	}
}

func TestCommittingAConflictedFileNeedsAConfirmation(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := conflictedFilesModel(t)
	m.cursor = 1
	m, _ = key(t, m, " ") // deliberately select the conflicted one
	if !m.files[1].Selected {
		t.Fatal("space did not select the conflicted file")
	}

	gated, _ := key(t, m, "enter")
	if gated.step != stepFiles {
		t.Fatalf("enter walked past the gate to %v", gated.step)
	}
	if !gated.confirmConflictedCommit {
		t.Fatal("no confirmation was raised")
	}
	out := gated.viewFiles()
	for _, want := range []string{"conflict markers", "marked.txt", "Resolve before"} {
		if !strings.Contains(out, want) {
			t.Errorf("the gate never says %q:\n%s", want, out)
		}
	}
	if footer := renderHelpRows(gated.helpRows()); !strings.Contains(footer, "commit anyway") {
		t.Errorf("footer = %q", footer)
	}

	// Any other key goes back to the list, with the selection intact.
	cancelled, _ := key(t, gated, "j")
	if cancelled.step != stepFiles || cancelled.confirmConflictedCommit {
		t.Error("a stray key did not cancel the gate")
	}

	// y is the deliberate way through.
	through, _ := key(t, gated, "y")
	if through.step != stepType {
		t.Errorf("y landed on %v, want the wizard to continue", through.step)
	}
}

func TestACleanSelectionSkipsTheConflictGate(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := conflictedFilesModel(t)
	m.cursor = 0
	m, _ = key(t, m, " ")
	after, _ := key(t, m, "enter")
	if after.confirmConflictedCommit {
		t.Error("the gate fired on a selection with no conflicted files")
	}
	if after.step != stepType {
		t.Errorf("step = %v, want the ordinary wizard flow", after.step)
	}
}
