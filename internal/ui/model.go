package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"git-assist/internal/git"
	"git-assist/internal/types"
)

// fetchDebounce is the minimum interval between background fetches when
// returning to the menu. Startup always fetches regardless.
const fetchDebounce = 30 * time.Second

// step represents which screen we're on.
type step int

const (
	stepMenu    step = iota // main menu hub
	stepFiles               // file selection
	stepBranch              // branch manager
	stepConfig              // config editor
	stepType                // commit type picker
	stepCustom              // custom type input
	stepMessage             // commit message input (includes inline scope)
	stepConfirm             // commit confirmation
	stepPush                // branch picker + push
	stepDone                // success screen
	stepSync                // sync dialog (pull current / merge origin/main)
)

// Async result messages
type commitResultMsg struct{ err error }
type pushResultMsg struct{ err error }
type undoResultMsg struct {
	err   error
	files []types.FileEntry
}
type saveResultMsg struct {
	err   error
	files []types.FileEntry
	diff  string
}
type branchSwitchResultMsg struct {
	err           error
	newBranch     string
	stashConflict bool
}
type branchCreateResultMsg struct {
	err       error
	newBranch string
}
type branchDeleteResultMsg struct{ err error }
type branchMergeResultMsg struct {
	err           error
	conflictFiles []string
}
type fetchResultMsg struct{ err error }
type pullResultMsg struct {
	err           error
	conflictFiles []string
	// kind distinguishes which operation produced this message, so the
	// handler can craft a specific error ("pull conflict" vs "sync conflict").
	kind pullKind
}

type pullKind int

const (
	pullKindCurrent pullKind = iota // pulled origin/<current> into current
	pullKindMain                    // merged origin/main into current
)

// Model is the main Bubble Tea model.
type Model struct {
	// Current wizard step
	step step

	// Step 1 — file selection
	files           []types.FileEntry
	cursor          int
	fileScroll      int
	branch          string
	gitignoreMode   bool
	existingIgnored []string
	removeIgnored   map[string]bool

	// Step 2 — type picker
	typeIdx    int
	commitType string
	breaking   bool

	// Step 2b — custom type input
	customInput textinput.Model

	// Step 2c — scope input
	scopeInput textinput.Model
	scope      string

	// Step 3 — commit message
	msgInput    textinput.Model
	bodyInput   textarea.Model
	showBody    bool
	bodyFocused bool

	// Diff preview
	showDiff    bool
	diffContent string
	diffFile    string
	diffScroll  int

	// Edit mode
	editMode    bool
	editArea    textarea.Model
	editDirty   bool
	saving      bool
	confirmExit bool

	// Filter mode (file search)
	filterMode    bool
	filterInput   textinput.Model
	filterMatches []int
	filterCursor  int

	// Branch manager
	branchEntries     []types.BranchEntry
	branchCursor      int
	branchScroll      int
	branchCreateMode  bool
	branchCreateInput textinput.Model
	branchDeleteMode  bool
	branchMergeMode   bool
	branchConflict    bool
	branchConflFiles  []string
	branchStandalone  bool
	branchSwitching    bool
	branchMerging      bool
	branchCreatedHint  string
	branchMergePending string
	mergeSource        string
	mergeTarget        string
	mergeTargetMode    bool
	mergeTargets       []types.BranchEntry
	mergeTargetCursor  int

	// Config editor
	configCursor     int
	configEditMode   bool
	configEditInput  textinput.Model
	configGlobal     bool
	configItems      []configItem
	configPickMode   bool
	configPickItems  []string
	configPickCursor int

	// Undo confirmation
	confirmUndo bool

	// Step 4 — push
	branches   []string
	branchIdx  int
	hasRemote  bool
	pushBranch string

	// Gitignore — paths that need git rm --cached during commit
	gitignoreCached []string

	// State flags
	committing bool
	pushing    bool
	pushed     bool

	// Spinner for async operations
	spinner spinner.Model

	// Error (shown on current step, cleared on next keypress)
	err error

	// Main menu
	menuCursor int

	// Commit graph
	localGraph   string
	aheadBehind  string
	behindMain   int
	behindOrigin int

	// Background fetch
	fetching  bool
	lastFetch time.Time

	// Sync dialog
	syncReturnStep     step     // where to return after the dialog closes
	syncPullCurrent    bool     // current branch is behind its origin tracking ref
	syncSyncMain       bool     // current branch is behind origin/main (off when on main)
	syncIncomingCurr   []string // commit subjects coming in from origin/<current>
	syncIncomingMain   []string // commit subjects coming in from origin/main
	syncDialogShown    bool     // suppress auto-show after the first startup prompt
	pulling            bool     // pull in progress (blocks dialog input)
	pullingKind        pullKind // which operation is running
	syncMainBranchName string   // resolved main branch name (main or master)

	// Terminal dimensions
	width    int
	height   int
	quitting bool
}

// NewModel creates the initial model.
func NewModel(files []types.FileEntry, branch string) Model {
	mi := textinput.New()
	mi.Placeholder = "Describe your changes..."
	mi.CharLimit = 200
	mi.Width = 50

	ci := textinput.New()
	ci.Placeholder = "Enter custom type..."
	ci.CharLimit = 20
	ci.Width = 30

	si := textinput.New()
	si.Placeholder = "e.g. auth, api, ui (empty to skip)"
	si.CharLimit = 30
	si.Width = 40

	bi := textarea.New()
	bi.Placeholder = "Optional detailed description..."
	bi.SetWidth(50)
	bi.SetHeight(4)
	bi.CharLimit = 500

	ei := textarea.New()
	ei.Placeholder = ""
	ei.SetWidth(60)
	ei.SetHeight(20)
	ei.CharLimit = 0

	fi := textinput.New()
	fi.Placeholder = "Type to filter files..."
	fi.CharLimit = 100
	fi.Width = 40

	bci := textinput.New()
	bci.Placeholder = "new-branch-name"
	bci.CharLimit = 100
	bci.Width = 40

	cfi := textinput.New()
	cfi.Placeholder = "Enter value..."
	cfi.CharLimit = 200
	cfi.Width = 50

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED"))

	m := Model{
		step:              stepMenu,
		branchCreateInput: bci,
		configEditInput:   cfi,
		files:       files,
		branch:      branch,
		msgInput:    mi,
		customInput: ci,
		scopeInput:  si,
		bodyInput:   bi,
		editArea:    ei,
		filterInput: fi,
		spinner:     s,
		hasRemote:   git.HasRemote(),
	}
	m.RefreshGraphs()
	// Show the spinner on first render if we're going to fetch immediately.
	if m.hasRemote {
		m.fetching = true
	}
	return m
}

// doFetch runs git fetch in the background and returns the result as a
// fetchResultMsg. Failures are surfaced but handled silently by the caller.
func doFetch() tea.Cmd {
	return func() tea.Msg {
		err := git.Fetch()
		return fetchResultMsg{err: err}
	}
}

// maybeFetch returns a fetch command if hasRemote and the debounce window
// has elapsed. Sets m.fetching so the spinner shows immediately. If a fetch
// is already in progress, resumes spinner ticks without starting a second one.
func (m *Model) maybeFetch() tea.Cmd {
	if !m.hasRemote {
		return nil
	}
	if m.fetching {
		return m.spinner.Tick
	}
	if !m.lastFetch.IsZero() && time.Since(m.lastFetch) < fetchDebounce {
		return nil
	}
	m.fetching = true
	return tea.Batch(doFetch(), m.spinner.Tick)
}

// NewBranchModel creates a model that starts in branch manager mode.
func NewBranchModel(branch string) Model {
	m := NewModel(nil, branch)
	m.step = stepBranch
	m.branchStandalone = true
	m.branchEntries = git.GetAllBranches()
	m.localGraph = git.GetUnifiedGraph(15)
	a, b := git.GetAheadBehind(branch)
	m.aheadBehind = formatAheadBehind(a, b)
	return m
}

// RefreshGraphs updates the graph data from git.
func (m *Model) RefreshGraphs() {
	m.localGraph = git.GetUnifiedGraph(15)
	a, b := git.GetAheadBehind(m.branch)
	m.aheadBehind = formatAheadBehind(a, b)
	m.behindOrigin = b
	m.behindMain = git.GetBehindMain(m.branch)
}

// commitPrefix builds the conventional commit prefix: type(scope)!
func (m Model) commitPrefix() string {
	prefix := m.commitType
	if m.scope != "" {
		prefix += "(" + m.scope + ")"
	}
	if m.breaking {
		prefix += "!"
	}
	return prefix
}

func (m Model) Init() tea.Cmd {
	if m.hasRemote {
		return tea.Batch(doFetch(), m.spinner.Tick)
	}
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		inputWidth := msg.Width - 16
		if inputWidth < 30 {
			inputWidth = 30
		}
		if inputWidth > 80 {
			inputWidth = 80
		}
		m.msgInput.Width = inputWidth
		m.bodyInput.SetWidth(inputWidth)
		m.customInput.Width = min(inputWidth, 40)
		m.scopeInput.Width = min(inputWidth, 50)
		m.filterInput.Width = min(inputWidth, 60)
		m.branchCreateInput.Width = min(inputWidth, 50)
		m.configEditInput.Width = min(inputWidth, 50)
		return m, nil

	case undoResultMsg:
		m.confirmUndo = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.files = msg.files
		m.cursor = 0
		return m, nil

	case saveResultMsg:
		m.saving = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// Carry over selections
		prevSelected := make(map[string]bool)
		for _, f := range m.files {
			if f.Selected {
				prevSelected[f.Path] = true
			}
		}
		for i, f := range msg.files {
			if prevSelected[f.Path] {
				msg.files[i].Selected = true
			}
		}
		m.files = msg.files
		m.diffContent = msg.diff
		m.editDirty = false
		m.editMode = false
		return m, nil

	case gitignoreResultMsg:
		m.gitignoreMode = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// Remember which files were commit-selected
		prevSelected := make(map[string]bool)
		for _, f := range m.files {
			if f.Selected {
				prevSelected[f.Path] = true
			}
		}
		// Refresh file list from git status
		freshFiles, err := git.GetStatus()
		if err != nil {
			m.err = err
			return m, nil
		}
		// Carry over commit selections for files that remain
		for i, f := range freshFiles {
			if prevSelected[f.Path] {
				freshFiles[i].Selected = true
			}
		}
		m.files = freshFiles
		m.cursor = 0
		return m, nil

	case commitResultMsg:
		m.committing = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.RefreshGraphs()
		if m.hasRemote {
			m.branches = git.GetBranches(m.branch)
			m.step = stepPush
		} else {
			m.step = stepDone
		}
		return m, nil

	case pushResultMsg:
		m.pushing = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.pushed = true
		m.RefreshGraphs()
		m.step = stepDone
		return m, nil

	case branchSwitchResultMsg:
		m.branchSwitching = false
		if msg.err != nil {
			m.branchMergePending = ""
			m.err = msg.err
			return m, nil
		}
		m.branch = msg.newBranch
		m.branchEntries = git.GetAllBranches()
		m.branchCursor = 0
		m.branchScroll = 0
		m.RefreshGraphs()
		if msg.stashConflict {
			m.branchMergePending = ""
			m.err = fmt.Errorf("switched to %s — changes saved in stash (conflicts). Switch back and run: git stash pop", msg.newBranch)
			return m, nil
		}
		// If a merge was pending (target picker flow), start it now
		if m.branchMergePending != "" {
			source := m.branchMergePending
			m.branchMergePending = ""
			m.branchMerging = true
			return m, tea.Batch(doMergeBranch(source), m.spinner.Tick)
		}
		return m, nil

	case branchCreateResultMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.branch = msg.newBranch
		m.branchEntries = git.GetAllBranches()
		m.branchCursor = 0
		m.branchScroll = 0
		m.branchCreateMode = false
		m.branchCreateInput.Reset()
		m.branchCreatedHint = msg.newBranch
		return m, nil

	case branchDeleteResultMsg:
		m.branchDeleteMode = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.branchEntries = git.GetAllBranches()
		if m.branchCursor >= len(m.branchEntries) {
			m.branchCursor = max(0, len(m.branchEntries)-1)
		}
		return m, nil

	case fetchResultMsg:
		m.fetching = false
		m.lastFetch = time.Now()
		// Errors are intentionally swallowed — this is a background op the
		// user didn't ask for, so failures (offline, auth, etc.) must not
		// surface as alarming banners. Stale ahead/behind numbers are the
		// only observable consequence.
		m.RefreshGraphs()
		// Auto-show the sync dialog once per session on the first successful
		// fetch, if we're on the menu (startup) and something is out of sync.
		// Later fetches (on return to menu) silently update indicators only.
		if !m.syncDialogShown && m.step == stepMenu && msg.err == nil {
			m.syncDialogShown = true
			if m.populateSyncDialog() {
				m.syncReturnStep = stepMenu
				m.step = stepSync
			}
		}
		return m, nil

	case pullResultMsg:
		m.pulling = false
		if msg.err != nil {
			// Conflict → abort cleanly, route user to Branch Manager.
			if len(msg.conflictFiles) > 0 {
				git.MergeAbort()
				verb := "pull"
				if msg.kind == pullKindMain {
					verb = "sync with " + m.syncMainBranchName
				}
				m.err = fmt.Errorf("%s conflict — resolve in Branch Manager", verb)
			} else {
				m.err = msg.err
			}
			return m.exitSyncDialog(), nil
		}
		m.RefreshGraphs()
		return m.exitSyncDialog(), nil

	case branchMergeResultMsg:
		m.branchMerging = false
		m.branchMergeMode = false
		if msg.err != nil {
			conflicts := msg.conflictFiles
			if len(conflicts) > 0 {
				if m.step == stepMenu {
					// Syncing from menu — abort and show error
					git.MergeAbort()
					m.err = fmt.Errorf("merge conflicts with main — use Branch Manager to resolve")
				} else {
					m.branchConflict = true
					m.branchConflFiles = conflicts
				}
			} else {
				m.err = msg.err
			}
			return m, nil
		}
		m.branchEntries = git.GetAllBranches()
		m.RefreshGraphs()
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		// Clear error on any keypress
		m.err = nil
	}

	// Route to the active step handler
	switch m.step {
	case stepMenu:
		return m.updateMenu(msg)
	case stepFiles:
		return m.updateFiles(msg)
	case stepBranch:
		return m.updateBranch(msg)
	case stepConfig:
		return m.updateConfig(msg)
	case stepType:
		return m.updateType(msg)
	case stepCustom:
		return m.updateCustom(msg)
	case stepMessage:
		return m.updateMessage(msg)
	case stepConfirm:
		return m.updateConfirm(msg)
	case stepPush:
		return m.updatePush(msg)
	case stepDone:
		return m.updateDone(msg)
	case stepSync:
		return m.updateSync(msg)
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var content string
	switch m.step {
	case stepMenu:
		content = m.viewMenu()
	case stepFiles:
		content = m.viewFiles()
	case stepBranch:
		content = m.viewBranch()
	case stepConfig:
		content = m.viewConfig()
	case stepType:
		content = m.viewType()
	case stepCustom:
		content = m.viewCustom()
	case stepMessage:
		content = m.viewMessage()
	case stepConfirm:
		content = m.viewConfirm()
	case stepPush:
		content = m.viewPush()
	case stepDone:
		content = m.viewDone()
	case stepSync:
		content = m.viewSync()
	}

	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, content)
}
