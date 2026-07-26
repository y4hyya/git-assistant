package ui

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"git-assist/internal/git"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// initPhase tracks which screen inside the init flow we're on. A single step
// that delegates to phases keeps model.go dispatch simple (one case stepInit).
type initPhase int

const (
	initPhasePickOption     initPhase = iota // 4-option radio list
	initPhasePickTemplate                    // .gitignore template picker
	initPhaseInputURL                        // paste remote URL
	initPhaseInputRepoName                   // repo name for gh create
	initPhasePickVisibility                  // public / private
	initPhaseConfirmGHAuth                   // offer to run gh auth login
	initPhaseWorking                         // async op in flight
	initPhaseDone                            // success → fall through to menu
)

// initChoice enumerates the 4 top-level options the user sees first.
type initChoice int

const (
	initChoiceLocal    initChoice = iota // A: local init only
	initChoiceConnect                    // B: init + connect to existing URL
	initChoiceGHCreate                   // C: init + `gh repo create`
	initChoiceCancel                     // D: exit
)

var initChoiceLabels = []struct {
	name string
	desc string
}{
	{"Initialize local repo", "git init, nothing else"},
	{"Connect to GitHub repo", "git init + add existing remote URL"},
	{"Create new GitHub repo", "git init + gh repo create + push"},
	{"Cancel", "quit without changes"},
}

// initVisibilityLabels drives the public/private picker.
var initVisibilityLabels = []string{"Public", "Private"}

// Async result messages specific to the init flow.
type initResultMsg struct {
	err     error
	branch  string
	message string // success banner shown on menu
	// warning describes a setup that succeeded but is headed somewhere bad
	// (an unrelated-histories remote). Rendered sticky, unlike message, which
	// any keypress clears — this is the one explanation for why the next push
	// will fail, and it must survive a stray arrow key.
	warning string
}

type ghAuthResultMsg struct{ err error }

// newInitModelFields initializes all fields the init flow needs. Called from
// NewModel when the working directory is not a git repo.
func (m *Model) setupInitModel() {
	urlInput := textinput.New()
	urlInput.Placeholder = "git@github.com:user/repo.git"
	urlInput.CharLimit = 300
	urlInput.Width = 50

	nameInput := textinput.New()
	nameInput.Placeholder = git.CurrentDirName()
	nameInput.SetValue(git.CurrentDirName())
	nameInput.CharLimit = 100
	nameInput.Width = 50

	m.initURLInput = urlInput
	m.initNameInput = nameInput
	m.initTemplateOptions = git.GitignoreTemplates()
	// One detection pass, its result kept. Both the preselected cursor and the
	// "(detected: Go)" label are the same answer, and the label used to be
	// recomputed by viewInitPickTemplate — an os.ReadDir of the working
	// directory on every keypress, resize and spinner tick.
	m.initDetectedTemplate = git.DetectGitignoreTemplate()
	m.initTemplateCursor = indexOfTemplate(m.initTemplateOptions, m.initDetectedTemplate)
}

func indexOfTemplate(tpls []git.GitignoreTemplate, name string) int {
	for i, t := range tpls {
		if t.Name == name {
			return i
		}
	}
	return 0
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateInit(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks during async work.
	if m.initPhase == initPhaseWorking {
		switch msg := msg.(type) {
		case initResultMsg:
			m.initWorking = false
			m.clearForceQuitPrompt()
			if msg.err != nil {
				m.err = msg.err
				m.initPhase = initPhasePickOption
				return m, nil
			}
			// Success — refresh model from new git state and land on menu.
			m.branch = msg.branch
			m.hasRemote = git.HasRemote()
			m.initPhase = initPhasePickOption
			m.ghReuseMode = false
			cmd := m.returnToMenu()
			m.initSuccessMsg = msg.message
			if msg.warning != "" {
				m.err = recoveryError{errors.New(msg.warning)}
			}
			return m, cmd
		case ghAuthResultMsg:
			m.initWorking = false
			m.clearForceQuitPrompt()
			// `gh auth login` can exit 0 after the user bails out in the
			// browser, so trust the session check over the process status.
			authed := git.IsGHAuthed()
			if !authed {
				if msg.err != nil {
					m.err = fmt.Errorf("gh auth failed: %v", msg.err)
				} else {
					m.err = fmt.Errorf("gh is still not authenticated — run `gh auth login` in your terminal")
				}
				m.initPhase = initPhasePickOption
				if m.ghReuseMode {
					// Reuse mode came from the menu of an already-initialized
					// repo; the first-run picker has nothing to offer there.
					m.ghReuseMode = false
					cmd := m.returnToMenu()
					return m, cmd
				}
				return m, nil
			}
			if m.ghReuseMode {
				// Existing repo — resume exactly where the auth check
				// interrupted us: naming the GitHub repo to create.
				m.initNameInput.Focus()
				m.initNameInput.CursorEnd()
				m.initPhase = initPhaseInputRepoName
				return m, nil
			}
			m.initPhase = initPhasePickTemplate
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch m.initPhase {
	case initPhasePickOption:
		return m.updateInitPickOption(keyMsg)
	case initPhasePickTemplate:
		return m.updateInitPickTemplate(keyMsg)
	case initPhaseInputURL:
		return m.updateInitInputURL(keyMsg)
	case initPhaseInputRepoName:
		return m.updateInitInputRepoName(keyMsg)
	case initPhasePickVisibility:
		return m.updateInitPickVisibility(keyMsg)
	case initPhaseConfirmGHAuth:
		return m.updateInitConfirmGHAuth(keyMsg)
	}
	return m, nil
}

func (m Model) updateInitPickOption(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.initCursor > 0 {
			m.initCursor--
		}
	case "down", "j":
		if m.initCursor < len(initChoiceLabels)-1 {
			m.initCursor++
		}
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "enter":
		switch initChoice(m.initCursor) {
		case initChoiceCancel:
			m.quitting = true
			return m, tea.Quit
		case initChoiceLocal:
			m.initPhase = initPhasePickTemplate
			return m, nil
		case initChoiceConnect:
			m.initPhase = initPhasePickTemplate
			return m, nil
		case initChoiceGHCreate:
			// Check gh availability up-front so we can offer `gh auth login`
			// before the user commits to the flow.
			if !git.HasGHCLI() {
				m.err = fmt.Errorf("gh CLI not installed — see https://cli.github.com")
				return m, nil
			}
			if !git.IsGHAuthed() {
				m.initPhase = initPhaseConfirmGHAuth
				return m, nil
			}
			m.initPhase = initPhasePickTemplate
			return m, nil
		}
	}
	return m, nil
}

func (m Model) updateInitPickTemplate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.initTemplateCursor > 0 {
			m.initTemplateCursor--
		}
	case "down", "j":
		if m.initTemplateCursor < len(m.initTemplateOptions)-1 {
			m.initTemplateCursor++
		}
	case "q":
		// This is a picker, not a text input — q quits here exactly like it
		// does on the option list one screen back.
		m.quitting = true
		return m, tea.Quit
	case "esc":
		m.initPhase = initPhasePickOption
	case "enter":
		// Advance to the choice-specific follow-up.
		switch initChoice(m.initCursor) {
		case initChoiceLocal:
			return m.startInitWork()
		case initChoiceConnect:
			m.initURLInput.Focus()
			m.initPhase = initPhaseInputURL
		case initChoiceGHCreate:
			m.initNameInput.Focus()
			m.initNameInput.CursorEnd()
			m.initPhase = initPhaseInputRepoName
		}
	}
	return m, nil
}

func (m Model) updateInitInputURL(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.initURLInput.Blur()
		m.initPhase = initPhasePickTemplate
		return m, nil
	case "enter":
		url := strings.TrimSpace(m.initURLInput.Value())
		if url == "" {
			m.err = fmt.Errorf("remote URL is required")
			return m, nil
		}
		if !isValidGitURL(url) {
			m.err = fmt.Errorf("invalid URL — expected https://…, [user@]host:path, ssh://…, file://…, or a local path")
			return m, nil
		}
		m.initRemoteURL = url
		m.initURLInput.Blur()
		return m.startInitWork()
	}
	var cmd tea.Cmd
	m.initURLInput, cmd = m.initURLInput.Update(msg)
	return m, cmd
}

func (m Model) updateInitInputRepoName(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.initNameInput.Blur()
		if m.ghReuseMode {
			// Recovery flow skips the template phase; bail to menu.
			m.ghReuseMode = false
			m.initPhase = initPhasePickOption
			cmd := m.returnToMenu()
			return m, cmd
		}
		m.initPhase = initPhasePickTemplate
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.initNameInput.Value())
		if name == "" {
			m.err = fmt.Errorf("repo name is required")
			return m, nil
		}
		if !isValidGitHubRepoName(name) {
			m.err = fmt.Errorf("invalid name — use letters, digits, '-', '_', '.', optionally prefixed with 'owner/'")
			return m, nil
		}
		m.initRepoName = name
		m.initNameInput.Blur()
		m.initVisibilityCursor = 0
		m.initPhase = initPhasePickVisibility
		return m, nil
	}
	var cmd tea.Cmd
	m.initNameInput, cmd = m.initNameInput.Update(msg)
	return m, cmd
}

// isValidGitURL accepts the URL forms `git remote add` understands. Accepted:
//
//   - scheme URLs — https://, http://, ssh://, git://, git+ssh://, ftp://,
//     ftps://, file:// — with at least one character of host/path after the
//     scheme ("https://" alone is a typo, not a remote)
//   - scp-style [user@]host:path. The user@ part is optional: git's syntax
//     doesn't require it, and on a self-hosted host where the ssh user
//     already matches, "host:path" is the normal way to write it
//   - local paths: absolute (/srv/repo.git) and explicitly relative
//     (./repo.git, ../other-repo) — both valid remotes
//
// Rejected: anything containing whitespace, a bare word with neither scheme,
// ':' nor path prefix ("origin", "repo.git"), a scheme with nothing after it,
// and Windows drive paths ("C:\repo"), which only look like scp-style.
func isValidGitURL(url string) bool {
	if url == "" || strings.ContainsAny(url, " \t\n\r") {
		return false
	}
	for _, p := range []string{"https://", "http://", "ssh://", "git://", "git+ssh://", "ftp://", "ftps://", "file://"} {
		if strings.HasPrefix(url, p) {
			return len(url) > len(p)
		}
	}
	if strings.HasPrefix(url, "/") || strings.HasPrefix(url, "./") || strings.HasPrefix(url, "../") {
		return len(url) > 1
	}
	// scp-style: everything before the first ':' is [user@]host.
	if i := strings.Index(url, ":"); i > 0 {
		host, path := url[:i], url[i+1:]
		if at := strings.Index(host, "@"); at >= 0 {
			host = host[at+1:]
		}
		// A one-letter "host" followed by a path separator is a drive letter.
		if len(host) == 1 && (strings.HasPrefix(path, `\`) || strings.HasPrefix(path, "/")) {
			return false
		}
		return host != "" && path != ""
	}
	return false
}

// isValidGitHubRepoName mirrors GitHub's repo naming rules: alphanumerics,
// dashes, underscores, dots; cannot start with `-` or `.`; max 100 chars per
// segment. An optional `owner/` prefix is allowed for org repos. We validate
// upfront so users see a clear inline error instead of a `gh repo create`
// failure several seconds later.
func isValidGitHubRepoName(name string) bool {
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		if !isValidGitHubNameSegment(parts[0]) {
			return false
		}
		return isValidGitHubNameSegment(parts[1])
	}
	return isValidGitHubNameSegment(parts[0])
}

func isValidGitHubNameSegment(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for i, c := range s {
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !alnum && c != '-' && c != '_' && c != '.' {
			return false
		}
		if i == 0 && (c == '-' || c == '.') {
			return false
		}
	}
	return true
}

func (m Model) updateInitPickVisibility(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.initVisibilityCursor > 0 {
			m.initVisibilityCursor--
		}
	case "down", "j":
		if m.initVisibilityCursor < len(initVisibilityLabels)-1 {
			m.initVisibilityCursor++
		}
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		m.initPhase = initPhaseInputRepoName
		m.initNameInput.Focus()
	case "enter":
		m.initPrivate = m.initVisibilityCursor == 1
		return m.startInitWork()
	}
	return m, nil
}

func (m Model) updateInitConfirmGHAuth(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		// Shell out to `gh auth login --web`. This takes over the terminal,
		// so tea.ExecProcess is required — it suspends the TUI, runs the
		// interactive command, then restores rendering.
		cmdName, args := git.GHAuthLoginCmd()
		c := exec.Command(cmdName, args...)
		m.initWorking = true
		m.initPhase = initPhaseWorking
		return m, tea.ExecProcess(c, func(err error) tea.Msg {
			return ghAuthResultMsg{err: err}
		})
	case "n", "esc":
		if m.ghReuseMode {
			m.ghReuseMode = false
			m.initPhase = initPhasePickOption
			cmd := m.returnToMenu()
			return m, cmd
		}
		m.initPhase = initPhasePickOption
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// startInitWork kicks off the async operation for the currently selected
// initChoice. Returns the (model, cmd) pair to hand back to Bubble Tea.
func (m Model) startInitWork() (tea.Model, tea.Cmd) {
	m.initWorking = true
	m.initPhase = initPhaseWorking
	m.err = nil

	choice := initChoice(m.initCursor)
	tpl := m.initTemplateOptions[m.initTemplateCursor]
	url := m.initRemoteURL
	repoName := m.initRepoName
	private := m.initPrivate
	reuse := m.ghReuseMode

	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		return runInitFlow(choice, tpl, url, repoName, private, reuse)
	})
}

// runInitFlow performs the chosen initialization sequence. Runs on a
// goroutine (via tea.Cmd), so it must not touch the Model.
//
// When reuseExisting is true, we skip `git init` and the .gitignore
// template — the repo already exists and the user just wants to wire up a
// remote (recovery path from the menu).
func runInitFlow(choice initChoice, tpl git.GitignoreTemplate, url, repoName string, private, reuseExisting bool) initResultMsg {
	const defaultBranch = "main"
	branch := defaultBranch

	if !reuseExisting {
		if err := git.InitRepo(defaultBranch); err != nil {
			return initResultMsg{err: err}
		}
		// Older git versions may ignore `-b` silently. Force the branch name so
		// the initial state matches our expectation even before the first commit.
		_ = git.RenameBranch(defaultBranch)

		if tpl.Content != "" {
			if err := git.WriteGitignoreTemplate(tpl.Content); err != nil {
				return initResultMsg{err: fmt.Errorf("write .gitignore: %w", err)}
			}
		}
	} else {
		// Use whichever branch is actually checked out.
		if b, err := git.GetCurrentBranch(); err == nil && b != "" {
			branch = b
		}
	}

	switch choice {
	case initChoiceLocal:
		return initResultMsg{
			branch:  branch,
			message: "Initialized empty repo — stage your first commit from the Files screen.",
		}

	case initChoiceConnect:
		if err := git.AddOriginRemote(url); err != nil {
			return initResultMsg{err: err}
		}
		// A remote that already has commits is a guaranteed dead end for this
		// flow: the first local commit becomes a second root, and every later
		// push and pull is rejected with "refusing to merge unrelated
		// histories" — a message no screen in this app can act on. Fetch once
		// and say so now, while the user can still pick a different remote.
		// The remote stays configured either way; they may know better.
		warning := ""
		if fetchErr := git.Fetch(); fetchErr == nil {
			// Wording note: no "rejected" / "non-fast-forward" in this text.
			// formatError pattern-matches those and appends "Remote has newer
			// changes. Run git pull first." — the exact doomed advice this
			// warning exists to head off.
			if ref, unrelated := git.UnrelatedHistories(); unrelated {
				if git.HasAnyCommit() {
					warning = fmt.Sprintf("this repository and %s have unrelated histories — git will refuse every push and pull between them. Connect an empty remote, or clone the remote instead.", ref)
				} else {
					warning = fmt.Sprintf("%s already has commits and this repository has none — your first commit here would start an unrelated history that git refuses to push or pull. Clone the remote instead, or connect an empty one.", ref)
				}
			}
		}
		return initResultMsg{
			branch:  branch,
			message: fmt.Sprintf("Connected to %s — commit then push from the menu.", url),
			warning: warning,
		}

	case initChoiceGHCreate:
		// Create the GitHub repo and wire origin in one shot. Push only
		// when we already have commits (reuseExisting recovery flow). For
		// the fresh-init case, no push — user picks their first commit.
		push := reuseExisting && git.HasAnyCommit()
		if err := git.GHCreateRepo(repoName, private, push); err != nil {
			return initResultMsg{err: err}
		}
		msg := fmt.Sprintf("Created GitHub repo %q — commit and push from the menu.", repoName)
		if push {
			msg = fmt.Sprintf("Created GitHub repo %q and pushed %s.", repoName, branch)
		}
		return initResultMsg{
			branch:  branch,
			message: msg,
		}
	}

	return initResultMsg{branch: branch}
}

// ── View ────────────────────────────────────────────────

func (m Model) viewInit() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(dimStyle.Render("not a git repository — let's set one up"))
	b.WriteString("\n\n")

	switch m.initPhase {
	case initPhasePickOption:
		b.WriteString(m.viewInitPickOption())
	case initPhasePickTemplate:
		b.WriteString(m.viewInitPickTemplate())
	case initPhaseInputURL:
		b.WriteString(m.viewInitInputURL())
	case initPhaseInputRepoName:
		b.WriteString(m.viewInitInputRepoName())
	case initPhasePickVisibility:
		b.WriteString(m.viewInitPickVisibility())
	case initPhaseConfirmGHAuth:
		b.WriteString(m.viewInitConfirmGHAuth())
	case initPhaseWorking:
		b.WriteString(m.viewInitWorking())
	}

	// Errors stay hidden while work is in flight — the working screen owns
	// that frame — except the force-quit warning, which is only ever raised
	// during an in-flight operation and must be visible exactly then.
	if m.err != nil && (m.initPhase != initPhaseWorking || m.forceQuitArmed) {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	return m.styledBox(b.String())
}

func (m Model) viewInitPickOption() string {
	var b strings.Builder
	b.WriteString(highlightStyle.Render("How would you like to start?"))
	b.WriteString("\n\n")

	for i, item := range initChoiceLabels {
		cursor := "  "
		name := inactiveStyle.Render(item.name)
		desc := dimStyle.Render(item.desc)
		if i == m.initCursor {
			cursor = cursorStyle.Render(symCursor + " ")
			name = highlightStyle.Render(item.name)
		}
		b.WriteString(fmt.Sprintf("%s%-28s %s\n", cursor, name, desc))
	}

	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return b.String()
}

func (m Model) viewInitPickTemplate() string {
	var b strings.Builder
	b.WriteString(highlightStyle.Render("Pick a .gitignore template"))
	b.WriteString("  ")
	b.WriteString(dimStyle.Render("(detected: " + m.initDetectedTemplate + ")"))
	b.WriteString("\n\n")

	for i, tpl := range m.initTemplateOptions {
		cursor := "  "
		name := inactiveStyle.Render(tpl.Name)
		if i == m.initTemplateCursor {
			cursor = cursorStyle.Render(symCursor + " ")
			name = highlightStyle.Render(tpl.Name)
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, name))
	}

	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return b.String()
}

func (m Model) viewInitInputURL() string {
	var b strings.Builder
	b.WriteString(highlightStyle.Render("Paste the GitHub repo URL"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  SSH or HTTPS — e.g. git@github.com:user/repo.git"))
	b.WriteString("\n\n  ")
	b.WriteString(m.initURLInput.View())
	b.WriteString("\n\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return b.String()
}

func (m Model) viewInitInputRepoName() string {
	var b strings.Builder
	b.WriteString(highlightStyle.Render("Name the new GitHub repo"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("  use 'owner/name' for an org, or bare name for your account"))
	b.WriteString("\n\n  ")
	b.WriteString(m.initNameInput.View())
	b.WriteString("\n\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return b.String()
}

func (m Model) viewInitPickVisibility() string {
	var b strings.Builder
	b.WriteString(highlightStyle.Render("Visibility"))
	b.WriteString("\n\n")
	for i, v := range initVisibilityLabels {
		cursor := "  "
		name := inactiveStyle.Render(v)
		if i == m.initVisibilityCursor {
			cursor = cursorStyle.Render(symCursor + " ")
			name = highlightStyle.Render(v)
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, name))
	}
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return b.String()
}

func (m Model) viewInitConfirmGHAuth() string {
	var b strings.Builder
	b.WriteString(highlightStyle.Render("GitHub CLI is not authenticated"))
	b.WriteString("\n\n")
	b.WriteString("  Run " + helpKeyStyle.Render("gh auth login --web") + " now?\n")
	b.WriteString(dimStyle.Render("  Opens your browser. We'll resume right after.\n"))
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return b.String()
}

func (m Model) viewInitWorking() string {
	var b strings.Builder
	b.WriteString("  " + m.spinner.View() + " ")
	switch initChoice(m.initCursor) {
	case initChoiceLocal:
		b.WriteString(dimStyle.Render("Initializing repo..."))
	case initChoiceConnect:
		b.WriteString(dimStyle.Render("Initializing and adding remote..."))
	case initChoiceGHCreate:
		b.WriteString(dimStyle.Render("Creating GitHub repo..."))
	}
	return b.String()
}
