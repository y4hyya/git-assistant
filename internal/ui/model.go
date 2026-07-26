package ui

import (
	"errors"
	"fmt"
	"time"

	"git-assist/internal/git"
	"git-assist/internal/types"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	stepInit                // first-run setup when cwd is not a git repo
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
	stashRef      string // short SHA of the stash entry, surfaced when pop fails
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
	// stashed/stashRef carry the auto-stash the pull took before merging.
	// The handler owns the recovery (it aborts the merge first), so it needs
	// to know whether uncommitted work is parked in the stash and under
	// which ref — otherwise those changes vanish silently.
	stashed  bool
	stashRef string
}

type pullKind int

const (
	pullKindCurrent pullKind = iota // pulled origin/<current> into current
	pullKindMain                    // merged origin/main into current
)

// Input limits for the commit message editors. The amend flow raises both to
// unlimited while pre-filling (SetValue truncates silently past the limit,
// which would rewrite the user's commit with a chopped body) and resetWizard
// puts them back.
const (
	msgCharLimit  = 200
	bodyCharLimit = 500
)

// recoveryError marks an error whose text the user must act on — a stash SHA,
// a `git stash apply` command. The blanket "clear m.err on the next keypress"
// rule would wipe those mid-copy, so Update keeps them on screen until esc or
// enter dismisses them explicitly.
type recoveryError struct{ err error }

func (e recoveryError) Error() string { return e.err.Error() }
func (e recoveryError) Unwrap() error { return e.err }

// isRecoveryError reports whether err carries recovery instructions and must
// therefore survive ordinary keypresses.
func isRecoveryError(err error) bool {
	if err == nil {
		return false
	}
	var re recoveryError
	return errors.As(err, &re)
}

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
	gitignoring     bool // .gitignore apply in flight (blocks re-dispatch)
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
	branchEntries      []types.BranchEntry
	branchCursor       int
	branchScroll       int
	branchCreateMode   bool
	branchCreateInput  textinput.Model
	branchDeleteMode   bool
	branchMergeMode    bool
	branchConflict     bool
	branchConflFiles   []string
	branchStandalone   bool
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
	undoing     bool // soft reset in flight (blocks a second confirmation)
	// undoPushed caches git.IsLastCommitPushed() from the moment the prompt
	// opened. Undoing a pushed commit makes the branch diverge from origin,
	// which is worth warning about — but never from a View func.
	undoPushed bool

	// Step 4 — push
	branches   []string
	branchIdx  int
	hasRemote  bool
	pushBranch string
	// pushCheckDone marks the pre-push behind-origin check as already run for
	// this visit to the push step. Without it the check re-fires on every
	// enter and declining the offered pull makes pushing unreachable.
	pushCheckDone bool

	// Gitignore — paths that need git rm --cached during commit
	gitignoreCached []string

	// State flags
	committing bool
	pushing    bool
	pushed     bool
	amendMode  bool // when true, the commit wizard ends with `git commit --amend`
	// amendRaw is set when the amended commit's subject isn't conventional
	// ("Initial commit", "Merge branch ..."). The type/scope/breaking
	// machinery is skipped entirely and the message is written back verbatim
	// — forcing a "type: " prefix onto those commits rewrites history the
	// user never asked to change.
	amendRaw bool
	// amendStaged is the index-vs-HEAD file list, read once on entering the
	// confirm step. `git commit --amend` commits the WHOLE index, so anything
	// staged outside the wizard rides along and has to be disclosed.
	amendStaged []string

	// Cached: whether the repo has at least one commit. Refreshed by
	// RefreshGraphs so menuItems doesn't fork git per keypress.
	hasAnyCommit bool

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

	// Cached branch count — refreshed by RefreshGraphs so the menu can read
	// it without forking `git branch` on every keypress.
	branchCount int

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

	// Set when ctrl+c was pressed while a mutating operation was in flight.
	// The keypress is swallowed and a warning shown; a second ctrl+c while
	// the flag is set force-quits. Cleared by any other keypress and by
	// every async result handler.
	forceQuitArmed bool

	// Init flow (first-run setup when cwd is not a git repo)
	initPhase            initPhase
	initCursor           int
	initTemplateOptions  []git.GitignoreTemplate
	initTemplateCursor   int
	initURLInput         textinput.Model
	initNameInput        textinput.Model
	initRemoteURL        string
	initRepoName         string
	initPrivate          bool
	initVisibilityCursor int
	initWorking          bool
	initSuccessMsg       string
	ghReuseMode          bool // init flow invoked from menu against existing repo
}

// NewModel creates the initial model.
func NewModel(files []types.FileEntry, branch string) Model {
	mi := textinput.New()
	mi.Placeholder = "Describe your changes..."
	mi.CharLimit = msgCharLimit
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
	bi.CharLimit = bodyCharLimit

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
		files:             files,
		branch:            branch,
		msgInput:          mi,
		customInput:       ci,
		scopeInput:        si,
		bodyInput:         bi,
		editArea:          ei,
		filterInput:       fi,
		spinner:           s,
		hasRemote:         git.HasRemote(),
	}
	// Initialize init-flow inputs up-front so the "Connect to GitHub"
	// recovery entry works from an already-initialized repo. Without this,
	// initNameInput is a zero-value textinput and .Focus() nil-derefs.
	m.setupInitModel()
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

// NewInitModel creates a model that starts in the first-run init flow. Used
// when git-assist is launched from a non-git directory — instead of exiting,
// we guide the user through setup.
func NewInitModel() Model {
	m := NewModel(nil, "")
	m.step = stepInit
	m.initPhase = initPhasePickOption
	m.hasRemote = false
	m.fetching = false // no remote yet, don't spin
	return m
}

// NewBranchModel creates a model that starts in branch manager mode.
func NewBranchModel(branch string) Model {
	m := NewModel(nil, branch)
	m.step = stepBranch
	m.branchStandalone = true
	m.branchEntries = git.GetAllBranches()
	m.localGraph = git.GetUnifiedGraph(15)
	a, b, up := git.GetAheadBehind(branch)
	m.aheadBehind = formatAheadBehind(a, b, up)
	return m
}

// RefreshGraphs updates the graph data from git, plus the cached branch
// count and hasAnyCommit flag so the menu can render without forking
// `git branch` / `git rev-parse` on every keypress.
func (m *Model) RefreshGraphs() {
	m.localGraph = git.GetUnifiedGraph(15)
	a, b, up := git.GetAheadBehind(m.branch)
	m.aheadBehind = formatAheadBehind(a, b, up)
	m.behindOrigin = b
	m.behindMain = git.GetBehindMain(m.branch)
	m.branchCount = len(git.GetAllBranches())
	m.hasAnyCommit = git.HasAnyCommit()
}

// resetWizard clears every field the commit/amend wizard writes, so the next
// run starts blank. It exists because the amend prefill (type, scope, subject,
// body, breaking flag) used to survive an abandoned amend and reappear in the
// next ordinary commit — enter-through would then ship the old commit's
// message. Call it from EVERY path that abandons or completes the wizard.
func (m *Model) resetWizard() {
	m.amendMode = false
	m.amendRaw = false
	m.amendStaged = nil
	m.typeIdx = 0
	m.commitType = ""
	m.scope = ""
	m.breaking = false
	m.showBody = false
	m.bodyFocused = false
	m.pushed = false
	m.pushBranch = ""
	m.branchIdx = 0
	m.pushCheckDone = false
	m.gitignoreCached = nil

	m.msgInput.Reset()
	m.msgInput.Blur()
	// Restore the everyday limits: the amend prefill lifts them so a long
	// commit body round-trips intact.
	m.msgInput.CharLimit = msgCharLimit
	m.bodyInput.Reset()
	m.bodyInput.Blur()
	m.bodyInput.CharLimit = bodyCharLimit
	m.scopeInput.Reset()
	m.scopeInput.Blur()
	m.customInput.Reset()
	m.customInput.Blur()
}

// refreshFiles re-reads git status into m.files, dropping selections (a new
// wizard run always starts fresh). The always-on dashboard would otherwise
// gate "Commit" on a snapshot taken at startup: files changed from an editor
// or another terminal stay invisible, and Commit answers "working tree clean".
func (m *Model) refreshFiles() {
	files, err := git.GetStatus()
	if err != nil {
		return
	}
	m.files = files
	m.cursor = 0
	m.fileScroll = 0
}

// returnToMenu is the single way back to the main menu: it abandons any wizard
// state, re-reads the working tree, and refreshes the graph/counters. Every
// transition to stepMenu goes through here so no path can skip one of the
// three (the esc-from-Files path used to skip all of them).
func (m *Model) returnToMenu() tea.Cmd {
	m.resetWizard()
	m.refreshFiles()
	m.step = stepMenu
	m.RefreshGraphs()
	return m.maybeFetch()
}

// opInFlight reports whether a mutating operation is currently running as a
// detached Bubble Tea command. Quitting mid-sequence can leave the repo in a
// half-finished state — an auto-stash orphaned between `git stash` and
// `git stash pop`, a merge left with MERGE_HEAD, a commit killed mid-write —
// so ctrl+c asks for confirmation while any of these are set.
//
// Background fetch is deliberately excluded: it is read-only and safe to
// abandon, and it runs often enough that guarding it would make ctrl+c feel
// broken.
func (m Model) opInFlight() bool {
	return m.committing ||
		m.pushing ||
		m.saving ||
		m.branchSwitching ||
		m.branchMerging ||
		m.pulling ||
		m.initWorking
}

// clearForceQuitPrompt disarms the force-quit confirmation and drops its
// warning banner. Called from every async result handler: once the operation
// the warning referred to has finished, both are stale. Real errors set by
// those handlers land after this call and are unaffected.
func (m *Model) clearForceQuitPrompt() {
	if m.forceQuitArmed {
		m.forceQuitArmed = false
		m.err = nil
	}
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
		m.undoing = false
		m.undoPushed = false
		m.clearForceQuitPrompt()
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// The commit the wizard was pointed at no longer exists. Staying
		// latched in amendMode would make the next confirm rewrite the
		// PREVIOUS commit with the undone commit's message.
		m.resetWizard()
		m.files = msg.files
		m.cursor = 0
		m.fileScroll = 0
		// Without this the menu graph still shows the undone commit, which
		// reads as "undo failed" and invites a second, destructive undo.
		m.RefreshGraphs()
		return m, nil

	case saveResultMsg:
		m.saving = false
		m.clearForceQuitPrompt()
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
		// Saving a file back to its HEAD content drops it from git status,
		// so the fresh list can be shorter than the cursor (or empty). The
		// next frame renders the diff view, which indexes files[cursor].
		if m.cursor >= len(m.files) {
			m.cursor = max(0, len(m.files)-1)
		}
		if m.fileScroll > m.cursor {
			m.fileScroll = m.cursor
		}
		if len(m.files) == 0 {
			// Nothing left to show a diff for — fall back to the file list.
			m.showDiff = false
			m.diffFile = ""
			m.diffScroll = 0
			m.fileScroll = 0
		}
		m.diffContent = msg.diff
		m.editDirty = false
		m.editMode = false
		return m, nil

	case gitignoreResultMsg:
		m.gitignoreMode = false
		m.gitignoring = false
		m.clearForceQuitPrompt()
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
		m.fileScroll = 0
		return m, nil

	case commitResultMsg:
		m.committing = false
		m.clearForceQuitPrompt()
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.RefreshGraphs()
		// Amends route straight to Done — auto-routing to push after an
		// amend would either fail (non-FF on pushed commits) or surprise
		// the user by force-pushing. The Confirm step already warned
		// about --force-with-lease; the user pushes manually afterward.
		if m.amendMode {
			m.step = stepDone
			return m, nil
		}
		if m.hasRemote {
			m.branches = git.GetBranches(m.branch)
			// The picker selection is per-wizard-run: a stale index from an
			// earlier run would preselect (and push to) the wrong branch, or
			// point past the end of a list that shrank after a prune.
			// GetBranches puts the current branch first.
			m.branchIdx = 0
			// Fresh visit to the push step: the behind-origin check gets one
			// shot, so declining the offered pull still leaves push reachable.
			m.pushCheckDone = false
			m.step = stepPush
		} else {
			m.step = stepDone
		}
		return m, nil

	case pushResultMsg:
		m.pushing = false
		m.clearForceQuitPrompt()
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
		m.clearForceQuitPrompt()
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
			pendingNote := ""
			if m.branchMergePending != "" {
				pendingNote = fmt.Sprintf(" Pending merge of %s was cancelled.", m.branchMergePending)
			}
			m.branchMergePending = ""
			// Describe the state CleanupFailedStashPop actually leaves behind:
			// the working tree was reset clean, so there are no conflict
			// markers to resolve anywhere. The changes only exist in the stash.
			m.err = recoveryError{fmt.Errorf("switched to %s, but your stashed changes did not apply here — the working tree was reset clean and nothing was lost.%s Your changes are in stash %s; recover with: git stash apply %s",
				msg.newBranch, pendingNote, msg.stashRef, msg.stashRef)}
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
		m.clearForceQuitPrompt()
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
		m.clearForceQuitPrompt()
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
		m.clearForceQuitPrompt()
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
		m.clearForceQuitPrompt()
		if msg.err != nil {
			verb := "pull"
			if msg.kind == pullKindMain {
				verb = "sync with " + m.syncMainBranchName
			}
			if len(msg.conflictFiles) > 0 {
				// Abort first: that puts the tree back on the stash's base
				// commit, which is exactly where the stash applies cleanly.
				git.MergeAbort()
				m.err = fmt.Errorf("%s conflict — the merge was aborted, nothing changed", verb)
			} else {
				m.err = msg.err
			}
			// The pull auto-stashed a dirty tree before merging. Whatever
			// went wrong above, those changes must come back — or the user
			// must be told exactly where they are.
			if msg.stashed {
				if popErr := git.StashPop(); popErr != nil {
					git.CleanupFailedStashPop()
					m.err = recoveryError{fmt.Errorf("%v. Restoring your uncommitted changes also failed, so the working tree was reset clean — they are safe in stash %s; recover with: git stash apply %s",
						m.err, msg.stashRef, msg.stashRef)}
				} else {
					m.err = fmt.Errorf("%v. Your uncommitted changes were restored", m.err)
				}
			}
			return m.exitSyncDialog()
		}
		m.RefreshGraphs()
		return m.exitSyncDialog()

	case branchMergeResultMsg:
		m.branchMerging = false
		m.branchMergeMode = false
		m.clearForceQuitPrompt()
		if msg.err != nil {
			conflicts := msg.conflictFiles
			if len(conflicts) > 0 {
				// Always auto-abort merge conflicts — there's no in-TUI
				// resolution path, and leaving the repo in a half-merged
				// state surprises users on the next operation. The error
				// tells them how many files conflicted so they know how
				// much work awaits in their terminal.
				git.MergeAbort()
				m.err = fmt.Errorf("merge aborted — %d conflicting file(s). Resolve in your terminal (git status, edit, git add, git merge --continue) and retry", len(conflicts))
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
			// Quitting between the exec steps of a mutating sequence can
			// orphan work (see opInFlight). Swallow the first ctrl+c and
			// warn; a second one force-quits for users who really are stuck.
			if m.opInFlight() && !m.forceQuitArmed {
				m.forceQuitArmed = true
				m.err = fmt.Errorf("%s operation in progress — ctrl+c again to force quit", symWarn)
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		}
		// Any other keypress disarms the force-quit prompt.
		m.forceQuitArmed = false
		// Clear error on any keypress — except recovery errors, which carry a
		// stash SHA the user still has to copy. An arrow key used to be enough
		// to destroy the only in-app record of it. Those are dismissed
		// deliberately with esc/enter, and that keypress is consumed so the
		// dismissal doesn't also navigate.
		if isRecoveryError(m.err) {
			switch msg.String() {
			case "esc", "enter":
				m.err = nil
				return m, nil
			}
		} else {
			m.err = nil
		}
		// Clear the one-shot init success banner after the user acknowledges
		// the menu by pressing any key.
		if m.step == stepMenu {
			m.initSuccessMsg = ""
		}
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
	case stepInit:
		return m.updateInit(msg)
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
	case stepInit:
		content = m.viewInit()
	}

	return lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, content)
}
