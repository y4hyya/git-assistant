package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
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
// suppress. Fixtures that create a BARE repo then leave HEAD on a branch
// nothing ever creates, and clones taken from them come out with no checkout.
func isolatedGitConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(path, []byte("[init]\n\tdefaultBranch = main\n"), 0o644); err != nil {
		t.Fatalf("write test gitconfig: %v", err)
	}
	return path
}

// initTempDir chdirs into an empty NON-repository directory with the host's
// git config neutralised — the exact state that makes main.go choose
// NewInitModel over the dashboard.
func initTempDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// See tempRepo: a ctrl+c test anywhere in this binary cancels the
	// package-global network context for good.
	git.ResetNetworkOps()
	t.Setenv("GIT_CONFIG_GLOBAL", isolatedGitConfig(t))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t.invalid")

	dir := filepath.Join(t.TempDir(), "fresh-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

// initModel is NewInitModel with a terminal size, parked on the option list.
func initModel(t *testing.T) Model {
	t.Helper()
	initTempDir(t)
	m := NewInitModel()
	m.width = 120
	m.height = 40
	return m
}

// noGH empties PATH so HasGHCLI answers false. Safe only after the model is
// built — nothing may shell out to git afterwards.
func noGH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// stubGH puts a `gh` on PATH that exits with the given code. Exit 0 makes
// IsGHAuthed true; anything else makes it false.
func stubGH(t *testing.T, exitCode int) {
	t.Helper()
	binDir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// seedBareRemote builds a bare repo holding one commit and returns its path.
// Used as a stand-in for "a GitHub repo that already has commits".
func seedBareRemote(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "origin.git")
	work := filepath.Join(t.TempDir(), "seed")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "--bare", remote)
	run("init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", work, "add", "-A")
	run("-C", work, "commit", "-q", "-m", "Initial commit")
	run("-C", work, "push", "-q", remote, "main:main")
	return remote
}

// ── Option list ────────────────────────────────────────

func TestInitOptionCursorClampsAtBothEnds(t *testing.T) {
	m := initModel(t)

	m, _ = key(t, m, "up")
	if m.initCursor != 0 {
		t.Fatalf("up at the top moved the cursor to %d", m.initCursor)
	}
	for i := 0; i < len(initChoiceLabels)+3; i++ {
		m, _ = key(t, m, "down")
	}
	if m.initCursor != len(initChoiceLabels)-1 {
		t.Fatalf("cursor = %d after running off the end, want %d", m.initCursor, len(initChoiceLabels)-1)
	}
	// j/k are the same movement.
	m, _ = key(t, m, "k")
	if m.initCursor != len(initChoiceLabels)-2 {
		t.Fatalf("k did not move the cursor: %d", m.initCursor)
	}
	m, _ = key(t, m, "j")
	if m.initCursor != len(initChoiceLabels)-1 {
		t.Fatalf("j did not move the cursor: %d", m.initCursor)
	}
}

// Cancel is the fourth option and the only one that leaves without doing
// anything. `q` from the same screen must do the same.
func TestInitCancelAndQuitLeaveWithoutTouchingTheDirectory(t *testing.T) {
	t.Run("cancel-entry", func(t *testing.T) {
		m := initModel(t)
		m.initCursor = int(initChoiceCancel)
		m, cmd := key(t, m, "enter")
		if !m.quitting || cmd == nil {
			t.Fatalf("enter on Cancel: quitting=%v cmd=%v", m.quitting, cmd)
		}
		if git.IsGitRepo() {
			t.Fatal("Cancel initialized a repository")
		}
	})

	t.Run("q", func(t *testing.T) {
		m := initModel(t)
		m, cmd := key(t, m, "q")
		if !m.quitting || cmd == nil {
			t.Fatalf("q: quitting=%v cmd=%v", m.quitting, cmd)
		}
	})
}

func TestInitLocalAndConnectGoStraightToTheTemplatePicker(t *testing.T) {
	for _, choice := range []initChoice{initChoiceLocal, initChoiceConnect} {
		m := initModel(t)
		m.initCursor = int(choice)
		m, _ = key(t, m, "enter")
		if m.initPhase != initPhasePickTemplate {
			t.Fatalf("choice %d landed on phase %d, want the template picker", choice, m.initPhase)
		}
	}
}

// The GitHub option is gated on `gh` twice — installed, then authenticated —
// and both answers have to be available BEFORE the user has invested any
// keystrokes in the flow.
func TestInitGHCreateChecksTheCLIUpFront(t *testing.T) {
	t.Run("not-installed", func(t *testing.T) {
		m := initModel(t)
		noGH(t)
		m.initCursor = int(initChoiceGHCreate)
		m, _ = key(t, m, "enter")

		if m.initPhase != initPhasePickOption {
			t.Fatalf("phase = %d, want to stay on the option list", m.initPhase)
		}
		if m.err == nil || !strings.Contains(m.err.Error(), "cli.github.com") {
			t.Fatalf("err = %v, want an install hint", m.err)
		}
	})

	t.Run("not-authenticated", func(t *testing.T) {
		m := initModel(t)
		stubGH(t, 1)
		m.initCursor = int(initChoiceGHCreate)
		m, _ = key(t, m, "enter")

		if m.initPhase != initPhaseConfirmGHAuth {
			t.Fatalf("phase = %d, want the gh auth offer", m.initPhase)
		}
		if m.err != nil {
			t.Fatalf("err = %v, want the offer instead of an error", m.err)
		}
	})

	t.Run("authenticated", func(t *testing.T) {
		m := initModel(t)
		stubGH(t, 0)
		m.initCursor = int(initChoiceGHCreate)
		m, _ = key(t, m, "enter")

		if m.initPhase != initPhasePickTemplate {
			t.Fatalf("phase = %d, want the template picker", m.initPhase)
		}
	})
}

// ── Template picker ────────────────────────────────────

// The picker is shared by all three flows and each one continues somewhere
// different: local starts work immediately, the other two collect input first.
func TestInitTemplatePickerRoutesPerChoice(t *testing.T) {
	cases := []struct {
		choice    initChoice
		wantPhase initPhase
	}{
		{initChoiceLocal, initPhaseWorking},
		{initChoiceConnect, initPhaseInputURL},
		{initChoiceGHCreate, initPhaseInputRepoName},
	}
	for _, tc := range cases {
		m := initModel(t)
		m.initCursor = int(tc.choice)
		m.initPhase = initPhasePickTemplate

		m, cmd := key(t, m, "enter")
		if m.initPhase != tc.wantPhase {
			t.Fatalf("choice %d → phase %d, want %d", tc.choice, m.initPhase, tc.wantPhase)
		}
		if tc.wantPhase == initPhaseWorking {
			if !m.initWorking || cmd == nil {
				t.Fatalf("local did not start work: working=%v cmd=%v", m.initWorking, cmd)
			}
			continue
		}
		// Text-input phases: the input has to be focused or the first
		// keystroke is dropped.
		focused := m.initURLInput.Focused() || m.initNameInput.Focused()
		if !focused {
			t.Fatalf("choice %d left both inputs blurred", tc.choice)
		}
	}
}

func TestInitTemplatePickerNavigationAndExits(t *testing.T) {
	m := initModel(t)
	m.initPhase = initPhasePickTemplate
	m.initTemplateCursor = 0

	m, _ = key(t, m, "up")
	if m.initTemplateCursor != 0 {
		t.Fatalf("up at the top moved to %d", m.initTemplateCursor)
	}
	for i := 0; i < len(m.initTemplateOptions)+3; i++ {
		m, _ = key(t, m, "down")
	}
	if m.initTemplateCursor != len(m.initTemplateOptions)-1 {
		t.Fatalf("cursor = %d, want the last template", m.initTemplateCursor)
	}

	m, _ = key(t, m, "esc")
	if m.initPhase != initPhasePickOption {
		t.Fatalf("esc → phase %d, want back to the option list", m.initPhase)
	}

	m.initPhase = initPhasePickTemplate
	m, cmd := key(t, m, "q")
	if !m.quitting || cmd == nil {
		t.Fatalf("q on the template picker: quitting=%v cmd=%v", m.quitting, cmd)
	}
}

// The picker preselects whatever the directory looks like, so the common case
// is a single `enter`.
func TestInitTemplatePreselectsTheDetectedLanguage(t *testing.T) {
	initTempDir(t)
	if err := os.WriteFile("go.mod", []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewInitModel()

	if m.initDetectedTemplate != "Go" {
		t.Fatalf("detected %q in a directory with a go.mod", m.initDetectedTemplate)
	}
	if got := m.initTemplateOptions[m.initTemplateCursor].Name; got != "Go" {
		t.Fatalf("cursor sits on %q, want the detected template", got)
	}
}

// ── URL input ──────────────────────────────────────────

func TestInitURLInputRefusesJunkAndAcceptsARemote(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		m := initModel(t)
		m.initCursor = int(initChoiceConnect)
		m.initPhase = initPhaseInputURL
		m, _ = key(t, m, "enter")

		if m.initPhase != initPhaseInputURL {
			t.Fatalf("an empty URL advanced to phase %d", m.initPhase)
		}
		if m.err == nil || !strings.Contains(m.err.Error(), "required") {
			t.Fatalf("err = %v, want a 'required' message", m.err)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		m := initModel(t)
		m.initCursor = int(initChoiceConnect)
		m.initPhase = initPhaseInputURL
		m.initURLInput.SetValue("just-a-word")
		m, _ = key(t, m, "enter")

		if m.initPhase != initPhaseInputURL {
			t.Fatalf("an invalid URL advanced to phase %d", m.initPhase)
		}
		if m.err == nil || !strings.Contains(m.err.Error(), "invalid URL") {
			t.Fatalf("err = %v, want an 'invalid URL' message", m.err)
		}
		if m.initRemoteURL != "" {
			t.Fatalf("initRemoteURL = %q, want it left unset", m.initRemoteURL)
		}
	})

	t.Run("accepted", func(t *testing.T) {
		m := initModel(t)
		m.initCursor = int(initChoiceConnect)
		m.initPhase = initPhaseInputURL
		// Surrounding whitespace is what a paste actually contains.
		m.initURLInput.SetValue("  git@github.com:acme/tool.git  ")
		m, cmd := key(t, m, "enter")

		if m.initPhase != initPhaseWorking || !m.initWorking || cmd == nil {
			t.Fatalf("a valid URL did not start work: phase=%d working=%v cmd=%v",
				m.initPhase, m.initWorking, cmd)
		}
		if m.initRemoteURL != "git@github.com:acme/tool.git" {
			t.Fatalf("initRemoteURL = %q, want it trimmed", m.initRemoteURL)
		}
		if m.initURLInput.Focused() {
			t.Fatal("the URL input is still focused during the async work")
		}
	})

	t.Run("esc-returns-to-the-template-picker", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseInputURL
		m.initURLInput.Focus()
		m, _ = key(t, m, "esc")

		if m.initPhase != initPhasePickTemplate {
			t.Fatalf("esc → phase %d", m.initPhase)
		}
		if m.initURLInput.Focused() {
			t.Fatal("the input kept focus after esc")
		}
	})

	// Ordinary keystrokes have to reach the textinput, not be swallowed by
	// the switch.
	t.Run("typing-reaches-the-input", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseInputURL
		m.initURLInput.Focus()
		m.initURLInput.SetValue("")
		m, _ = key(t, m, "x")
		if m.initURLInput.Value() != "x" {
			t.Fatalf("input value = %q, want the typed rune", m.initURLInput.Value())
		}
	})
}

// ── Repo name input ────────────────────────────────────

func TestInitRepoNameValidatesBeforeAskingGitHub(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseInputRepoName
		m.initNameInput.SetValue("   ")
		m, _ = key(t, m, "enter")

		if m.initPhase != initPhaseInputRepoName {
			t.Fatalf("an empty name advanced to phase %d", m.initPhase)
		}
		if m.err == nil || !strings.Contains(m.err.Error(), "required") {
			t.Fatalf("err = %v", m.err)
		}
	})

	t.Run("invalid", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseInputRepoName
		m.initNameInput.SetValue("bad name!")
		m, _ = key(t, m, "enter")

		if m.initPhase != initPhaseInputRepoName {
			t.Fatalf("an invalid name advanced to phase %d", m.initPhase)
		}
		if m.err == nil || !strings.Contains(m.err.Error(), "invalid name") {
			t.Fatalf("err = %v", m.err)
		}
	})

	t.Run("accepted", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseInputRepoName
		m.initVisibilityCursor = 1 // stale value from a previous pass
		m.initNameInput.SetValue(" acme/tool ")
		m, _ = key(t, m, "enter")

		if m.initPhase != initPhasePickVisibility {
			t.Fatalf("phase = %d, want the visibility picker", m.initPhase)
		}
		if m.initRepoName != "acme/tool" {
			t.Fatalf("initRepoName = %q, want it trimmed", m.initRepoName)
		}
		if m.initVisibilityCursor != 0 {
			t.Fatalf("visibility cursor = %d, want it reset to Public", m.initVisibilityCursor)
		}
	})
}

// esc means two different things depending on how the screen was reached: the
// first-run flow steps back one screen, the menu's recovery entry has no
// earlier screen to step back to and must land on the dashboard.
func TestInitRepoNameEscRoutesByHowTheScreenWasEntered(t *testing.T) {
	t.Run("first-run", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseInputRepoName
		m.initNameInput.Focus()
		m, _ = key(t, m, "esc")

		if m.initPhase != initPhasePickTemplate {
			t.Fatalf("phase = %d, want the template picker", m.initPhase)
		}
		if m.step != stepInit {
			t.Fatalf("step = %d, want to stay in the init flow", m.step)
		}
	})

	t.Run("menu-recovery", func(t *testing.T) {
		tempRepo(t, "chore: seed", "")
		m := wizardModel(t, stepInit)
		m.ghReuseMode = true
		m.initPhase = initPhaseInputRepoName
		m.initNameInput.Focus()
		m, _ = key(t, m, "esc")

		if m.step != stepMenu {
			t.Fatalf("step = %d, want the menu", m.step)
		}
		if m.ghReuseMode {
			t.Fatal("ghReuseMode survived the exit — the next visit would misroute")
		}
	})
}

func TestIsValidGitHubRepoName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"tool", true},
		{"my-tool", true},
		{"my_tool", true},
		{"my.tool", true},
		{"Tool123", true},
		{"acme/tool", true},
		{"acme-org/my.tool_2", true},
		{"", false},
		{"-leading-dash", false},
		{".leading-dot", false},
		{"has space", false},
		{"has!bang", false},
		{"acme/", false},
		{"/tool", false},
		{"acme/-bad", false},
		{"-acme/tool", false},
		{"acme/tool/extra", false}, // SplitN(2) leaves "tool/extra", and / is not allowed
		{strings.Repeat("a", 100), true},
		{strings.Repeat("a", 101), false},
	}
	for _, tc := range cases {
		if got := isValidGitHubRepoName(tc.name); got != tc.want {
			t.Errorf("isValidGitHubRepoName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ── Visibility picker ──────────────────────────────────

func TestInitVisibilityPicker(t *testing.T) {
	t.Run("public-is-the-default", func(t *testing.T) {
		m := initModel(t)
		m.initCursor = int(initChoiceGHCreate)
		m.initPhase = initPhasePickVisibility
		m.initVisibilityCursor = 0
		m, cmd := key(t, m, "enter")

		if m.initPrivate {
			t.Fatal("cursor on Public produced a private repo")
		}
		if m.initPhase != initPhaseWorking || cmd == nil {
			t.Fatalf("enter did not start work: phase=%d cmd=%v", m.initPhase, cmd)
		}
	})

	t.Run("private", func(t *testing.T) {
		m := initModel(t)
		m.initCursor = int(initChoiceGHCreate)
		m.initPhase = initPhasePickVisibility
		m, _ = key(t, m, "down")
		m, _ = key(t, m, "enter")

		if !m.initPrivate {
			t.Fatal("cursor on Private produced a public repo")
		}
	})

	t.Run("esc-refocuses-the-name-input", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhasePickVisibility
		m, _ = key(t, m, "esc")

		if m.initPhase != initPhaseInputRepoName {
			t.Fatalf("phase = %d, want the name input", m.initPhase)
		}
		if !m.initNameInput.Focused() {
			t.Fatal("stepping back left the name input blurred — keystrokes would be dropped")
		}
	})

	t.Run("cursor-clamps", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhasePickVisibility
		m, _ = key(t, m, "up")
		if m.initVisibilityCursor != 0 {
			t.Fatalf("cursor = %d after up at the top", m.initVisibilityCursor)
		}
		for i := 0; i < 5; i++ {
			m, _ = key(t, m, "down")
		}
		if m.initVisibilityCursor != len(initVisibilityLabels)-1 {
			t.Fatalf("cursor = %d, want %d", m.initVisibilityCursor, len(initVisibilityLabels)-1)
		}
	})
}

// ── gh auth offer ──────────────────────────────────────

func TestInitConfirmGHAuthDeclineRoutesByMode(t *testing.T) {
	for _, k := range []string{"n", "esc"} {
		t.Run("first-run-"+k, func(t *testing.T) {
			m := initModel(t)
			m.initPhase = initPhaseConfirmGHAuth
			m, _ = key(t, m, k)

			if m.initPhase != initPhasePickOption {
				t.Fatalf("phase = %d, want the option list", m.initPhase)
			}
			if m.step != stepInit {
				t.Fatalf("step = %d, want to stay in the init flow", m.step)
			}
		})

		t.Run("menu-recovery-"+k, func(t *testing.T) {
			tempRepo(t, "chore: seed", "")
			m := wizardModel(t, stepInit)
			m.ghReuseMode = true
			m.initPhase = initPhaseConfirmGHAuth
			m, _ = key(t, m, k)

			if m.step != stepMenu {
				t.Fatalf("step = %d, want the menu — the option list has nothing to offer an initialized repo", m.step)
			}
			if m.ghReuseMode {
				t.Fatal("ghReuseMode survived the exit")
			}
		})
	}

	t.Run("q-quits", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseConfirmGHAuth
		m, cmd := key(t, m, "q")
		if !m.quitting || cmd == nil {
			t.Fatalf("quitting=%v cmd=%v", m.quitting, cmd)
		}
	})

	t.Run("accept-suspends-the-tui", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseConfirmGHAuth
		m, cmd := key(t, m, "y")

		if m.initPhase != initPhaseWorking || !m.initWorking {
			t.Fatalf("phase=%d working=%v, want the working screen", m.initPhase, m.initWorking)
		}
		if cmd == nil {
			t.Fatal("no command — gh auth login was never launched")
		}
	})
}

// ── Async results ──────────────────────────────────────

// Input is blocked while the async work runs; only the result message moves
// the flow on.
func TestInitWorkingPhaseIgnoresKeypresses(t *testing.T) {
	m := initModel(t)
	m.initPhase = initPhaseWorking
	m.initWorking = true

	for _, k := range []string{"q", "enter", "esc", "j"} {
		next, _ := key(t, m, k)
		if next.initPhase != initPhaseWorking {
			t.Fatalf("%q moved off the working screen to phase %d", k, next.initPhase)
		}
		if next.quitting {
			t.Fatalf("%q quit during an in-flight operation", k)
		}
	}
}

func TestInitResultFailureReturnsToTheOptionList(t *testing.T) {
	m := initModel(t)
	m.initPhase = initPhaseWorking
	m.initWorking = true

	next, _ := m.Update(initResultMsg{err: fmt.Errorf("init failed: boom")})
	out := next.(Model)

	if out.initWorking {
		t.Fatal("still marked as working after the result landed")
	}
	if out.initPhase != initPhasePickOption {
		t.Fatalf("phase = %d, want the option list so the user can pick again", out.initPhase)
	}
	if out.err == nil || !strings.Contains(out.err.Error(), "boom") {
		t.Fatalf("err = %v, want git's own message", out.err)
	}
	if out.step != stepInit {
		t.Fatalf("step = %d, want to stay in the init flow", out.step)
	}
}

func TestInitResultSuccessLandsOnTheMenu(t *testing.T) {
	m := initModel(t)
	m.initPhase = initPhaseWorking
	m.initWorking = true
	m.ghReuseMode = true

	next, cmd := m.Update(initResultMsg{
		branch:  "main",
		message: "Initialized empty repo — stage your first commit from the Files screen.",
	})
	out := next.(Model)

	if out.step != stepMenu {
		t.Fatalf("step = %d, want the menu", out.step)
	}
	if out.branch != "main" {
		t.Fatalf("branch = %q, want the one the flow reported", out.branch)
	}
	if out.initSuccessMsg == "" {
		t.Fatal("the success banner was dropped — the menu has nothing to explain what happened")
	}
	if out.ghReuseMode {
		t.Fatal("ghReuseMode survived a successful run")
	}
	if out.initWorking {
		t.Fatal("still marked as working")
	}
	if cmd == nil {
		t.Fatal("no dashboard refresh was requested after the repo changed underneath it")
	}
}

// `gh auth login` exits 0 even when the user closes the browser without
// finishing, so the session check — not the process status — decides.
func TestGHAuthResultTrustsTheSessionCheckOverTheExitCode(t *testing.T) {
	t.Run("exit-0-but-still-logged-out", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseWorking
		m.initWorking = true
		stubGH(t, 1) // `gh auth status` says no

		next, _ := m.Update(ghAuthResultMsg{err: nil})
		out := next.(Model)

		if out.initPhase != initPhasePickOption {
			t.Fatalf("phase = %d, want the option list", out.initPhase)
		}
		if out.err == nil || !strings.Contains(out.err.Error(), "still not authenticated") {
			t.Fatalf("err = %v, want the 'still not authenticated' explanation", out.err)
		}
	})

	t.Run("failed-and-still-logged-out", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseWorking
		m.initWorking = true
		stubGH(t, 1)

		next, _ := m.Update(ghAuthResultMsg{err: fmt.Errorf("exit status 1")})
		out := next.(Model)

		if out.err == nil || !strings.Contains(out.err.Error(), "gh auth failed") {
			t.Fatalf("err = %v, want the launch failure reported", out.err)
		}
	})

	t.Run("authenticated-resumes-the-first-run-flow", func(t *testing.T) {
		m := initModel(t)
		m.initPhase = initPhaseWorking
		m.initWorking = true
		stubGH(t, 0)

		next, _ := m.Update(ghAuthResultMsg{})
		out := next.(Model)

		if out.initPhase != initPhasePickTemplate {
			t.Fatalf("phase = %d, want the template picker", out.initPhase)
		}
		if out.err != nil {
			t.Fatalf("err = %v", out.err)
		}
	})

	t.Run("authenticated-resumes-the-recovery-flow", func(t *testing.T) {
		tempRepo(t, "chore: seed", "")
		m := wizardModel(t, stepInit)
		m.ghReuseMode = true
		m.initPhase = initPhaseWorking
		m.initWorking = true
		stubGH(t, 0)

		next, _ := m.Update(ghAuthResultMsg{})
		out := next.(Model)

		// The recovery flow has no template step — it resumes at the name.
		if out.initPhase != initPhaseInputRepoName {
			t.Fatalf("phase = %d, want the repo name input", out.initPhase)
		}
		if !out.initNameInput.Focused() {
			t.Fatal("resuming left the name input blurred")
		}
	})

	t.Run("logged-out-recovery-goes-back-to-the-menu", func(t *testing.T) {
		tempRepo(t, "chore: seed", "")
		m := wizardModel(t, stepInit)
		m.ghReuseMode = true
		m.initPhase = initPhaseWorking
		m.initWorking = true
		stubGH(t, 1)

		next, _ := m.Update(ghAuthResultMsg{})
		out := next.(Model)

		if out.step != stepMenu {
			t.Fatalf("step = %d, want the menu", out.step)
		}
		if out.ghReuseMode {
			t.Fatal("ghReuseMode survived")
		}
		if out.err == nil {
			t.Fatal("the failure was swallowed on the way back to the menu")
		}
	})
}

// Spinner ticks must keep flowing while work is in flight, or the working
// screen freezes.
func TestInitWorkingForwardsSpinnerTicks(t *testing.T) {
	m := initModel(t)
	m.initPhase = initPhaseWorking
	m.initWorking = true

	next, cmd := m.Update(m.spinner.Tick())
	if _, ok := next.(Model); !ok {
		t.Fatalf("Update returned %T", next)
	}
	if cmd == nil {
		t.Fatal("the spinner stopped ticking during the async operation")
	}
}

// ── runInitFlow ────────────────────────────────────────

// The local option is the whole job in one command: a repo on `main` with the
// picked .gitignore already written.
func TestRunInitFlowLocalCreatesTheRepoAndGitignore(t *testing.T) {
	initTempDir(t)
	tpl := git.GitignoreTemplate{Name: "Go", Content: "*.exe\n/vendor/\n"}

	res := runInitFlow(initChoiceLocal, tpl, "", "", false, false)
	if res.err != nil {
		t.Fatalf("runInitFlow: %v", res.err)
	}
	if res.branch != "main" {
		t.Fatalf("branch = %q, want main", res.branch)
	}
	if !git.IsGitRepo() {
		t.Fatal("no repository was created")
	}
	if b, _ := git.GetCurrentBranch(); b != "main" {
		t.Fatalf("current branch = %q", b)
	}
	content, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatalf(".gitignore was not written: %v", err)
	}
	if !strings.Contains(string(content), "/vendor/") {
		t.Fatalf(".gitignore = %q, want the template's content", content)
	}
	if res.message == "" {
		t.Fatal("no message for the menu banner")
	}
	if res.warning != "" {
		t.Fatalf("warning = %q on a plain local init", res.warning)
	}
}

// The "None (skip)" template writes nothing at all.
func TestRunInitFlowLocalSkipsAnEmptyTemplate(t *testing.T) {
	initTempDir(t)
	res := runInitFlow(initChoiceLocal, git.GitignoreTemplate{Name: "None (skip)"}, "", "", false, false)
	if res.err != nil {
		t.Fatalf("runInitFlow: %v", res.err)
	}
	if _, err := os.Stat(".gitignore"); err == nil {
		t.Fatal("the skip template still created a .gitignore")
	}
}

// Connecting an EMPTY remote is the supported case and must not warn.
func TestRunInitFlowConnectToAnEmptyRemoteDoesNotWarn(t *testing.T) {
	initTempDir(t)
	empty := filepath.Join(t.TempDir(), "empty.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", empty).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}

	res := runInitFlow(initChoiceConnect, git.GitignoreTemplate{}, empty, "", false, false)
	if res.err != nil {
		t.Fatalf("runInitFlow: %v", res.err)
	}
	if res.warning != "" {
		t.Fatalf("warning = %q for an empty remote", res.warning)
	}
	if git.GetRemoteURL() != empty {
		t.Fatalf("origin = %q, want %q", git.GetRemoteURL(), empty)
	}
}

// A remote that already has commits is a dead end for a repository with none:
// the first local commit becomes a second root and every push and pull after
// it is refused. The warning has to say so BEFORE the user commits anything.
func TestRunInitFlowConnectWarnsWhenTheRemoteAlreadyHasCommits(t *testing.T) {
	initTempDir(t)
	remote := seedBareRemote(t)

	res := runInitFlow(initChoiceConnect, git.GitignoreTemplate{}, remote, "", false, false)
	if res.err != nil {
		t.Fatalf("runInitFlow: %v", res.err)
	}
	if res.warning == "" {
		t.Fatal("no warning for a remote with unrelated history")
	}
	if !strings.Contains(res.warning, "Clone the remote instead") {
		t.Fatalf("warning = %q, want the clone suggestion", res.warning)
	}
	// formatError appends "Run git pull first" to anything mentioning a
	// rejected / non-fast-forward push — the exact advice this warning exists
	// to head off.
	for _, forbidden := range []string{"rejected", "non-fast-forward"} {
		if strings.Contains(res.warning, forbidden) {
			t.Fatalf("warning contains %q, which formatError turns into 'run git pull first'", forbidden)
		}
	}
	// The remote stays configured either way — the user may know better.
	if git.GetRemoteURL() != remote {
		t.Fatalf("origin = %q, want it left configured", git.GetRemoteURL())
	}
}

// reuseExisting is the menu's recovery path: the repo exists, only the remote
// is missing. It must not re-init, must not touch .gitignore, and must report
// the branch that is actually checked out.
func TestRunInitFlowReuseKeepsTheExistingRepo(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	if out, err := exec.Command("git", "checkout", "-q", "-b", "develop").CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v\n%s", err, out)
	}
	headBefore, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	remote := seedBareRemote(t)

	res := runInitFlow(initChoiceConnect, git.GitignoreTemplate{Name: "Go", Content: "*.exe\n"}, remote, "", false, true)
	if res.err != nil {
		t.Fatalf("runInitFlow: %v", res.err)
	}
	if res.branch != "develop" {
		t.Fatalf("branch = %q, want the checked-out branch", res.branch)
	}
	if _, err := os.Stat(".gitignore"); err == nil {
		t.Fatal("the reuse path wrote a .gitignore over an existing repo")
	}
	headAfter, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(headBefore) != string(headAfter) {
		t.Fatal("the existing history was disturbed")
	}
	// Both sides have commits and they are unrelated — the other half of the
	// warning.
	if !strings.Contains(res.warning, "unrelated histories") {
		t.Fatalf("warning = %q, want the unrelated-histories wording", res.warning)
	}
}

// --push is only correct when there is something to push: `gh repo create
// --push` on a repo with no commits fails. The fresh-init path therefore never
// asks for it, and the recovery path only does when HEAD exists.
func TestRunInitFlowGHCreatePushesOnlyWhenThereIsSomethingToPush(t *testing.T) {
	t.Run("fresh-init-never-pushes", func(t *testing.T) {
		initTempDir(t)
		argLog := recordingGH(t)

		res := runInitFlow(initChoiceGHCreate, git.GitignoreTemplate{}, "", "acme/tool", true, false)
		if res.err != nil {
			t.Fatalf("runInitFlow: %v", res.err)
		}
		argv := strings.Join(readLines(t, argLog), " ")
		if strings.Contains(argv, "--push") {
			t.Fatalf("argv = %q, want no --push on a repo with no commits", argv)
		}
		if !strings.Contains(argv, "--private") {
			t.Fatalf("argv = %q, want --private", argv)
		}
	})

	t.Run("recovery-with-commits-pushes", func(t *testing.T) {
		tempRepo(t, "chore: seed", "")
		argLog := recordingGH(t)

		res := runInitFlow(initChoiceGHCreate, git.GitignoreTemplate{}, "", "acme/tool", false, true)
		if res.err != nil {
			t.Fatalf("runInitFlow: %v", res.err)
		}
		argv := strings.Join(readLines(t, argLog), " ")
		if !strings.Contains(argv, "--push") {
			t.Fatalf("argv = %q, want --push when the repo has commits", argv)
		}
		if !strings.Contains(res.message, "pushed") {
			t.Fatalf("message = %q, want it to mention the push", res.message)
		}
	})
}

func TestRunInitFlowGHCreateSurfacesFailure(t *testing.T) {
	initTempDir(t)
	failingGH(t)

	res := runInitFlow(initChoiceGHCreate, git.GitignoreTemplate{}, "", "acme/tool", false, false)
	if res.err == nil {
		t.Fatal("a failing gh was reported as success")
	}
}

// ── Views ──────────────────────────────────────────────

// Every init phase must render without panicking — a nil textinput or an
// out-of-range template index here takes the whole app down on first paint.
func TestEveryInitPhaseRenders(t *testing.T) {
	phases := []struct {
		name  string
		phase initPhase
	}{
		{"pick-option", initPhasePickOption},
		{"pick-template", initPhasePickTemplate},
		{"input-url", initPhaseInputURL},
		{"input-repo-name", initPhaseInputRepoName},
		{"pick-visibility", initPhasePickVisibility},
		{"confirm-gh-auth", initPhaseConfirmGHAuth},
		{"working", initPhaseWorking},
	}
	for _, tc := range phases {
		t.Run(tc.name, func(t *testing.T) {
			m := initModel(t)
			m.initPhase = tc.phase
			out := m.View()
			if strings.TrimSpace(out) == "" {
				t.Fatal("rendered nothing")
			}
		})
	}
}

// The working screen owns its frame: a stale error from a previous attempt
// must not be drawn under the spinner, but the force-quit warning — which only
// ever exists during an in-flight operation — must be.
func TestInitWorkingScreenHidesStaleErrorsButNotTheForceQuitWarning(t *testing.T) {
	m := initModel(t)
	m.initPhase = initPhaseWorking
	m.initWorking = true
	m.err = fmt.Errorf("stale-marker-xyz")

	if strings.Contains(m.View(), "stale-marker-xyz") {
		t.Fatal("a stale error is drawn over the working screen")
	}

	m.forceQuitArmed = true
	if !strings.Contains(m.View(), "stale-marker-xyz") {
		t.Fatal("the force-quit warning is hidden on the only screen that can raise it")
	}
}

// ── gh stubs ───────────────────────────────────────────

// recordingGH installs a `gh` that logs its argv and succeeds.
func recordingGH(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	argLog := filepath.Join(binDir, "argv")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" >> '%s'\nexit 0\n", argLog)
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argLog
}

func failingGH(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\necho 'HTTP 403' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Split(strings.TrimRight(string(content), "\n"), "\n")
}

var _ tea.Model = Model{}
