package ui

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

// initPhase tracks which screen inside the init flow we're on. A single step
// that delegates to phases keeps model.go dispatch simple (one case stepInit).
type initPhase int

const (
	initPhasePickOption    initPhase = iota // 4-option radio list
	initPhasePickTemplate                   // .gitignore template picker
	initPhaseInputURL                       // paste remote URL
	initPhaseInputRepoName                  // repo name for gh create
	initPhasePickVisibility                 // public / private
	initPhaseConfirmGHAuth                  // offer to run gh auth login
	initPhaseWorking                        // async op in flight
	initPhaseDone                           // success → fall through to menu
)

// initChoice enumerates the 4 top-level options the user sees first.
type initChoice int

const (
	initChoiceLocal   initChoice = iota // A: local init only
	initChoiceConnect                   // B: init + connect to existing URL
	initChoiceGHCreate                  // C: init + `gh repo create`
	initChoiceCancel                    // D: exit
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
	m.initTemplateCursor = indexOfTemplate(m.initTemplateOptions, git.DetectGitignoreTemplate())
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
			if msg.err != nil {
				m.err = msg.err
				m.initPhase = initPhasePickOption
				return m, nil
			}
			// Success — refresh model from new git state and land on menu.
			m.branch = msg.branch
			m.hasRemote = git.HasRemote()
			files, _ := git.GetStatus()
			m.files = files
			m.RefreshGraphs()
			m.step = stepMenu
			m.initPhase = initPhasePickOption
			m.initSuccessMsg = msg.message
			m.ghReuseMode = false
			return m, m.maybeFetch()
		case ghAuthResultMsg:
			m.initWorking = false
			if msg.err != nil {
				m.err = fmt.Errorf("gh auth failed: %v", msg.err)
			}
			m.initPhase = initPhasePickOption
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
			m.step = stepMenu
			m.ghReuseMode = false
			m.initPhase = initPhasePickOption
			return m, nil
		}
		m.initPhase = initPhasePickTemplate
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.initNameInput.Value())
		if name == "" {
			m.err = fmt.Errorf("repo name is required")
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
			m.step = stepMenu
			m.ghReuseMode = false
			m.initPhase = initPhasePickOption
			return m, nil
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
		return initResultMsg{
			branch:  branch,
			message: fmt.Sprintf("Connected to %s — commit then push from the menu.", url),
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

	if m.err != nil && m.initPhase != initPhaseWorking {
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
	b.WriteString(renderHelp([]helpEntry{
		{symArrows, "navigate"},
		{"enter", "select"},
		{"q", "quit"},
	}))
	return b.String()
}

func (m Model) viewInitPickTemplate() string {
	var b strings.Builder
	b.WriteString(highlightStyle.Render("Pick a .gitignore template"))
	b.WriteString("  ")
	b.WriteString(dimStyle.Render("(detected: " + git.DetectGitignoreTemplate() + ")"))
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
	b.WriteString(renderHelp([]helpEntry{
		{symArrows, "navigate"},
		{"enter", "continue"},
		{"esc", "back"},
	}))
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
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "connect"},
		{"esc", "back"},
	}))
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
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "continue"},
		{"esc", "back"},
	}))
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
	b.WriteString(renderHelp([]helpEntry{
		{symArrows, "navigate"},
		{"enter", "create"},
		{"esc", "back"},
	}))
	return b.String()
}

func (m Model) viewInitConfirmGHAuth() string {
	var b strings.Builder
	b.WriteString(highlightStyle.Render("GitHub CLI is not authenticated"))
	b.WriteString("\n\n")
	b.WriteString("  Run " + helpKeyStyle.Render("gh auth login --web") + " now?\n")
	b.WriteString(dimStyle.Render("  Opens your browser. We'll resume right after.\n"))
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"y", "yes"},
		{"n", "back"},
		{"q", "quit"},
	}))
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
