package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── One key list per screen ─────────────────────────────
//
// Every footer in the app and the `?` overlay render the SAME data: the rows
// returned by helpRows for whatever screen is currently on display. The overlay
// used to be the obvious place to hand-copy a key list into, and a copied list
// drifts the first time a key moves — so there is exactly one list, and the
// switch below mirrors the same mode checks the views make, in the same order.
//
// Rows, not a flat slice, because two footers are honestly two lines: the file
// selector's key set does not fit an 80-column row, and a wrapped help bar is
// worse than two tidy ones.

// helpRows returns the current screen's keys, grouped into the rows its footer
// draws. Empty means "this screen has no footer" (the in-flight frames), and
// that is also what suppresses the overlay there.
func (m Model) helpRows() [][]helpEntry {
	switch m.step {
	case stepMenu:
		return m.menuHelp()
	case stepFiles:
		return m.filesHelp()
	case stepBranch:
		return m.branchHelp()
	case stepConfig:
		return m.configHelp()
	case stepType:
		return m.typeHelp()
	case stepCustom:
		return m.customHelp()
	case stepMessage:
		return m.messageHelp()
	case stepConfirm:
		return m.confirmHelp()
	case stepPush:
		return m.pushHelp()
	case stepDone:
		return m.doneHelp()
	case stepSync:
		return m.syncHelp()
	case stepInit:
		return m.initHelp()
	case stepStash:
		return m.stashHelp()
	}
	return nil
}

// helpEntries flattens helpRows. The overlay lists the keys one per line, so it
// has no use for the footer's row breaks — but it must contain exactly the same
// entries, which is what the parity test asserts.
func (m Model) helpEntries() []helpEntry {
	var all []helpEntry
	for _, row := range m.helpRows() {
		all = append(all, row...)
	}
	return all
}

// oneRow is the common case: a single footer line.
func oneRow(entries ...helpEntry) [][]helpEntry { return [][]helpEntry{entries} }

// renderHelpRows renders a footer: one help bar per row, newline separated.
func renderHelpRows(r [][]helpEntry) string {
	if len(r) == 0 {
		return ""
	}
	lines := make([]string, 0, len(r))
	for _, row := range r {
		lines = append(lines, renderHelp(row))
	}
	return strings.Join(lines, "\n")
}

// footerHeight is how many terminal rows this screen's footer will actually
// take. Views that budget their content against the terminal ask for it instead
// of hardcoding a row count: the number of help rows changes with the screen
// (and grew by one the day `x` joined the file selector), and a budget that
// disagrees drops the whole footer rather than one list row.
func (m Model) footerHeight() int {
	r := m.helpRows()
	inner := m.width - 8 // styledBox's inner text width
	if inner <= 0 {
		return len(r)
	}
	total := 0
	for _, row := range r {
		cost := 1
		if w := lipgloss.Width(renderHelp(row)); w > inner {
			cost = (w + inner - 1) / inner
		}
		total += cost
	}
	return total
}

// ── Per-screen key lists ────────────────────────────────

func (m Model) menuHelp() [][]helpEntry {
	entries := []helpEntry{
		{symArrows, "navigate"},
		{"enter", "select"},
	}
	if m.hasRemote && m.behindOrigin > 0 && !m.detached {
		entries = append(entries, helpEntry{"p", "pull"})
	}
	if m.canSyncMain() {
		entries = append(entries, helpEntry{"s", "sync"})
	}
	// Capital S, because lowercase s is the sync shortcut above and the two are
	// live on this screen at the same time. Listed only when there is something
	// to open — the recovery banners that send people here say "press S", and
	// the key answers under exactly the same condition.
	if m.stashAvailable() {
		entries = append(entries, helpEntry{"S", "stash"})
	}
	// The dashboard is where the `?` overlay is discovered; every other screen
	// answers it too, and the overlay itself says so.
	entries = append(entries, helpEntry{"?", "help"}, helpEntry{"q", "quit"})
	return oneRow(entries...)
}

// filesHelp mirrors viewFiles' dispatch — edit and diff and filter each have
// their own view function, and the two prompts short-circuit the list.
func (m Model) filesHelp() [][]helpEntry {
	switch {
	case m.editMode:
		switch {
		case m.confirmExit:
			return oneRow(helpEntry{"y", "discard"}, helpEntry{"any", "cancel"})
		case m.saving:
			return nil // the saving frame draws no footer
		}
		return oneRow(helpEntry{"ctrl+s", "save"}, helpEntry{"esc", "back"})

	case m.showDiff:
		if m.diffContent == "" {
			return oneRow(helpEntry{"esc", "back"})
		}
		if m.diffFileEditable() {
			return oneRow(
				helpEntry{symArrows, "scroll"},
				helpEntry{"e", "edit"},
				helpEntry{"esc", "back"},
				helpEntry{"q", "quit"},
			)
		}
		return oneRow(
			helpEntry{symArrows, "scroll"},
			helpEntry{"esc", "back"},
			helpEntry{"q", "quit"},
		)

	case m.filterMode:
		return oneRow(
			helpEntry{symArrows, "navigate"},
			helpEntry{"tab", "select"},
			helpEntry{"enter", "jump"},
			helpEntry{"esc", "cancel"},
		)

	case m.confirmUndo:
		// A pushed commit gets both exits, and they are different operations —
		// see viewFiles, which spells each one out above this bar.
		if m.undoPushed {
			return oneRow(
				helpEntry{"u", "undo anyway"},
				helpEntry{"r", "revert instead"},
				helpEntry{"any", "cancel"},
			)
		}
		return oneRow(helpEntry{"y", "confirm"}, helpEntry{"any", "cancel"})

	case m.confirmDiscard:
		return oneRow(helpEntry{"y", "confirm"}, helpEntry{"any", "cancel"})

	case m.gitignoreMode:
		return oneRow(
			helpEntry{symArrows, "navigate"},
			helpEntry{"space", "toggle"},
			helpEntry{"a", "all"},
			helpEntry{"enter", "confirm"},
			helpEntry{"g/esc", "cancel"},
			helpEntry{"q", "quit"},
		)
	}

	// Commit mode. Every key listed here is handled in updateFiles, and every
	// key handled there is listed. Three rows, grouped by what they do — choose
	// what to commit, act on the file under the cursor, go somewhere else —
	// because twelve keys at four-space separation do not fit two 80-column
	// lines, and a wrapped help bar loses a whole row to the height budget.
	return [][]helpEntry{
		{
			{symArrows, "navigate"},
			{"space", "select"},
			{"a", "all"},
			{"enter", "next"},
		},
		{
			{"/", "filter"},
			{"d", "diff"},
			// The label follows the entry under the cursor: x on a deleted file
			// brings it BACK, and a key labelled "discard" there reads as
			// "delete it harder".
			{"x", m.discardKeyLabel()},
			{"u", "undo"},
		},
		{
			{"b", "branch"},
			{"g", "ignore"},
			{"esc", "menu"},
			{"q", "quit"},
		},
	}
}

func (m Model) branchHelp() [][]helpEntry {
	switch {
	case m.branchCreateMode:
		return oneRow(helpEntry{"enter", "create"}, helpEntry{"esc", "cancel"})
	case m.branchRenameMode:
		return oneRow(helpEntry{"enter", "rename"}, helpEntry{"esc", "cancel"})
	case m.branchDeleteMode && m.branchCursor < len(m.branchEntries):
		return oneRow(helpEntry{"y", "confirm"}, helpEntry{"any", "cancel"})
	case m.branchForceDeleteMode:
		return oneRow(helpEntry{"y", "force delete"}, helpEntry{"any", "cancel"})
	case m.mergeTargetMode:
		return oneRow(
			helpEntry{symArrows, "navigate"},
			helpEntry{"enter", "select"},
			helpEntry{"esc", "cancel"},
		)
	case m.branchMergeMode:
		return oneRow(helpEntry{"y", "confirm"}, helpEntry{"any", "cancel"})
	}

	// The list. Two rows since rename joined: seven entries do not fit an
	// 80-column line.
	exit := helpEntry{"esc", "back"}
	if m.branchStandalone {
		exit = helpEntry{"q", "quit"}
	}
	first := []helpEntry{
		{symArrows, "navigate"},
		{"enter", "switch"},
		{"c", "create"},
	}
	// A failed auto-stash pop from a switch or a merge lands its recovery banner
	// on THIS screen, and the banner says "press S". Same key, same gate as the
	// dashboard's — on the shorter row, so the second one does not wrap.
	if m.stashAvailable() {
		first = append(first, helpEntry{"S", "stash"})
	}
	return [][]helpEntry{
		first,
		{
			{"r", "rename"},
			{"d", "delete"},
			{"m", "merge"},
			exit,
		},
	}
}

// stashHelp mirrors viewStash's dispatch: the confirmation and the preview each
// short-circuit the list, and an empty stack has no per-entry keys at all.
func (m Model) stashHelp() [][]helpEntry {
	switch {
	case m.stashConfirmDrop:
		return oneRow(helpEntry{"y", "delete"}, helpEntry{"any", "cancel"})
	case m.stashShowDiff:
		return oneRow(
			helpEntry{symArrows, "scroll"},
			helpEntry{"d/enter/esc", "back"},
			helpEntry{"q", "quit"},
		)
	}
	if len(m.stashEntries) == 0 {
		return oneRow(helpEntry{"esc", "menu"}, helpEntry{"q", "quit"})
	}
	// Two rows, grouped by what they do — move and look, then act on the entry
	// under the cursor. Five keys with their honest labels do not fit one
	// 80-column line, and a wrapped bar costs the list a whole row.
	return [][]helpEntry{
		{
			{symArrows, "navigate"},
			{"enter/d", "preview"},
			{"esc", "menu"},
			{"q", "quit"},
		},
		{
			{"a", "apply (keep)"},
			{"p", "pop (apply + delete)"},
			{"x", "delete"},
		},
	}
}

func (m Model) configHelp() [][]helpEntry {
	switch {
	case m.configRemoveRemote:
		return oneRow(helpEntry{"y", "remove"}, helpEntry{"any", "keep"})
	case m.configPickMode:
		return oneRow(
			helpEntry{symArrows, "navigate"},
			helpEntry{"enter", "select"},
			helpEntry{"esc", "cancel"},
		)
	case m.configEditMode:
		return oneRow(
			helpEntry{"enter", "save"},
			helpEntry{"tab", "scope (cancels edit)"},
			helpEntry{"esc", "cancel"},
		)
	}
	return oneRow(
		helpEntry{symArrows, "navigate"},
		helpEntry{"enter", "edit"},
		helpEntry{"tab", "scope"},
		helpEntry{"esc", "back"},
		helpEntry{"q", "quit"},
	)
}

func (m Model) typeHelp() [][]helpEntry {
	return oneRow(
		helpEntry{symArrows, "navigate"},
		helpEntry{"enter", "select"},
		helpEntry{"!", "breaking"},
		helpEntry{"esc", "back"},
		helpEntry{"q", "quit"},
	)
}

func (m Model) customHelp() [][]helpEntry {
	return oneRow(helpEntry{"enter", "confirm"}, helpEntry{"esc", "back"})
}

func (m Model) messageHelp() [][]helpEntry {
	switch m.messageFocus() {
	case focusScope:
		return oneRow(
			helpEntry{symArrows, "navigate"},
			helpEntry{"enter/tab", "subject"},
			helpEntry{"esc", "back"},
		)
	case focusBody:
		tabTarget := "scope"
		if m.amendRaw {
			tabTarget = "subject"
		}
		return oneRow(
			helpEntry{symArrows, "move"},
			helpEntry{"ctrl+d", "next"},
			helpEntry{"tab", tabTarget},
			helpEntry{"esc", "subject"},
		)
	}
	// Raw amends have no scope field, so esc leaves the step entirely.
	escTarget := "scope"
	if m.amendRaw {
		escTarget = "back"
	}
	tabTarget := "add body"
	if m.showBody {
		tabTarget = "body"
	}
	return oneRow(
		helpEntry{symArrows, "navigate"},
		helpEntry{"enter", "next"},
		helpEntry{"tab", tabTarget},
		helpEntry{"esc", escTarget},
	)
}

func (m Model) confirmHelp() [][]helpEntry {
	var entries []helpEntry
	// Scroll keys only when there is something to scroll.
	if len(m.selectedFiles()) > m.confirmListRows() {
		entries = append(entries, helpEntry{symArrows, "scroll files"})
	}
	entries = append(entries,
		helpEntry{"enter", "commit"},
		helpEntry{"esc", "back"},
		helpEntry{"q", "quit"},
	)
	return oneRow(entries...)
}

func (m Model) pushHelp() [][]helpEntry {
	var entries []helpEntry
	if m.pushForce {
		entries = append(entries, helpEntry{"f", "force-push (with lease)"})
	} else {
		entries = append(entries, helpEntry{"enter", m.pushVerb()})
	}
	// esc is a skip here, not a "back" — after a commit there is no step behind
	// this one to return to.
	if m.pushReturnToMenu {
		entries = append(entries, helpEntry{"n/esc", "back to menu"})
	} else {
		entries = append(entries, helpEntry{"n/esc", "skip — commit is already made"})
	}
	entries = append(entries, helpEntry{"q", "quit"})
	return oneRow(entries...)
}

func (m Model) doneHelp() [][]helpEntry {
	var entries []helpEntry
	if m.canPushFromDone() {
		label := "push now"
		if m.amendMode && m.amendPushed {
			label = "push now (force-with-lease)"
		}
		entries = append(entries, helpEntry{"p", label})
	}
	entries = append(entries,
		helpEntry{"enter/esc", "menu"},
		helpEntry{"q", "quit"},
	)
	return oneRow(entries...)
}

func (m Model) syncHelp() [][]helpEntry {
	var entries []helpEntry
	if m.syncPullCurrent {
		label := "pull"
		if m.syncDiverged {
			label = "pull anyway"
		}
		entries = append(entries, helpEntry{"p", label})
	}
	if m.syncSyncMain {
		entries = append(entries, helpEntry{"s", "sync with " + m.syncMainBranchName})
	}
	entries = append(entries, helpEntry{"enter", "skip"})
	return oneRow(entries...)
}

func (m Model) initHelp() [][]helpEntry {
	switch m.initPhase {
	case initPhasePickOption:
		return oneRow(
			helpEntry{symArrows, "navigate"},
			helpEntry{"enter", "select"},
			helpEntry{"q", "quit"},
		)
	case initPhasePickTemplate:
		return oneRow(
			helpEntry{symArrows, "navigate"},
			helpEntry{"enter", "continue"},
			helpEntry{"esc", "back"},
			helpEntry{"q", "quit"},
		)
	case initPhaseInputURL:
		return oneRow(helpEntry{"enter", "connect"}, helpEntry{"esc", "back"})
	case initPhaseInputRepoName:
		return oneRow(helpEntry{"enter", "continue"}, helpEntry{"esc", "back"})
	case initPhasePickVisibility:
		return oneRow(
			helpEntry{symArrows, "navigate"},
			helpEntry{"enter", "create"},
			helpEntry{"esc", "back"},
			helpEntry{"q", "quit"},
		)
	case initPhaseConfirmGHAuth:
		return oneRow(
			helpEntry{"y", "yes"},
			helpEntry{"n", "back"},
			helpEntry{"q", "quit"},
		)
	}
	// initPhaseWorking (and the terminal initPhaseDone) draw no footer.
	return nil
}

// ── The ? overlay ───────────────────────────────────────

// typingActive reports whether the screen on display is currently swallowing
// characters into a text field. `?` is a character there, not a command — the
// overlay must never eat a keystroke meant for a branch name or a commit
// subject.
func (m Model) typingActive() bool {
	switch m.step {
	case stepMessage, stepCustom:
		return true
	case stepFiles:
		return m.filterMode || m.editMode
	case stepBranch:
		return m.branchCreateMode || m.branchRenameMode
	case stepConfig:
		return m.configEditMode
	case stepInit:
		return m.initPhase == initPhaseInputURL || m.initPhase == initPhaseInputRepoName
	}
	return false
}

// helpAvailable reports whether `?` opens the overlay right now. Typing screens
// are out (see typingActive), so are the frames of an in-flight operation —
// covering the spinner with a key list hides the one thing the user is waiting
// on — and so is any screen with no keys to list.
func (m Model) helpAvailable() bool {
	if m.typingActive() || m.opInFlight() {
		return false
	}
	return len(m.helpRows()) > 0
}

// screenName titles the overlay. Sub-modes get their own name: "Keys — Files"
// on top of a diff would be describing the wrong screen.
func (m Model) screenName() string {
	switch m.step {
	case stepMenu:
		return "Dashboard"
	case stepFiles:
		switch {
		case m.editMode:
			return "File editor"
		case m.showDiff:
			return "Diff"
		case m.filterMode:
			return "File filter"
		case m.confirmUndo:
			return "Undo last commit"
		case m.confirmDiscard:
			return "Discard changes"
		case m.gitignoreMode:
			return ".gitignore"
		}
		return "Files"
	case stepBranch:
		return "Branch manager"
	case stepConfig:
		return "Config"
	case stepType:
		return "Commit type"
	case stepCustom:
		return "Custom commit type"
	case stepMessage:
		return "Commit message"
	case stepConfirm:
		return "Review"
	case stepPush:
		return "Push"
	case stepDone:
		return "Done"
	case stepSync:
		return "Remote sync"
	case stepInit:
		return "Setup"
	case stepStash:
		switch {
		case m.stashConfirmDrop:
			return "Delete stash"
		case m.stashShowDiff:
			return "Stash preview"
		}
		return "Stash manager"
	}
	return "git-assist"
}

// helpKeyColumn is how wide the key column is in the overlay. Padded in display
// cells rather than bytes: symArrows is two cells and six bytes, and %-10s pads
// by the latter.
const helpKeyColumn = 12

func padKey(s string) string {
	if pad := helpKeyColumn - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// viewHelp draws the `?` overlay: every key the screen underneath responds to,
// one per line, from the same rows its footer renders.
func (m Model) viewHelp() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(stepStyle.Render("Keys — " + m.screenName()))
	b.WriteString("\n\n")

	entries := m.helpEntries()
	if len(entries) == 0 {
		b.WriteString("  " + dimStyle.Render("This screen has no keys of its own.") + "\n")
	}
	for _, e := range entries {
		b.WriteString("  " + helpKeyStyle.Render(padKey(e.key)) + helpStyle.Render(e.desc) + "\n")
	}

	// Global keys, deliberately outside the list above: they belong to the app,
	// not to this screen, and the overlay must list exactly what the footer does.
	b.WriteString("\n  " + dimStyle.Render("Everywhere: "+
		"ctrl+c force quit  "+symMidDot+"  ? this list") + "\n")

	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"esc/?", "close"},
	}))
	return m.styledBox(b.String())
}
