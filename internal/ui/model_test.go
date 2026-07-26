package ui

import (
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
