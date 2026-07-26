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
	// Identity and config isolation go through the environment so the calls
	// internal/git makes (plain exec.Command, inherited env) see them too.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
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
	m.branches = []string{"main"}
	m.hasRemote = false

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

// ── Push branch index ──────────────────────────────────

func TestPushClampsStaleBranchIdx(t *testing.T) {
	m := Model{step: stepPush, branches: []string{"main", "dev"}, branchIdx: 5, height: 30}
	m, _ = key(t, m, "up")
	if m.branchIdx != 0 {
		t.Fatalf("branchIdx = %d, want 0", m.branchIdx)
	}

	empty := Model{step: stepPush, height: 30}
	empty, cmd := key(t, empty, "enter")
	if cmd != nil {
		t.Fatal("push dispatched with no branches")
	}
	_ = empty
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
	if note := next.(Model).branchOpNote; !strings.Contains(note, "stashed and restored") {
		t.Errorf("result note = %q, want the stash disclosed", note)
	}
}

// The menu's sync shortcut merges from outside the branch manager, where
// there is no note line — the stash round-trip still has to be stated, and
// nothing may be parked on the model to resurface later.
func TestMergeStashNoteReachesTheMenuToo(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)
	next, _ := m.Update(branchMergeResultMsg{source: "origin/main", merged: true, stashRestored: true})
	got := next.(Model)
	if got.branchOpNote != "" {
		t.Errorf("a stale branch-manager note was parked on the model: %q", got.branchOpNote)
	}
	if got.err == nil || !strings.Contains(got.err.Error(), "stashed and restored") {
		t.Fatalf("the stash round-trip was not stated on the menu: %v", got.err)
	}
	if !strings.HasPrefix(got.err.Error(), symWarn) {
		t.Errorf("advisory note = %q, want the symWarn prefix so it renders as a note", got.err)
	}
}

func TestMergeConflictOnADirtyTreeRestoresTheStash(t *testing.T) {
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
	if got.err == nil {
		t.Fatal("the failure was swallowed")
	}
	if !strings.Contains(got.err.Error(), "restored") {
		t.Errorf("error = %q, want it to state what happened to the stash", got.err)
	}
	if content := readFile(t, "notes.txt"); content != "in progress\n" {
		t.Errorf("uncommitted work not restored: notes.txt = %q", content)
	}
	if n := stashDepth(t); n != 0 {
		t.Errorf("%d stash entr(ies) left behind after recovery", n)
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
