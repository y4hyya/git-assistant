package ui

import (
	"os/exec"
	"strings"
	"testing"

	"git-assist/internal/git"
)

// configModel parks a model on the config editor over a real repo.
func configModel(t *testing.T) Model {
	t.Helper()
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepConfig)
	m.loadConfigItems()
	return m
}

func localConfig(t *testing.T, key string) (string, bool) {
	t.Helper()
	out, err := exec.Command("git", "config", "--local", key).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// ── Navigation ─────────────────────────────────────────

func TestConfigCursorClampsAtBothEnds(t *testing.T) {
	m := configModel(t)

	m, _ = key(t, m, "up")
	if m.configCursor != 0 {
		t.Fatalf("up at the top moved the cursor to %d", m.configCursor)
	}
	for i := 0; i < len(m.configItems)+3; i++ {
		m, _ = key(t, m, "down")
	}
	if m.configCursor != len(m.configItems)-1 {
		t.Fatalf("cursor = %d, want the last item (%d)", m.configCursor, len(m.configItems)-1)
	}
	m, _ = key(t, m, "k")
	if m.configCursor != len(m.configItems)-2 {
		t.Fatalf("k did not move the cursor: %d", m.configCursor)
	}
	m, _ = key(t, m, "j")
	if m.configCursor != len(m.configItems)-1 {
		t.Fatalf("j did not move the cursor: %d", m.configCursor)
	}
}

func TestConfigEscReturnsToTheMenuAndQQuits(t *testing.T) {
	m := configModel(t)
	next, cmd := key(t, m, "esc")
	if next.step != stepMenu {
		t.Fatalf("step = %d, want the menu", next.step)
	}
	if cmd == nil {
		t.Fatal("returning to the menu did not request a refresh")
	}

	next, cmd = key(t, m, "q")
	if !next.quitting || cmd == nil {
		t.Fatalf("quitting=%v cmd=%v", next.quitting, cmd)
	}
}

// ── Scope toggle ───────────────────────────────────────

// tab is a genuine reload, not a label swap: the local and global scopes hold
// different values, and the editor writes to whichever one it is showing.
func TestConfigTabSwitchesScopeAndReloadsTheValues(t *testing.T) {
	m := configModel(t)
	gitCmd(t, "config", "--local", "user.name", "Local Person")
	m.loadConfigItems()

	if m.configGlobal {
		t.Fatal("the editor opens on the global scope")
	}
	if m.scopeName() != "local" {
		t.Fatalf("scopeName = %q", m.scopeName())
	}
	idx := configIndex(t, m, "User name")
	if got := m.configItems[idx].value; got != "Local Person" {
		t.Fatalf("local user.name = %q", got)
	}

	m, _ = key(t, m, "tab")
	if !m.configGlobal || m.scopeName() != "global" {
		t.Fatalf("tab did not switch scope: global=%v name=%q", m.configGlobal, m.scopeName())
	}
	// The global config is /dev/null in these tests, so the same key must now
	// read as unset rather than keeping the local value on screen.
	if item := m.configItems[configIndex(t, m, "User name")]; item.set {
		t.Fatalf("the local value survived the scope switch: %+v", item)
	}

	m, _ = key(t, m, "tab")
	if m.configGlobal {
		t.Fatal("tab is not a toggle")
	}
	if item := m.configItems[configIndex(t, m, "User name")]; !item.set || item.value != "Local Person" {
		t.Fatalf("the local value did not come back: %+v", item)
	}
}

// ── Inline edit ────────────────────────────────────────

func TestConfigEditWritesTheValueToTheShownScope(t *testing.T) {
	m := configModel(t)
	m.configCursor = configIndex(t, m, "User email")

	m, _ = key(t, m, "enter")
	if !m.configEditMode || !m.configEditInput.Focused() {
		t.Fatalf("enter did not open a focused editor: mode=%v focused=%v",
			m.configEditMode, m.configEditInput.Focused())
	}
	m.configEditInput.SetValue("  someone@example.invalid  ")
	m, _ = key(t, m, "enter")

	if m.configEditMode {
		t.Fatal("the editor stayed open after submitting")
	}
	got, ok := localConfig(t, "user.email")
	if !ok {
		t.Fatal("user.email was not written")
	}
	if got != "someone@example.invalid" {
		t.Fatalf("user.email = %q, want it trimmed", got)
	}
	if item := m.configItems[configIndex(t, m, "User email")]; item.value != got {
		t.Fatalf("the list was not reloaded after the write: %+v", item)
	}
}

// The editor opens pre-filled with the current value, so the common edit is a
// tweak rather than a retype.
func TestConfigEditPrefillsTheCurrentValue(t *testing.T) {
	m := configModel(t)
	gitCmd(t, "config", "--local", "core.editor", "vim")
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "Editor")

	m, _ = key(t, m, "enter")
	if got := m.configEditInput.Value(); got != "vim" {
		t.Fatalf("the editor opened with %q, want the current value", got)
	}
}

func TestConfigEditEscDiscardsTheEdit(t *testing.T) {
	m := configModel(t)
	gitCmd(t, "config", "--local", "core.editor", "vim")
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "Editor")

	m, _ = key(t, m, "enter")
	m.configEditInput.SetValue("emacs")
	m, _ = key(t, m, "esc")

	if m.configEditMode || m.configEditInput.Focused() {
		t.Fatalf("esc left the editor open: mode=%v focused=%v", m.configEditMode, m.configEditInput.Focused())
	}
	if got, _ := localConfig(t, "core.editor"); got != "vim" {
		t.Fatalf("core.editor = %q, want the untouched original", got)
	}
}

// Clearing a key that was never set must not run `git config --unset` — the
// call fails with exit 5 on a missing key and the failure would surface as an
// error banner for an action that did nothing wrong.
func TestConfigClearingAnUnsetKeyIsSilent(t *testing.T) {
	m := configModel(t)
	m.configCursor = configIndex(t, m, "Editor")
	if m.configItems[m.configCursor].set {
		t.Fatal("fixture already has core.editor set")
	}

	m, _ = key(t, m, "enter")
	m.configEditInput.SetValue("")
	m, _ = key(t, m, "enter")

	if m.err != nil {
		t.Fatalf("err = %v, want silence for a no-op", m.err)
	}
	if _, ok := localConfig(t, "core.editor"); ok {
		t.Fatal("core.editor exists after clearing an unset key")
	}
}

// Typing has to reach the textinput rather than being swallowed by the
// key switch.
func TestConfigEditTypingReachesTheInput(t *testing.T) {
	m := configModel(t)
	m.configCursor = configIndex(t, m, "User name")
	m, _ = key(t, m, "enter")
	m.configEditInput.SetValue("")

	m, _ = key(t, m, "z")
	if got := m.configEditInput.Value(); got != "z" {
		t.Fatalf("input value = %q, want the typed rune", got)
	}
}

// ── Remote URL ─────────────────────────────────────────

// The Remote URL row is not a config key: it writes through git remote, is
// per-repo regardless of the scope toggle, and changing it has to move
// hasRemote — the menu's Push entry is gated on that flag.
func TestConfigRemoteURLWritesThroughGitRemote(t *testing.T) {
	m := configModel(t)
	m.configGlobal = true // the scope toggle must not apply here
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "Remote URL")

	m, _ = key(t, m, "enter")
	m.configEditInput.SetValue("https://example.invalid/new.git")
	m, _ = key(t, m, "enter")

	if got := git.GetRemoteURL(); got != "https://example.invalid/new.git" {
		t.Fatalf("origin = %q", got)
	}
	if !m.hasRemote {
		t.Fatal("hasRemote was not refreshed — the menu would still hide Push")
	}
	if item := m.configItems[configIndex(t, m, "Remote URL")]; item.value != git.GetRemoteURL() {
		t.Fatalf("the row was not reloaded: %+v", item)
	}
}

// Emptying a Remote URL that was never set has nothing to remove and must not
// raise the confirmation.
func TestConfigEmptyRemoteURLWithNoOriginIsANoOp(t *testing.T) {
	m := configModel(t)
	m.configCursor = configIndex(t, m, "Remote URL")

	m, _ = key(t, m, "enter")
	m.configEditInput.SetValue("")
	m, _ = key(t, m, "enter")

	if m.configRemoveRemote {
		t.Fatal("the removal confirmation was raised with no origin to remove")
	}
	if m.err != nil {
		t.Fatalf("err = %v, want silence", m.err)
	}
}

// Any key other than `y` declines — this is a destructive prompt and the
// default has to be "no".
func TestConfigRemoveRemotePromptDefaultsToNo(t *testing.T) {
	m := configModel(t)
	gitCmd(t, "remote", "add", "origin", "https://example.invalid/x.git")
	m.hasRemote = true
	m.loadConfigItems()

	for _, k := range []string{"n", "esc", "j", "enter"} {
		m.configRemoveRemote = true
		m.configRemoveURL = "https://example.invalid/x.git"
		next, _ := key(t, m, k)

		if next.configRemoveRemote {
			t.Fatalf("%q left the prompt up", k)
		}
		if next.configRemoveURL != "" {
			t.Fatalf("%q left the URL on the prompt", k)
		}
		if git.GetRemoteURL() == "" {
			t.Fatalf("%q removed origin — only y may do that", k)
		}
	}
}

// ── Branch picker (init.defaultBranch) ─────────────────

// The default-branch row picks from the branches that exist rather than
// accepting free text, so it cannot be set to a name git would never create.
func TestConfigDefaultBranchPicker(t *testing.T) {
	m := configModel(t)
	gitCmd(t, "branch", "develop")
	gitCmd(t, "branch", "release")
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "Default branch")

	m, _ = key(t, m, "enter")
	if !m.configPickMode {
		t.Fatal("enter opened a text editor instead of the branch picker")
	}
	if m.configEditMode {
		t.Fatal("the picker and the inline editor are both open")
	}
	if len(m.configPickItems) < 3 {
		t.Fatalf("picker items = %v, want the local branches", m.configPickItems)
	}
	for _, name := range m.configPickItems {
		if strings.HasPrefix(name, "origin/") {
			t.Fatalf("picker lists a remote branch: %v", m.configPickItems)
		}
	}

	// Cursor clamps at both ends.
	m, _ = key(t, m, "up")
	if m.configPickCursor != 0 {
		t.Fatalf("up at the top moved to %d", m.configPickCursor)
	}
	for i := 0; i < len(m.configPickItems)+3; i++ {
		m, _ = key(t, m, "down")
	}
	if m.configPickCursor != len(m.configPickItems)-1 {
		t.Fatalf("cursor = %d, want the last branch", m.configPickCursor)
	}

	want := m.configPickItems[m.configPickCursor]
	m, _ = key(t, m, "enter")

	if m.configPickMode {
		t.Fatal("the picker stayed open after choosing")
	}
	got, ok := localConfig(t, "init.defaultBranch")
	if !ok {
		t.Fatal("init.defaultBranch was not written")
	}
	if got != want {
		t.Fatalf("init.defaultBranch = %q, want the selected branch %q", got, want)
	}
	if item := m.configItems[configIndex(t, m, "Default branch")]; item.value != want {
		t.Fatalf("the row was not reloaded: %+v", item)
	}
}

// Re-opening the picker lands the cursor on the value already configured, so
// the current setting is visible rather than having to be hunted for.
func TestConfigDefaultBranchPickerPreselectsTheCurrentValue(t *testing.T) {
	m := configModel(t)
	gitCmd(t, "branch", "develop")
	gitCmd(t, "config", "--local", "init.defaultBranch", "develop")
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "Default branch")

	m, _ = key(t, m, "enter")
	if got := m.configPickItems[m.configPickCursor]; got != "develop" {
		t.Fatalf("cursor sits on %q, want the configured branch", got)
	}
}

func TestConfigDefaultBranchPickerEscKeepsTheOldValue(t *testing.T) {
	m := configModel(t)
	gitCmd(t, "branch", "develop")
	gitCmd(t, "config", "--local", "init.defaultBranch", "main")
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "Default branch")

	m, _ = key(t, m, "enter")
	m, _ = key(t, m, "down")
	m, _ = key(t, m, "esc")

	if m.configPickMode {
		t.Fatal("esc left the picker open")
	}
	if got, _ := localConfig(t, "init.defaultBranch"); got != "main" {
		t.Fatalf("init.defaultBranch = %q, want the untouched original", got)
	}
}

// The picker renders inline under its row; with no branches it says so rather
// than drawing an empty gap.
func TestConfigPickerRendersWithAndWithoutBranches(t *testing.T) {
	m := configModel(t)
	gitCmd(t, "branch", "develop")
	m.loadConfigItems()
	m.configCursor = configIndex(t, m, "Default branch")
	m, _ = key(t, m, "enter")

	if out := m.viewConfig(); !strings.Contains(out, "develop") {
		t.Fatalf("the picker does not list the branches:\n%s", out)
	}

	m.configPickItems = nil
	if out := m.viewConfig(); !strings.Contains(out, "no branches yet") {
		t.Fatalf("an empty picker draws nothing to explain itself:\n%s", out)
	}
}

// The consequence of an empty field is spelled out only while editing — it is
// not a key, so it does not belong in the footer.
func TestConfigEditDisclosesThatClearingUnsets(t *testing.T) {
	m := configModel(t)
	m.configCursor = configIndex(t, m, "User name")

	if strings.Contains(m.viewConfig(), "unsets the key") {
		t.Fatal("the unset hint is shown when nothing is being edited")
	}
	m, _ = key(t, m, "enter")
	if !strings.Contains(m.viewConfig(), "unsets the key") {
		t.Fatalf("the unset consequence is never disclosed:\n%s", m.viewConfig())
	}
}
