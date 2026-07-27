package ui

import (
	"errors"
	"fmt"
	"strings"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── The stash manager ───────────────────────────────────
//
// This screen exists because the app manufactures stashes. Every branch
// switch, merge and pull auto-stashes a dirty tree, and when the pop afterwards
// conflicts the entry is left in the stack — at which point the app used to
// print "recover with: git stash apply abc1234" and send the user to a terminal
// to finish a job it had started. Here, the same entry can be looked at,
// applied and deleted without leaving.
//
// Return routing is deliberately always-menu: the screen is reachable from the
// dashboard, from `S` on the branch manager and from the recovery banners those
// two raise, and an applied stash changes the working tree — so every exit goes
// through returnToMenu(), which re-reads status and the graph. A
// stashReturnStep would buy a shorter path back to a screen whose contents the
// apply just invalidated.

// stashRecoveryHint is the closing sentence of every message about an
// auto-stash this app orphaned. It used to read "recover with: git stash apply
// abc1234" — an instruction to leave, open a terminal and finish by hand a
// sequence git-assist had started. One sentence, one place, so a new auto-stash
// path cannot reintroduce the round trip.
func stashRecoveryHint(ref string) string {
	return fmt.Sprintf("Your changes are safe in stash %s — press S to open the stash manager.", ref)
}

// stashLockedByMergeErr is what apply, pop and delete refuse with while a merge
// is being resolved. Advisory, not a failure — nothing broke, the entry simply
// belongs to an operation that is not finished.
//
// The resolver parks the auto-stash a conflicting merge took and restores it
// itself once the merge is committed or aborted. Mutating the stack underneath
// it is how the two halves of this feature used to destroy work: dropping the
// parked entry sent the resolver's pop after somebody else's stash, and popping
// it made the resolver's pop fail on an entry that no longer existed. Looking is
// still allowed — the preview is read-only.
func stashLockedByMergeErr() error {
	return fmt.Errorf("%s finish or abort the merge first — the resolver is holding a parked stash, and it restores it for you. Open \"Resolve conflicts\"", symWarn)
}

// ── Async results ───────────────────────────────────────

// stashApplyResultMsg carries the outcome of `a` (apply) or `p` (pop).
// entries is the stack as it stands AFTER the operation: refs renumber under a
// pop, so the screen must never redraw from the list it dispatched with.
type stashApplyResultMsg struct {
	err     error
	popped  bool   // pop (apply + delete) rather than apply
	sha     string // the entry this was about — stable, unlike its ref
	files   int    // how many files the entry held, read before applying
	entries []git.StashEntry
}

// stashDropResultMsg carries the outcome of `x`.
type stashDropResultMsg struct {
	err     error
	sha     string
	entries []git.StashEntry
}

// ── Async commands ──────────────────────────────────────

// doStashApply restores one entry, optionally dropping it afterwards (pop).
//
// The ref is resolved HERE, from a fresh listing, rather than taken from the
// model: stash@{N} is positional, and a drop performed since the screen was
// drawn renumbers everything below it. Applying "the stash@{1} that was on
// screen" after a drop applies a different entry entirely.
func doStashApply(sha string, pop bool) tea.Cmd {
	return func() tea.Msg {
		ref, err := git.StashRefForSHA(sha)
		if err != nil {
			entries, _ := git.StashList()
			return stashApplyResultMsg{err: err, popped: pop, sha: sha, entries: entries}
		}
		// Read before the operation: a pop deletes the entry, and the note has
		// to be able to say how much came back.
		files := git.StashFileCount(ref)
		var opErr error
		if pop {
			opErr = git.StashPopRef(ref)
		} else {
			opErr = git.StashApplyRef(ref)
		}
		entries, _ := git.StashList()
		return stashApplyResultMsg{
			err: opErr, popped: pop, sha: sha, files: files, entries: entries,
		}
	}
}

// doStashDrop deletes one entry. Same re-resolution rule as doStashApply, and
// for a sharper reason: dropping the wrong index destroys work.
func doStashDrop(sha string) tea.Cmd {
	return func() tea.Msg {
		ref, err := git.StashRefForSHA(sha)
		if err != nil {
			entries, _ := git.StashList()
			return stashDropResultMsg{err: err, sha: sha, entries: entries}
		}
		opErr := git.StashDropRef(ref)
		entries, _ := git.StashList()
		return stashDropResultMsg{err: opErr, sha: sha, entries: entries}
	}
}

// ── Entry ───────────────────────────────────────────────

// enterStash opens the manager with a freshly read stack. Called from the menu
// entry, from `S` on the menu and from `S` on the branch manager — all three
// re-read, because the count the dashboard is showing may be minutes old.
func (m *Model) enterStash() {
	// The recovery banner that sent the user here has been acted on. Recovery
	// errors deliberately survive ordinary keypresses (see Update), so without
	// this the "press S to open the stash manager" advice follows them onto the
	// screen it was pointing at and reads as a fresh failure.
	if isRecoveryError(m.err) {
		m.err = nil
	}
	entries, err := git.StashList()
	if err != nil {
		m.err = err
		return
	}
	m.stashEntries = entries
	m.stashCount = len(entries)
	m.stashCursor = 0
	m.stashScroll = 0
	m.stashShowDiff = false
	m.stashDiff = ""
	m.stashDiffScroll = 0
	m.stashConfirmDrop = false
	m.step = stepStash
}

// stashAvailable gates the dashboard's Stash entry, the `S` key and both of
// their help rows on ONE condition. A beginner who has never stashed anything
// should not have to wonder what the word means, so with an empty stack the
// feature is not on screen at all.
func (m Model) stashAvailable() bool { return m.stashCount > 0 }

// stashListRows is how many entries the list draws — listRows minus the footer
// rows past the first, exactly as the file selector budgets (see fileListRows).
// Read off the footer rather than hardcoded, so adding a key cannot silently
// delete the footer instead of one list row.
func (m Model) stashListRows() int {
	rows := m.listRows() - (m.footerHeight() - 1)
	if rows < 3 {
		rows = 3
	}
	return rows
}

// stashDiffMaxScroll is the largest preview offset that still ends on the last
// line — same bound the file diff uses, so the counter can never promise a line
// the keys cannot reach.
func (m Model) stashDiffMaxScroll() int {
	n := len(strings.Split(m.stashDiff, "\n")) - m.listRows()
	if n < 0 {
		n = 0
	}
	return n
}

// stashSelected returns the entry under the cursor.
func (m Model) stashSelected() (git.StashEntry, bool) {
	if m.stashCursor < 0 || m.stashCursor >= len(m.stashEntries) {
		return git.StashEntry{}, false
	}
	return m.stashEntries[m.stashCursor], true
}

// stashLabel names one entry in a sentence: the SHA, which is what the recovery
// banners told the user to look for.
func stashLabel(e git.StashEntry) string {
	if e.SHA == "" {
		return e.Ref
	}
	return e.SHA
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateStash(msg tea.Msg) (tea.Model, tea.Cmd) {
	// An apply, a pop and a drop all rewrite the stack. Block key input while
	// one is running so a second press cannot dispatch against a list that is
	// about to be renumbered; spinner ticks keep animating.
	if m.stashApplying || m.stashDropping {
		if _, ok := msg.(tea.KeyMsg); ok {
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

	// ── Delete confirmation ────────────────────────────
	if m.stashConfirmDrop {
		switch keyMsg.String() {
		case "y":
			m.stashConfirmDrop = false
			e, ok := m.stashSelected()
			if !ok {
				return m, nil
			}
			m.stashDropping = true
			return m, tea.Batch(doStashDrop(e.SHA), m.spinner.Tick)
		default:
			m.stashConfirmDrop = false
			return m, nil
		}
	}

	// ── Preview pane ───────────────────────────────────
	if m.stashShowDiff {
		switch keyMsg.String() {
		case "up", "k":
			if m.stashDiffScroll > 0 {
				m.stashDiffScroll--
			}
		case "down", "j":
			if m.stashDiffScroll < m.stashDiffMaxScroll() {
				m.stashDiffScroll++
			}
		case "d", "enter", "esc":
			m.stashShowDiff = false
			m.stashDiff = ""
			m.stashDiffScroll = 0
		case "q":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}

	// ── The list ───────────────────────────────────────
	switch keyMsg.String() {
	case "up", "k":
		if m.stashCursor > 0 {
			m.stashCursor--
		}
	case "down", "j":
		if m.stashCursor < len(m.stashEntries)-1 {
			m.stashCursor++
		}
	case "d", "enter":
		e, ok := m.stashSelected()
		if !ok {
			return m, nil
		}
		patch, err := git.StashShow(e.Ref)
		if err != nil {
			m.err = err
			return m, nil
		}
		m.stashDiff = patch
		m.stashDiffScroll = 0
		m.stashShowDiff = true
	case "a":
		e, ok := m.stashSelected()
		if !ok {
			return m, nil
		}
		if m.mergeInProgress {
			m.err = stashLockedByMergeErr()
			return m, nil
		}
		m.stashApplying = true
		return m, tea.Batch(doStashApply(e.SHA, false), m.spinner.Tick)
	case "p":
		e, ok := m.stashSelected()
		if !ok {
			return m, nil
		}
		if m.mergeInProgress {
			m.err = stashLockedByMergeErr()
			return m, nil
		}
		m.stashApplying = true
		return m, tea.Batch(doStashApply(e.SHA, true), m.spinner.Tick)
	case "x":
		if _, ok := m.stashSelected(); !ok {
			return m, nil
		}
		if m.mergeInProgress {
			m.err = stashLockedByMergeErr()
			return m, nil
		}
		m.stashConfirmDrop = true
		return m, nil
	case "esc":
		// Always back to the dashboard, never to whatever opened this screen:
		// an apply changes the working tree, and returnToMenu is what re-reads
		// it. See the note at the top of this file.
		return m, m.returnToMenu()
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	// Keep the cursor inside the window the view will draw.
	rows := m.stashListRows()
	if m.stashCursor < m.stashScroll {
		m.stashScroll = m.stashCursor
	}
	if m.stashCursor >= m.stashScroll+rows {
		m.stashScroll = m.stashCursor - rows + 1
	}
	if m.stashScroll > len(m.stashEntries)-rows {
		m.stashScroll = len(m.stashEntries) - rows
	}
	if m.stashScroll < 0 {
		m.stashScroll = 0
	}

	return m, nil
}

// ── Result handling ─────────────────────────────────────
//
// Both handlers live here rather than in model.go's switch so the wording, the
// list replacement and the count stay next to the commands that produce them.

func (m Model) handleStashApplyResult(msg stashApplyResultMsg) (tea.Model, tea.Cmd) {
	m.stashApplying = false
	m.startResult()
	m.stashEntries = msg.entries
	m.stashCount = len(msg.entries)
	m.clampStashCursor()
	if msg.err != nil {
		m.err = stashOpError(msg.err, msg.sha, msg.popped)
		// A conflicted apply DID change the working tree, and a failed pop can
		// have left files behind too. The dashboard has to be re-read either way.
		return m, m.requestDashboardRefresh()
	}
	verb := "Applied"
	if msg.popped {
		verb = "Popped"
	}
	if msg.files > 0 {
		m.setStatusNote("%s stash %s — %s restored to the working tree",
			verb, msg.sha, plural(msg.files, "file", "files"))
	} else {
		m.setStatusNote("%s stash %s", verb, msg.sha)
	}
	if msg.popped {
		m.appendNoteClause("the entry has been removed from the stash list")
	} else {
		m.appendNoteClause("the entry is still in the stash list")
	}
	// The files are back: the dashboard's change count, graph and Commit entry
	// are all describing a tree that no longer exists.
	return m, m.requestDashboardRefresh()
}

func (m Model) handleStashDropResult(msg stashDropResultMsg) (tea.Model, tea.Cmd) {
	m.stashDropping = false
	m.startResult()
	m.stashEntries = msg.entries
	m.stashCount = len(msg.entries)
	m.clampStashCursor()
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	m.setStatusNote("Deleted stash %s", msg.sha)
	return m, nil
}

// clampStashCursor keeps the cursor and the scroll offset on a list that just
// got shorter. A pop or a drop removes the row the cursor was sitting on, and
// the next frame indexes it for the footer and the confirmation.
func (m *Model) clampStashCursor() {
	if m.stashCursor >= len(m.stashEntries) {
		m.stashCursor = max(0, len(m.stashEntries)-1)
	}
	if m.stashScroll > m.stashCursor {
		m.stashScroll = m.stashCursor
	}
	if m.stashScroll < 0 {
		m.stashScroll = 0
	}
}

// stashOpError turns a failed apply/pop into a sentence that is true about the
// state the repository is actually in. The three outcomes are genuinely
// different and telling them apart is the whole value of the screen:
//
//   - conflict: git merged what it could and wrote conflict markers into the
//     files. The index has unmerged entries and the stash was KEPT — git keeps
//     it even on a pop — so nothing is lost.
//   - refused: uncommitted changes cover files the stash touches. git stopped
//     before touching anything; the tree and the stash are exactly as they were.
//   - gone: the entry is not in the stack any more.
func stashOpError(err error, sha string, popped bool) error {
	verb := "applying"
	if popped {
		verb = "popping"
	}
	switch {
	case errors.Is(err, git.ErrStashConflict):
		// Named files, not git's status report: the banner is three rows tall
		// and the only thing in that report the user cannot see for themselves
		// is which paths are in the way.
		what := "the files it touches"
		var conflict *git.StashConflictError
		if errors.As(err, &conflict) && len(conflict.Files) > 0 {
			what = strings.Join(conflict.Files, ", ")
		}
		// Phrased so one file and five read the same; "a.txt now hold markers"
		// is the sort of thing that makes a beginner distrust the whole message.
		return recoveryError{fmt.Errorf(
			"%s stash %s conflicted — conflict markers are now in %s, and the stash itself was kept, so nothing is lost",
			verb, sha, what)}
	case errors.Is(err, git.ErrStashDirtyTree):
		return fmt.Errorf(
			"could not restore stash %s — uncommitted changes in your working tree cover the same files, so git stopped before touching anything. %v",
			sha, err)
	case errors.Is(err, git.ErrNoSuchStash):
		return fmt.Errorf("stash %s is no longer in the list — it was already applied or deleted", sha)
	}
	return err
}

// ── View ────────────────────────────────────────────────

func (m Model) viewStash() string {
	if m.stashShowDiff {
		return m.viewStashDiff()
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Stash Manager"))
	b.WriteString("\n\n")

	// Mid-merge the stack is frozen (see stashLockedByMergeErr). Said up front,
	// because a key that refuses is only honest if the screen has already
	// explained why before it is pressed.
	if m.mergeInProgress {
		b.WriteString("  " + modifiedStyle.Render(symWarn+" A merge is being resolved — this list is read-only until it is done") + "\n")
		b.WriteString("  " + dimStyle.Render("The resolver is holding one of these entries and puts it back itself.") + "\n\n")
	}

	// ── Delete confirmation ────────────────────────────
	// Named, and costed: a dropped stash is not in any commit, so there is no
	// branch, no reflog entry a beginner can find and no undo.
	if e, ok := m.stashSelected(); ok && m.stashConfirmDrop {
		b.WriteString("  " + modifiedStyle.Render("Delete stash "+stashLabel(e)+"?") + "\n")
		b.WriteString("  " + dimStyle.Render(stashRowText(e, 0)) + "\n\n")
		b.WriteString("  " + dimStyle.Render("This permanently deletes these stashed changes — they are not in any commit,") + "\n")
		b.WriteString("  " + dimStyle.Render("and git cannot bring them back.") + "\n")
		b.WriteString("\n")
		b.WriteString(renderHelpRows(m.helpRows()))
		return m.styledBox(b.String())
	}

	// ── The list ───────────────────────────────────────
	if len(m.stashEntries) == 0 {
		// Reachable by emptying the stack from this very screen, so it has to
		// explain where entries come from rather than just say "none".
		b.WriteString("  " + dimStyle.Render("No stashes.") + "\n\n")
		b.WriteString("  " + dimStyle.Render("Branch switches and merges stash automatically when needed —") + "\n")
		b.WriteString("  " + dimStyle.Render("entries appear here if restoring fails.") + "\n")
	} else {
		rows := m.stashListRows()
		start, end := listWindow(len(m.stashEntries), m.stashScroll, rows)
		if start > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp, start)) + "\n")
		}
		for i := start; i < end; i++ {
			e := m.stashEntries[i]
			cursor := "  "
			if i == m.stashCursor {
				cursor = cursorStyle.Render(symCursor + " ")
			}
			b.WriteString(cursor + m.renderStashRow(e, i == m.stashCursor) + "\n")
		}
		if end < len(m.stashEntries) {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown, len(m.stashEntries)-end)) + "\n")
		}
		b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(
			plural(len(m.stashEntries), "stashed change", "stashed changes"))))
	}

	if m.stashApplying {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Restoring...") + "\n")
	}
	if m.stashDropping {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Deleting...") + "\n")
	}

	if note := m.renderStatusNote(); note != "" {
		b.WriteString("\n" + note + "\n")
	}
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return m.styledBox(b.String())
}

// stashRowText is one entry as plain text: "abc1234  2h ago  on feat — subject".
// Unstyled, so the confirmation can reuse it and the width can be measured.
// width 0 means "do not truncate".
func stashRowText(e git.StashEntry, width int) string {
	parts := []string{stashLabel(e)}
	if e.Age != "" {
		parts = append(parts, e.Age)
	}
	tail := ""
	if e.Branch != "" {
		tail = "on " + e.Branch
	}
	if e.Subject != "" {
		if tail != "" {
			tail += " " + symEmDash + " "
		}
		tail += e.Subject
	}
	if tail != "" {
		parts = append(parts, tail)
	}
	row := strings.Join(parts, "  ")
	if width > 0 {
		return truncateWidth(row, width)
	}
	return row
}

// renderStashRow colours one row: the SHA is what the recovery banners named,
// so it leads and it is the part that has to be findable at a glance.
func (m Model) renderStashRow(e git.StashEntry, active bool) string {
	// The box's inner text width, less the cursor column.
	width := m.width - 12
	if width < 20 {
		width = 20
	}

	sha := helpKeyStyle.Render(stashLabel(e))
	if active {
		sha = highlightStyle.Render(stashLabel(e))
	}
	rest := stashRowText(git.StashEntry{Age: e.Age, Branch: e.Branch, Subject: e.Subject},
		width-lipgloss.Width(sha)-2)
	style := dimStyle
	if active {
		style = inactiveStyle
	}
	if rest == "" {
		return sha
	}
	return sha + "  " + style.Render(rest)
}

// viewStashDiff is the preview pane: the same rendering the file diff viewer
// uses, against the same window helper, so the two screens scroll identically.
func (m Model) viewStashDiff() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	label := "Stash"
	if e, ok := m.stashSelected(); ok {
		label = "Stash " + stashRowText(e, 0)
	}
	b.WriteString(stepStyle.Render("  " + label))
	b.WriteString("\n\n")

	lines := strings.Split(m.stashDiff, "\n")
	start, end := listWindow(len(lines), m.stashDiffScroll, m.listRows())
	for i, line := range lines[start:end] {
		lineNum := dimStyle.Render(fmt.Sprintf("%4d ", start+i+1))
		b.WriteString("  " + lineNum + styleDiffLine(line) + "\n")
	}
	b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(
		fmt.Sprintf("Lines %d-%d of %d", start+1, end, len(lines)))))

	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return m.styledBox(b.String())
}
