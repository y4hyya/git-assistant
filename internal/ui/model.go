package ui

import (
	"errors"
	"fmt"
	"strings"
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
type branchDeleteResultMsg struct {
	err error
	// name/forced describe the attempt, so the handler can offer a force
	// delete for the one failure that has an in-TUI answer without having to
	// re-read the (already mutated) branch list to find out which branch it was.
	name   string
	forced bool
}
type branchMergeResultMsg struct {
	err           error
	conflictFiles []string
	source        string // branch that was merged in, for the result note
	// merged distinguishes "the merge itself failed" from "the merge landed
	// but restoring the auto-stash afterwards did not" — the second still
	// needs the graph refreshed.
	merged bool
	// stashed/stashRef carry the auto-stash the merge took before starting.
	// On failure the handler owns recovery (it aborts the merge first); on
	// success the command has already popped and reports via stashRestored.
	stashed       bool
	stashRef      string
	stashRestored bool
}
type fetchResultMsg struct{ err error }

// dashboardRefreshMsg delivers one off-thread reading of everything the
// dashboard shows. See requestDashboardRefresh.
type dashboardRefreshMsg struct{ snap dashboardSnapshot }

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
	branchEntries     []types.BranchEntry
	branchCursor      int
	branchScroll      int
	branchCreateMode  bool
	branchCreateInput textinput.Model
	branchDeleteMode  bool
	branchMergeMode   bool
	// branchForceDelete* drive the second confirmation shown when a safe
	// `git branch -d` came back with ErrBranchNotMerged.
	branchForceDeleteMode bool
	branchForceDeleteName string
	branchStandalone      bool
	branchSwitching       bool
	branchMerging         bool
	// branchCreating/branchDeleting block input while those ops are in
	// flight. Without them a double-enter fired two creates (the loser
	// reporting "already exists") and left the dying branch interactive.
	branchCreating     bool
	branchDeleting     bool
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
	// amendSHA/amendPushed are the confirm screen's view of the commit being
	// rewritten, also read once on entering the step. They used to be read from
	// viewConfirm, which runs on every spinner tick — two git subprocesses ten
	// times a second for the whole duration of "Committing...".
	amendSHA    string
	amendPushed bool

	// Cached: whether the repo has at least one commit. Refreshed by the
	// dashboard snapshot so menuItems doesn't fork git per keypress.
	hasAnyCommit bool

	// Spinner for async operations
	spinner spinner.Model

	// Error (shown on current step, cleared on next keypress)
	err error

	// statusNote is the one-shot "what just happened" line: merged, pulled,
	// deleted, switched-with-stash, .gitignore updated. Every mutating
	// operation that used to succeed in complete silence writes one here, and
	// the menu and branch manager render it identically. Survives navigation
	// keys, cleared by the next real action — see the tea.KeyMsg branch of
	// Update and isNavKey.
	statusNote string

	// Main menu
	menuCursor int

	// Commit graph
	localGraph   string
	aheadBehind  string
	behindMain   int
	behindOrigin int
	// mainName/mainRef come from the same resolution behindMain was measured
	// with, so the badge, the "s" shortcut's visibility and the ref it merges
	// can never disagree about what "main" is.
	mainName string
	mainRef  string

	// Cached branch count — refreshed by the dashboard snapshot so the menu
	// can read it without forking `git branch` on every keypress.
	branchCount int

	// Dashboard refresh (async). refreshing marks one in flight; refreshPending
	// remembers a request that arrived while it was, so refreshes coalesce
	// instead of stacking a second fleet of git processes on the first.
	refreshing     bool
	refreshPending bool

	// Background fetch
	fetching  bool
	lastFetch time.Time
	// fetchStale is set when a background fetch failed. The ahead/behind
	// numbers on screen are then from the last successful fetch, and the menu
	// says so — quietly, since a failed background fetch is noise, not an error.
	fetchStale bool

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
	// True incoming counts. The dialog only ever lists a sample, and reading
	// the header off len(sample) silently understated "25 new" as "10 new".
	syncCurrTotal int
	syncMainTotal int
	// syncAhead/syncDiverged: a branch that is ahead AND behind has either
	// genuinely forked or had its history rewritten (amend/undo of a pushed
	// commit). Pulling there merges the pre-rewrite commits straight back in,
	// so the dialog has to say so before offering it.
	syncAhead    int
	syncDiverged bool

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
	// The adaptive palette entry, not the pinned brand hex: this glyph sits on
	// the terminal's own background, where the dark-only purple was close to
	// unreadable on a light theme.
	s.Style = lipgloss.NewStyle().Foreground(purple)

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
// NewModel has already taken the one synchronous dashboard reading; only the
// branch list is extra here.
func NewBranchModel(branch string) Model {
	m := NewModel(nil, branch)
	m.step = stepBranch
	m.branchStandalone = true
	m.branchEntries = git.GetAllBranches()
	return m
}

// dashboardSnapshot is everything the dashboard reads out of git in one go.
// It is computed off the Bubble Tea loop and applied to the model in a single
// assignment, so no git process ever runs inside Update.
type dashboardSnapshot struct {
	branch       string // the branch it was computed for; a switch invalidates it
	graph        string
	aheadBehind  string
	behindOrigin int
	behindMain   int
	mainName     string
	mainRef      string
	branchCount  int
	hasAnyCommit bool

	files   []types.FileEntry
	filesOK bool // false when withStatus was off, or git status failed
}

// readDashboard forks the six-to-nine git processes the dashboard needs. It
// touches no Model state — it runs on a Bubble Tea command goroutine, where
// reading the model would be a data race.
func readDashboard(branch string, withStatus bool) dashboardSnapshot {
	snap := dashboardSnapshot{branch: branch}
	snap.graph = git.GetUnifiedGraph(15)
	a, b, up := git.GetAheadBehind(branch)
	snap.aheadBehind = formatAheadBehind(a, b, up)
	snap.behindOrigin = b
	main := git.ResolveMainSync(branch)
	snap.behindMain = main.Behind
	snap.mainName = main.Name
	snap.mainRef = main.Ref
	snap.branchCount = len(git.GetAllBranches())
	snap.hasAnyCommit = git.HasAnyCommit()
	if withStatus {
		if files, err := git.GetStatus(); err == nil {
			snap.files = files
			snap.filesOK = true
		}
	}
	return snap
}

// applyDashboard copies a snapshot onto the model. Pure assignment — this is
// the only thing the refresh does inside Update.
//
// The working-tree half is applied only while the dashboard is the visible
// step. A late snapshot landing after the user has walked into the file
// selector would otherwise reset their cursor and drop their selections; the
// wizard re-reads status for itself on the way in (see updateMenu's "Commit").
func (m *Model) applyDashboard(s dashboardSnapshot) {
	m.localGraph = s.graph
	m.aheadBehind = s.aheadBehind
	m.behindOrigin = s.behindOrigin
	m.behindMain = s.behindMain
	m.mainName = s.mainName
	m.mainRef = s.mainRef
	m.branchCount = s.branchCount
	m.hasAnyCommit = s.hasAnyCommit
	if s.filesOK && m.step == stepMenu {
		m.files = s.files
		m.cursor = 0
		m.fileScroll = 0
	}
}

// RefreshGraphs reads the dashboard synchronously. It is the startup path only
// — NewModel and NewBranchModel run before the first frame, where there is no
// UI to block. Everything inside the Update loop goes through
// requestDashboardRefresh instead.
func (m *Model) RefreshGraphs() {
	m.applyDashboard(readDashboard(m.branch, false))
}

// requestDashboardRefresh returns the command that recomputes the dashboard off
// the UI thread. It replaces the synchronous RefreshGraphs+refreshFiles pair
// that every result handler used to call: those forked up to nine git processes
// inside Update, and on a large repository `rev-list --count` walks history, so
// every return to the menu froze input for the sum of their latencies.
//
// Coalescing rule: at most one refresh runs at a time. A request made while one
// is in flight sets refreshPending and returns nil; the handler re-dispatches
// when the in-flight result lands. The screen therefore keeps rendering the
// previous snapshot instead of blocking, and the final state is always the
// freshest one without ever stacking two fleets of git processes.
func (m *Model) requestDashboardRefresh() tea.Cmd {
	if m.refreshing {
		m.refreshPending = true
		return nil
	}
	m.refreshing = true
	branch := m.branch
	return func() tea.Msg {
		return dashboardRefreshMsg{snap: readDashboard(branch, true)}
	}
}

// editAreaWidth/editAreaHeight size the file editor's textarea to the terminal.
// The WindowSizeMsg handler applies them on every resize; step_files.go's edit
// entry uses the same numbers.
func editAreaWidth(termWidth int) int {
	w := termWidth - 10
	if w < 40 {
		w = 40
	}
	return w
}

func editAreaHeight(termHeight int) int {
	h := termHeight - 10
	if h < 5 {
		h = 5
	}
	return h
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
// state and asks for a fresh reading of the working tree and the graph/counters.
// Every transition to stepMenu goes through here so no path can skip one of the
// three (the esc-from-Files path used to skip all of them).
//
// The reading is asynchronous: the menu draws immediately from the previous
// snapshot and updates a moment later. Returning to the dashboard used to mean
// waiting for git status plus the whole graph pass before the first keystroke
// was even read.
func (m *Model) returnToMenu() tea.Cmd {
	m.resetWizard()
	m.step = stepMenu
	return tea.Batch(m.requestDashboardRefresh(), m.maybeFetch())
}

// setStatusNote records the one-shot "what just happened" line. Notes replace
// each other: only the newest operation is worth reporting.
func (m *Model) setStatusNote(format string, args ...any) {
	m.statusNote = fmt.Sprintf(format, args...)
}

// rendersStatusNote reports whether the current step actually draws
// m.statusNote. Notes are only cleared on steps that showed them — an
// operation finishing on a screen with nowhere to render it (the .gitignore
// apply, which lands back in the file selector) keeps its note until the
// dashboard can say what happened.
func (m Model) rendersStatusNote() bool {
	return m.step == stepMenu || m.step == stepBranch
}

// isNavKey reports whether a keypress only moves a cursor. Status notes
// survive those: scrolling a list is not an acknowledgement, and clearing on
// literally any key made a merge result unreadable past the first frame.
func isNavKey(k string) bool {
	switch k {
	case "up", "down", "left", "right", "j", "k", "h", "l",
		"tab", "shift+tab", "pgup", "pgdown", "home", "end":
		return true
	}
	return false
}

// plural renders a count with the right noun: "1 commit", "3 commits".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// describeGitignoreChange summarizes an applied .gitignore edit: patterns
// added, patterns removed, and tracked files that had to be un-tracked for the
// new rules to bite. Read from the toggles the file list still carries, before
// the handler replaces it with a fresh git status.
func describeGitignoreChange(files []types.FileEntry, remove map[string]bool, cached []string) string {
	added := 0
	for _, f := range files {
		if f.Gitignored {
			added++
		}
	}
	removed := 0
	for _, drop := range remove {
		if drop {
			removed++
		}
	}
	var parts []string
	if added > 0 {
		parts = append(parts, plural(added, "entry", "entries")+" added")
	}
	if removed > 0 {
		parts = append(parts, plural(removed, "entry", "entries")+" removed")
	}
	if len(cached) > 0 {
		parts = append(parts, plural(len(cached), "file", "files")+" untracked")
	}
	if len(parts) == 0 {
		return "no entries changed"
	}
	return strings.Join(parts, ", ")
}

// opInFlight reports whether a mutating operation is currently running as a
// detached Bubble Tea command. Quitting mid-sequence can leave the repo in a
// half-finished state — an auto-stash orphaned between `git stash` and
// `git stash pop`, a merge left with MERGE_HEAD, a commit killed mid-write —
// so ctrl+c asks for confirmation while any of these are set.
//
// The background fetch and the dashboard refresh are deliberately excluded:
// both only read, they are safe to abandon, and they run often enough that
// guarding them would make ctrl+c feel broken.
func (m Model) opInFlight() bool {
	return m.committing ||
		m.pushing ||
		m.saving ||
		m.branchSwitching ||
		m.branchMerging ||
		m.branchCreating ||
		m.branchDeleting ||
		m.undoing ||
		m.gitignoring ||
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

// startResult is the prologue of every mutating operation's result handler.
// Both the force-quit warning and the previous operation's status note belong
// to the operation that just finished, so both are stale the instant a new
// result lands — without this, "Undid the last commit" could still be sitting
// on the dashboard after the commit that followed it. Handlers with something
// to report set a fresh note afterwards.
//
// The background fetch deliberately does NOT call this: it is not an operation
// the user performed, and clearing on it would erase a pull's own note a
// second after the pull produced it.
func (m *Model) startResult() {
	m.clearForceQuitPrompt()
	m.statusNote = ""
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
		// The init inputs used to keep the fixed width setupInitModel gave them,
		// so on a narrow terminal the URL field ran past the box border.
		m.initURLInput.Width = min(inputWidth, 60)
		m.initNameInput.Width = min(inputWidth, 50)
		// The file editor is sized once, on entry, from the dimensions of that
		// moment. Resizing mid-edit left it at the old size: shrunk, styledBox
		// clipped its bottom rows (help bar, sometimes the cursor's own line);
		// grown, it stayed small with dead space beside it. Same numbers the
		// entry path in step_files.go computes.
		m.editArea.SetWidth(editAreaWidth(m.width))
		m.editArea.SetHeight(editAreaHeight(m.height))
		return m, nil

	case dashboardRefreshMsg:
		m.refreshing = false
		// A snapshot taken for a branch we have since left describes the wrong
		// repository state; drop it and take a fresh reading instead.
		stale := msg.snap.branch != m.branch
		if !stale {
			m.applyDashboard(msg.snap)
		}
		if m.refreshPending || stale {
			m.refreshPending = false
			return m, m.requestDashboardRefresh()
		}
		return m, nil

	case undoResultMsg:
		m.confirmUndo = false
		m.undoing = false
		m.undoPushed = false
		m.startResult()
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
		m.setStatusNote("Undid the last commit — its changes are back in your working tree")
		// Without this the menu graph still shows the undone commit, which
		// reads as "undo failed" and invites a second, destructive undo.
		return m, m.requestDashboardRefresh()

	case saveResultMsg:
		m.saving = false
		m.startResult()
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
		m.startResult()
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		// Say what landed in .gitignore. Counted before the file list is
		// replaced below, which is what carries the toggles.
		m.setStatusNote("Updated .gitignore — %s", describeGitignoreChange(m.files, m.removeIgnored, m.gitignoreCached))
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
		m.startResult()
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		refresh := m.requestDashboardRefresh()
		// Amends route straight to Done — auto-routing to push after an
		// amend would either fail (non-FF on pushed commits) or surprise
		// the user by force-pushing. The Confirm step already warned
		// about --force-with-lease; the user pushes manually afterward.
		if m.amendMode {
			m.step = stepDone
			return m, refresh
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
		return m, refresh

	case pushResultMsg:
		m.pushing = false
		m.startResult()
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.pushed = true
		m.step = stepDone
		return m, m.requestDashboardRefresh()

	case branchSwitchResultMsg:
		m.branchSwitching = false
		m.startResult()
		if msg.err != nil {
			m.err = msg.err
			if m.branchMergePending != "" {
				// The picker flow's switch leg failed, so the merge it was
				// setting up never runs — say so instead of leaving the user
				// to infer it from a checkout error.
				m.err = annotateErr(msg.err,
					fmt.Sprintf("— the pending merge of %s was cancelled", m.branchMergePending))
				m.branchMergePending = ""
			}
			return m, nil
		}
		// The change count the user was looking at when they pressed enter —
		// read before the post-switch refresh replaces the list.
		carried := len(m.files)
		m.branch = msg.newBranch
		m.branchEntries = git.GetAllBranches()
		m.branchCursor = 0
		m.branchScroll = 0
		refresh := m.requestDashboardRefresh()
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
			return m, refresh
		}
		// A switch on a dirty tree stashes, checks out and pops — silently, so
		// far as the screen was concerned. Uncommitted work following the user
		// onto another branch is exactly the thing a beginner needs told.
		if msg.stashRef != "" {
			if carried > 0 {
				m.setStatusNote("Switched to %s — %s stashed and restored", msg.newBranch, plural(carried, "changed file", "changed files"))
			} else {
				m.setStatusNote("Switched to %s — your uncommitted changes were stashed and restored", msg.newBranch)
			}
		} else {
			m.setStatusNote("Switched to %s", msg.newBranch)
		}
		// If a merge was pending (target picker flow), start it now
		if m.branchMergePending != "" {
			source := m.branchMergePending
			m.branchMergePending = ""
			m.branchMerging = true
			return m, tea.Batch(doMergeBranch(source), m.spinner.Tick, refresh)
		}
		return m, refresh

	case branchCreateResultMsg:
		m.branchCreating = false
		m.startResult()
		if msg.err != nil {
			// Create mode stays on so the name can be fixed in place.
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
		// Creating a branch also checks it out, so the ahead/behind chip, the
		// branch count and the graph decorations are all stale now.
		return m, m.requestDashboardRefresh()

	case branchDeleteResultMsg:
		m.branchDeleteMode = false
		m.branchDeleting = false
		m.startResult()
		if msg.err != nil {
			// `git branch -d` refusing to drop commits no other branch holds
			// is the one delete failure with an answer inside the TUI: ask
			// once more, then run -D. Everything else surfaces as an error.
			if errors.Is(msg.err, git.ErrBranchNotMerged) && msg.name != "" {
				m.branchForceDeleteMode = true
				m.branchForceDeleteName = msg.name
				return m, nil
			}
			m.err = msg.err
			return m, nil
		}
		m.branchEntries = git.GetAllBranches()
		if m.branchCursor >= len(m.branchEntries) {
			m.branchCursor = max(0, len(m.branchEntries)-1)
		}
		if msg.forced {
			m.setStatusNote("Force-deleted %s — its unmerged commits are only in `git reflog` now", msg.name)
		} else {
			m.setStatusNote("Deleted branch %s", msg.name)
		}
		return m, m.requestDashboardRefresh()

	case fetchResultMsg:
		m.fetching = false
		m.clearForceQuitPrompt()
		// Failures stay quiet — this is a background op the user didn't ask
		// for, so offline/auth errors must not surface as alarming banners.
		// Two things do have to happen, though: the debounce clock only starts
		// on a SUCCESSFUL fetch (stamping it on failure suppressed for 30s
		// exactly the retry that would have recovered), and the menu says the
		// numbers it is showing may be stale rather than presenting refs from
		// the last successful fetch as current.
		if msg.err != nil {
			m.fetchStale = true
		} else {
			m.lastFetch = time.Now()
			m.fetchStale = false
		}
		refresh := m.requestDashboardRefresh()
		// Auto-show the sync dialog once per session on the first successful
		// fetch, if we're on the menu (startup) and something is out of sync.
		// Later fetches (on return to menu) silently update indicators only.
		//
		// Never while an operation is running: the startup fetch routinely
		// lands after the user has already pressed "s", and opening the dialog
		// on top of an in-flight merge let them start a pull against a repo
		// mid-merge (index.lock contention, a stash opened inside a merge)
		// behind a screen that showed neither operation's spinner.
		if !m.syncDialogShown && m.step == stepMenu && msg.err == nil && !m.opInFlight() {
			m.syncDialogShown = true
			if m.populateSyncDialog() {
				m.syncReturnStep = stepMenu
				m.step = stepSync
			}
		}
		return m, refresh

	case pullResultMsg:
		m.pulling = false
		m.startResult()
		if msg.err != nil {
			verb := "pull"
			if msg.kind == pullKindMain {
				verb = "sync with " + m.syncMainBranchName
			}
			if len(msg.conflictFiles) > 0 {
				// Abort first: that puts the tree back on the stash's base
				// commit, which is exactly where the stash applies cleanly.
				// State the file count the way the branch-manager merge does —
				// it is the only measure of how much work a manual retry is.
				git.MergeAbort()
				m.err = fmt.Errorf("%s conflict — %s, so the merge was aborted and nothing changed",
					verb, plural(len(msg.conflictFiles), "conflicting file", "conflicting files"))
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
		// Success used to be completely silent: the dialog vanished and the
		// menu came back looking exactly as it had. Say what arrived, using the
		// counts the dialog was already showing — exitSyncDialog clears them,
		// so build the note first.
		switch {
		case msg.kind == pullKindMain && m.syncMainTotal > 0:
			m.setStatusNote("Merged %s from origin/%s into %s",
				plural(m.syncMainTotal, "commit", "commits"), m.syncMainBranchName, m.branch)
		case msg.kind == pullKindMain:
			m.setStatusNote("Synced %s with origin/%s", m.branch, m.syncMainBranchName)
		case m.syncCurrTotal > 0:
			m.setStatusNote("Pulled %s from origin/%s",
				plural(m.syncCurrTotal, "commit", "commits"), m.branch)
		default:
			m.setStatusNote("Pulled origin/%s", m.branch)
		}
		// exitSyncDialog only refreshes when it lands back on the menu; the
		// pre-push check returns to the push step, where the graph is just as
		// stale.
		refresh := m.requestDashboardRefresh()
		next, cmd := m.exitSyncDialog()
		return next, tea.Batch(refresh, cmd)

	case branchMergeResultMsg:
		m.branchMerging = false
		m.branchMergeMode = false
		m.startResult()
		if msg.err != nil && !msg.merged {
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
			// The merge auto-stashed a dirty tree before starting. Whatever
			// failed above, those changes have to come back — the abort put
			// the tree back on the stash's base commit, which is exactly
			// where it applies cleanly.
			if msg.stashed {
				if popErr := git.StashPop(); popErr != nil {
					git.CleanupFailedStashPop()
					m.err = recoveryError{fmt.Errorf("%v. Restoring your uncommitted changes also failed, so the working tree was reset clean — they are safe in stash %s; recover with: git stash apply %s",
						m.err, msg.stashRef, msg.stashRef)}
				} else {
					m.err = fmt.Errorf("%v. Your uncommitted changes were stashed and restored", m.err)
				}
			}
			return m, nil
		}
		m.branchEntries = git.GetAllBranches()
		refresh := m.requestDashboardRefresh()
		if msg.err != nil {
			// The merge landed; only restoring the auto-stash afterwards
			// failed. The command already reported where the changes are.
			m.err = msg.err
			return m, refresh
		}
		// One note, rendered the same way wherever the merge was started from.
		// The menu's sync shortcut used to have nowhere to say this and smuggled
		// the stash disclosure out through m.err instead.
		note := fmt.Sprintf("Merged %s into %s", msg.source, m.branch)
		if msg.stashRestored {
			note += " — your uncommitted changes were stashed and restored"
		}
		m.statusNote = note
		return m, refresh

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
			// Take any in-flight remote command down with us. A plain exit
			// left `gh repo create` orphaned, and it went on to create the
			// repository the user had just cancelled — discovered only on
			// github.com or at the next launch.
			git.CancelNetworkOps()
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
		// The status note is one-shot too, but it survives cursor movement:
		// "Merged feature into main" is unreadable if arrowing down the branch
		// list wipes it. It also survives steps that don't render it, so the
		// .gitignore result — which lands back in the file selector — still gets
		// said on the dashboard.
		if m.statusNote != "" && m.rendersStatusNote() && !isNavKey(msg.String()) {
			m.statusNote = ""
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
