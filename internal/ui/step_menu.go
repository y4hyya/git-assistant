package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/git"
	"git-assist/internal/types"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type menuItem struct {
	name string
	desc string
}

// conflictMenuItem is the entry an unfinished merge puts at the top of the
// dashboard, in both the ordinary and the detached-HEAD list. It is permanent
// while MERGE_HEAD exists — the repository cannot be committed to, switched
// away from or pushed sensibly until it is dealt with, and the resolver is the
// only screen that can deal with it.
func (m Model) conflictMenuItem() []menuItem {
	if !m.mergeInProgress {
		return nil
	}
	return []menuItem{{"Resolve conflicts", symWarn + " merge in progress — finish it or undo it"}}
}

// mergeBlocked reports whether an entry that writes commits or moves HEAD has
// to refuse right now. Everything it gates would either corrupt the merge (the
// commit wizard's `git reset` unstages every resolution) or bury it (a checkout
// carrying MERGE_HEAD onto another branch).
func (m Model) mergeBlocked() bool { return m.mergeInProgress }

// mergeBlockedErr is the one sentence those entries refuse with. Advisory, not
// an error — nothing failed, the user simply has an earlier job to finish.
func mergeBlockedErr(what string) error {
	return fmt.Errorf("%s finish or abort the merge first — %s is not safe until then. Open \"Resolve conflicts\"", symWarn, what)
}

func (m Model) menuItems() []menuItem {
	// Detached HEAD: every other entry needs a branch. Commit and Amend would
	// write commits nothing points at, Push would try to create a remote branch
	// literally named "HEAD (detached)", and Sync has no upstream to measure
	// against. One way out is offered instead — the branch manager, where
	// switching to a branch is the cure.
	if m.detached {
		items := m.conflictMenuItem()
		items = append(items, menuItem{"Branch", "switch to a branch to continue"})
		// Stash is the exception to "every entry needs a branch": a stash
		// belongs to no branch, and a detached HEAD is precisely the state
		// where uncommitted work most needs a way back out.
		items = append(items, m.stashMenuItem()...)
		// History is the second exception, and for a sharper reason: it reads
		// HEAD, it changes nothing, and "which commit am I actually sitting on"
		// is the question a detached HEAD raises.
		items = append(items, m.historyMenuItem()...)
		return append(items, menuItem{"Config", "git settings"})
	}

	changeCount := 0
	for _, f := range m.files {
		_ = f
		changeCount++
	}

	commitDesc := "no changes"
	if changeCount > 0 {
		commitDesc = fmt.Sprintf("%d changes", changeCount)
	}

	items := m.conflictMenuItem()
	items = append(items, menuItem{"Commit", commitDesc})
	// Amend shows up only when there's a last commit to amend.
	if m.hasAnyCommit {
		items = append(items, menuItem{"Amend", "edit last commit"})
	}
	// Push, whenever there is something origin does not have. Without this
	// entry the push step was reachable ONLY in the seconds after a commit: a
	// push that was skipped, that failed, or that an amend made necessary could
	// not be run from inside the tool at all.
	if m.canPush() {
		items = append(items, menuItem{"Push", m.pushMenuDesc()})
	}
	items = append(items, menuItem{"Branch", fmt.Sprintf("%d branches", m.branchCount)})
	items = append(items, m.stashMenuItem()...)
	items = append(items, m.historyMenuItem()...)
	items = append(items, menuItem{"Config", "git settings"})
	// Recovery entry: when this local repo has no remote and `gh` is
	// available, offer to create the GitHub repo from here.
	if m.canConnectGH() {
		items = append(items, menuItem{"Connect to GitHub", "create repo + set origin via gh"})
	}
	return items
}

// stashMenuItem is the Stash entry, or nothing. Returned as a slice so both
// menuItems branches splice it in at the right place without repeating the
// condition — which is stashAvailable, the same one the `S` key and both help
// rows use. Hidden at zero on purpose: a beginner who has never stashed
// anything should not be asked to wonder what the word means.
func (m Model) stashMenuItem() []menuItem {
	if !m.stashAvailable() {
		return nil
	}
	return []menuItem{{"Stash", fmt.Sprintf("%d stashed", m.stashCount)}}
}

// historyMenuItem is the History entry, or nothing. Spliced in like the stash
// entry, gated on the same kind of condition (historyAvailable, i.e. the
// snapshot counted at least one commit): a repository with no commits has no
// history, and an empty browser is one more thing for a beginner to wonder at.
func (m Model) historyMenuItem() []menuItem {
	if !m.historyAvailable() {
		return nil
	}
	return []menuItem{{"History", plural(m.historyTotal, "commit", "commits")}}
}

// isConventionalType reports whether s can be a conventional-commit type:
// one word of letters, digits or hyphens. Nothing else — a type with a space
// in it is prose that happens to contain a colon.
func isConventionalType(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// parseConventionalSubject extracts the type, scope, breaking flag, and
// remaining text from a conventional-commit-format subject like
// "feat(auth)!: add login". Returns commitType="" if the input doesn't
// match — in that case `rest` contains the original subject untouched
// so the amend flow can fall back to raw mode with the original message
// preserved.
//
// The match has to be strict about what a type is. "Anything before the first
// colon" accepted "Update the README: add badges" as type="Update the README",
// a type no picker lists and no screen renders — the wizard opened with only
// "add badges" in the subject field and the first half of the user's own
// commit message was gone. Prose like that belongs on the raw amend path,
// which round-trips the subject byte-for-byte.
func parseConventionalSubject(subject string) (commitType, scope string, breaking bool, rest string) {
	idx := strings.Index(subject, ":")
	if idx <= 0 {
		return "", "", false, subject
	}
	prefix := subject[:idx]
	rest = strings.TrimSpace(subject[idx+1:])

	if strings.HasSuffix(prefix, "!") {
		breaking = true
		prefix = prefix[:len(prefix)-1]
	}
	if openParen := strings.Index(prefix, "("); openParen > 0 {
		if !strings.HasSuffix(prefix, ")") {
			return "", "", false, subject
		}
		cType := prefix[:openParen]
		cScope := prefix[openParen+1 : len(prefix)-1]
		if !isConventionalType(cType) || cScope == "" || strings.ContainsAny(cScope, "()") {
			return "", "", false, subject
		}
		return cType, cScope, breaking, rest
	}
	if !isConventionalType(prefix) {
		return "", "", false, subject
	}
	return prefix, "", breaking, rest
}

// clampMenuCursor keeps the menu cursor inside the item list. The list is
// built fresh on every pass and can shrink underneath the cursor: "Connect to
// GitHub" vanishes the instant the repo gains a remote, and a cursor left
// past the end highlights no row, makes "down" dead and turns "enter" into a
// no-op — with only "up" to escape.
func clampMenuCursor(cursor, n int) int {
	if cursor >= n {
		cursor = n - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	return cursor
}

// canSyncMain gates the "s" shortcut, its help entry and the behind-main badge
// on one condition, so the dashboard can never offer a sync it cannot perform
// (or hide one it should). mainRef is empty in a repository with no main
// branch at all, where there is nothing to sync with.
// A merge already in progress rules it out too: `s` starts a second merge,
// which git refuses outright, and the badge would be inviting the user to do it.
func (m Model) canSyncMain() bool {
	return m.behindMain > 0 && m.mainRef != "" && !m.detached && !m.mergeInProgress
}

// canPull gates the dashboard's `p` shortcut and its help entry on one
// condition, so the key and the row advertising it cannot disagree.
func (m Model) canPull() bool {
	return m.hasRemote && m.behindOrigin > 0 && !m.detached && !m.mergeInProgress
}

// mainLabel names the main branch for display. Falls back to "main" before the
// first dashboard snapshot has landed.
func (m Model) mainLabel() string {
	if m.mainName != "" {
		return m.mainName
	}
	return "main"
}

// canPush gates the dashboard's Push entry. Three states deserve it, and they
// are exactly the three the entry can describe: commits waiting to go out, a
// branch origin has never seen, and a local rewrite origin still contradicts.
// A branch that is merely BEHIND is not one of them — that is Pull's job.
func (m Model) canPush() bool {
	if !m.hasRemote || !m.hasAnyCommit || m.detached {
		return false
	}
	return m.aheadOrigin > 0 || !m.hasUpstream || m.historyRewritten
}

// pushMenuDesc says which of the three states the Push entry is offering, in
// priority order: a rewrite outranks a plain backlog, because the push it needs
// is a different (and louder) operation.
func (m Model) pushMenuDesc() string {
	switch {
	case m.historyRewritten:
		return symWarn + " force required (history rewritten)"
	case !m.hasUpstream:
		return "publish branch (no upstream yet)"
	default:
		return fmt.Sprintf("%s%d ahead", symArrowUp, m.aheadOrigin)
	}
}

// canConnectGH reports whether the recovery menu entry is applicable.
// Requires: we're in a git repo (always true on menu), no origin set, and
// the gh CLI is installed. Auth is checked later in the init flow.
func (m Model) canConnectGH() bool {
	return !m.hasRemote && git.HasGHCLI()
}

// startAmend pre-loads the commit wizard with the last commit's content so
// the user can edit message and/or stage additional files, then runs
// `git commit --amend` at the end.
//
// Conventional subjects are parsed so the type/scope/breaking flags survive
// the round-trip. Anything else — merge commits, "Initial commit", plain
// prose — enters raw mode: no type picker, no prefix, the subject goes back
// to git exactly as it came out. Forcing "<type>: " onto those subjects
// rewrote history the user never asked to change.
func (m Model) startAmend() Model {
	subject, body := git.GetLastCommitFull()
	m = m.applyAmendPrefill(subject, body)

	m.cursor = 0
	m.fileScroll = 0
	if len(m.files) == 0 {
		// Clean tree — the flagship amend case. There is nothing to stage, so
		// the file selector would be an empty list whose keys index into a
		// zero-length slice. Skip it: raw amends have no type to pick either.
		m.step = m.firstAmendStep()
		if m.step == stepMessage {
			m.msgInput.Focus()
		}
		return m
	}
	m.step = stepFiles
	return m
}

// applyAmendPrefill loads one commit's subject and body into the wizard
// inputs. Split out from startAmend so the parse/prefill rules can be
// exercised without a repo — and so the char-limit lift can never drift away
// from the SetValue calls it protects.
func (m Model) applyAmendPrefill(subject, body string) Model {
	cType, scope, breaking, rest := parseConventionalSubject(subject)

	// Start from a blank wizard so nothing from an earlier run leaks in.
	m.resetWizard()
	// Then lift the editor limits: SetValue silently truncates at CharLimit,
	// and a truncated prefill becomes a truncated commit the moment the user
	// confirms. resetWizard puts the everyday limits back afterwards.
	m.msgInput.CharLimit = 0
	m.bodyInput.CharLimit = 0

	if cType == "" {
		m.amendRaw = true
		m.msgInput.SetValue(subject)
	} else {
		m.typeIdx = len(types.CommitTypes) // "custom" unless it's a known type
		m.commitType = cType
		for i, ct := range types.CommitTypes {
			if ct.Name == cType {
				m.typeIdx = i
				break
			}
		}
		if m.typeIdx == len(types.CommitTypes) {
			// Conventional shape, unlisted type — pre-fill the custom input
			// so enter-through preserves the original prefix.
			m.customInput.SetValue(cType)
		}
		m.scope = scope
		m.breaking = breaking
		m.scopeInput.SetValue(scope)
		m.msgInput.SetValue(rest)
	}
	m.msgInput.CursorEnd()

	if body != "" {
		m.bodyInput.SetValue(body)
		m.showBody = true
	}

	m.amendMode = true
	return m
}

// firstAmendStep is the step the wizard enters once file selection is settled.
// Raw amends bypass the type picker entirely — there is no prefix to choose —
// while conventional ones keep the normal Type → Message order.
func (m Model) firstAmendStep() step {
	if m.amendRaw {
		return stepMessage
	}
	return stepType
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks while a background fetch is in progress.
	// Non-blocking — keypresses still route normally below, so the user
	// can navigate the menu while fetch runs.
	if m.fetching {
		if _, ok := msg.(spinner.TickMsg); ok {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	// Forward spinner during sync (blocking — input locked during merge)
	if m.branchMerging {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	items := m.menuItems()
	m.menuCursor = clampMenuCursor(m.menuCursor, len(items))

	switch keyMsg.String() {
	case "up", "k":
		if m.menuCursor > 0 {
			m.menuCursor--
		}
		m.followMenuCursor(len(items))
	case "down", "j":
		if m.menuCursor < len(items)-1 {
			m.menuCursor++
		}
		m.followMenuCursor(len(items))
	case "enter":
		if m.menuCursor >= len(items) {
			return m, nil
		}
		// Dispatch by item name rather than index — the menu order
		// shifts when conditional entries (Amend, Connect to GitHub)
		// are present, so name-based dispatch is harder to break.
		switch items[m.menuCursor].name {
		case "Resolve conflicts":
			if !m.openConflicts() {
				// The merge finished under us — a snapshot taken before it
				// landed can outlive it by a frame. Say so rather than
				// opening an empty resolver.
				m.setStatusNote("The merge is already finished — nothing left to resolve")
			}
			return m, nil
		case "Commit":
			if m.mergeBlocked() {
				// The wizard's first act is `git reset`, which throws away
				// every conflict resolution staged so far. Finishing the
				// merge IS the commit here, and it belongs to the resolver.
				m.err = mergeBlockedErr("committing")
				return m, nil
			}
			// Re-read the tree first: the dashboard stays open for hours and
			// the snapshot behind this gate goes stale the moment the user
			// edits a file in their editor or another terminal.
			m.refreshFiles()
			if len(m.files) == 0 {
				m.err = fmt.Errorf("nothing to commit — working tree clean")
				return m, nil
			}
			// Clears any leftovers (notably an abandoned amend's prefilled
			// type/scope/subject/body) so this run starts blank.
			m.resetWizard()
			m.step = stepFiles
		case "Amend":
			if m.mergeBlocked() {
				// `git commit --amend` mid-merge rewrites the commit BELOW the
				// merge and drags MERGE_HEAD along with it.
				m.err = mergeBlockedErr("amending")
				return m, nil
			}
			m.refreshFiles()
			return m.startAmend(), nil
		case "Push":
			if m.mergeBlocked() {
				m.err = mergeBlockedErr("pushing")
				return m, nil
			}
			// Same step the wizard uses; enterPush reads what this particular
			// push would send and decides whether it has to be a force.
			m.enterPush(true)
			return m, nil
		case "Branch":
			if m.mergeBlocked() {
				// A checkout carries the half-finished merge onto whatever
				// branch it lands on, and git refuses a second merge on top of
				// this one with a message about MERGE_HEAD.
				m.err = mergeBlockedErr("switching or merging branches")
				return m, nil
			}
			m.branchEntries = git.GetAllBranches()
			m.branchCursor = 0
			m.branchScroll = 0
			m.branchStandalone = false
			m.step = stepBranch
		case "Stash":
			m.enterStash()
			return m, nil
		case "History":
			// Returns a command: the first page is read off the UI thread like
			// everything else this screen dispatches.
			return m, m.enterHistory()
		case "Config":
			m.configCursor = 0
			m.configGlobal = false
			m.configEditMode = false
			m.loadConfigItems()
			m.step = stepConfig
		case "Connect to GitHub":
			if m.canConnectGH() {
				m.step = stepInit
				m.ghReuseMode = true
				m.initCursor = int(initChoiceGHCreate)
				m.initSuccessMsg = ""
				m.initNameInput.SetValue(git.CurrentDirName())
				m.initNameInput.CursorEnd()
				if !git.IsGHAuthed() {
					m.initPhase = initPhaseConfirmGHAuth
				} else {
					m.initNameInput.Focus()
					m.initPhase = initPhaseInputRepoName
				}
			}
		}
	case "s":
		if m.canSyncMain() {
			// Merge the exact ref m.behindMain was measured against — normally
			// origin/<main>, the local branch in a repo with no remote. Picking
			// the ref independently here is what let the badge count one thing
			// while "s" merged another: it could report "behind" forever
			// because every sync answered "Already up to date", or merge
			// origin/main in a repository that has no origin at all.
			//
			// git merge takes a remote-tracking ref directly, so doMergeBranch
			// covers both (and auto-stashes a dirty tree on the way).
			m.branchMerging = true
			return m, tea.Batch(doMergeBranch(m.mainRef), m.spinner.Tick)
		}
	case "S":
		// The loop-closer. Every recovery banner this app raises about an
		// orphaned auto-stash ends with "press S to open the stash manager", and
		// this is that key. Capital, because lowercase s already syncs with main
		// and both are live on this screen.
		if m.stashAvailable() {
			m.enterStash()
		}
		return m, nil
	case "p":
		// Manual pull fallback. Opens the sync dialog if there's anything
		// to pull — user may have skipped the startup dialog, or new
		// upstream commits appeared mid-session.
		//
		// Never mid-merge: a pull IS a merge, and git refuses to start one
		// while MERGE_HEAD exists. populateSyncDialog gates on the same flag,
		// so the startup auto-show cannot slip past this either.
		if m.detached || m.mergeInProgress {
			return m, nil
		}
		// syncRewriteHold opens the dialog with no pull on it: "behind" there
		// means origin is holding the commit this session removed, and the
		// dialog is where that gets explained. Doing nothing instead would make
		// `p` a dead key on a dashboard that is showing a behind badge.
		if m.populateSyncDialog() && (m.syncPullCurrent || m.syncRewriteHold) {
			m.syncReturnStep = stepMenu
			m.step = stepSync
		}
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── The dashboard's own list geometry ───────────────────
//
// The menu was the last list in the app with no scroll window. v1.3 grew it
// from four entries to nine (Resolve conflicts, Commit, Amend, Push, Branch,
// Stash, History, Config, Connect to GitHub) and styledBox simply cut whatever
// did not fit: at 24 rows with an error banner up, the bottom entries were
// invisible while `down` walked the cursor onto them and `enter` opened a
// screen nobody could see it had selected. Every other list here windows itself
// with "N more" markers; so does this one now.

// menuChrome returns the blocks that sit around the item list — above it and
// below it — plus the footer. View and the row budget below both read it, so
// the two cannot disagree about how much room the list has.
func (m Model) menuChrome() (above, below []string, help string) {
	var head strings.Builder
	head.WriteString(titleStyle.Render(" git-assist "))
	head.WriteString("  ")
	head.WriteString(branchStyle.Render(symBranch + " " + m.branch))

	if len(m.files) == 0 {
		head.WriteString(successStyle.Render("  clean"))
	} else {
		head.WriteString(modifiedStyle.Render(fmt.Sprintf("  %d changes", len(m.files))))
	}
	if m.behindMain > 0 {
		head.WriteString(modifiedStyle.Render(fmt.Sprintf("  %s%d behind %s", symArrowDown, m.behindMain, m.mainLabel())))
	}
	switch {
	case m.fetching:
		head.WriteString("  " + dimStyle.Render(m.spinner.View()+" syncing"))
	case m.fetchStale:
		// The last fetch failed, so everything remote on this screen dates from
		// the one before it. Said quietly and next to the numbers it qualifies:
		// a background fetch that could not reach the network is noise, not an
		// error, and it retries on the next return to the menu.
		head.WriteString("  " + dimStyle.Render("(offline — sync info may be stale)"))
	}
	above = []string{head.String()}

	// Detached HEAD. Loud, and above everything else on the screen: a commit
	// made here belongs to no branch and the next checkout leaves it reachable
	// only through the reflog. The menu below is already down to its one exit.
	if m.detached {
		above = append(above,
			"  "+errorStyle.Render(symWarn+" detached HEAD — you're not on any branch; commits made here can be lost")+
				"\n  "+dimStyle.Render("Open Branch and switch to a branch to carry on."))
	}

	// One-shot success banner from the init flow. Cleared on next keypress
	// by the main Update handler, same lifecycle as m.err.
	if m.initSuccessMsg != "" {
		above = append(above, "  "+successStyle.Render(symDone+" "+m.initSuccessMsg))
	}

	// What the last operation did — merge, pull, delete, stashed switch,
	// .gitignore edit. All of those used to finish in complete silence.
	if note := m.renderStatusNote(); note != "" {
		above = append(above, note)
	}

	// Spinner for sync
	if m.branchMerging {
		below = append(below, "  "+m.spinner.View()+" "+dimStyle.Render("Syncing with "+m.mainLabel()+"..."))
	}

	// Error. Below the list, and its rows are RESERVED before the list is sized
	// (see menuListRows): it used to be appended after a full-height list and
	// clipped away, so pressing enter on a dimmed entry produced no visible
	// feedback at all on a short terminal.
	if m.err != nil {
		below = append(below, "  "+formatError(m.err))
	}

	return above, below, renderHelpRows(m.helpRows())
}

// menuListRows is how many terminal rows the item list may occupy, indicators
// included. Everything else on the screen is reserved first — feedback is not
// what a short terminal should lose.
func (m Model) menuListRows() int {
	above, below, help := m.menuChrome()
	used := 0
	for _, blk := range above {
		used += lipgloss.Height(blk)
	}
	for _, blk := range below {
		used += lipgloss.Height(blk)
	}
	// One separator line per block boundary, and the footer's own blank line.
	used += len(above) + len(below)
	if help != "" {
		used += lipgloss.Height(help) + 1
	}
	rows := m.contentHeight() - used
	if rows < 1 {
		rows = 1
	}
	return rows
}

// menuWindow picks the visible slice of the item list. maxLines is the whole
// budget, "N more" markers included — each costs a row, so a list that needs
// both shows two fewer entries.
func menuWindow(total, cursor, scroll, maxLines int) (start, end int) {
	if maxLines < 1 {
		maxLines = 1
	}
	if total <= maxLines {
		return 0, total
	}
	rows := maxLines - 1 // at least one marker is going to be drawn
	if rows < 1 {
		rows = 1
	}
	fit := func(rows int) (int, int) {
		s := scroll
		// The cursor is never off-window: a menu whose highlighted row is not
		// drawn is one where enter opens something the user cannot see.
		if cursor < s {
			s = cursor
		}
		if cursor >= s+rows {
			s = cursor - rows + 1
		}
		if s > total-rows {
			s = total - rows
		}
		if s < 0 {
			s = 0
		}
		return s, min(total, s+rows)
	}
	start, end = fit(rows)
	if start > 0 && end < total && maxLines > 2 {
		// Both markers are needed, so the list gets one row less.
		start, end = fit(maxLines - 2)
	}
	return start, end
}

// followMenuCursor keeps the scroll offset on the window the view will draw.
func (m *Model) followMenuCursor(total int) {
	start, _ := menuWindow(total, m.menuCursor, m.menuScroll, m.menuListRows())
	m.menuScroll = start
}

// ── View ────────────────────────────────────────────────

// viewMenu renders the dashboard within the rows styledBox will actually
// draw. Sections are assembled as whole blocks against a measured budget so
// that overflow drops a section, never the box's own bottom border — the old
// fixed "m.height - 14" guess ignored the fifth menu item, the error banner
// and the sync spinner, and the box lost its floor exactly when an error was
// on screen.
func (m Model) viewMenu() string {
	above, below, help := m.menuChrome()
	blocks := above

	// ── Menu items ───────────────────────────────────
	items := m.menuItems()
	cursorIdx := clampMenuCursor(m.menuCursor, len(items))
	budget := m.menuListRows()
	start, end := menuWindow(len(items), cursorIdx, m.menuScroll, budget)
	// Markers cost a row each and menuWindow has already left room for them —
	// except on a budget too small to hold even one, where the row the cursor is
	// on wins. Spending the last row on "3 more" would be reporting the clipping
	// instead of avoiding it.
	spare := budget - (end - start)
	showUp := start > 0 && spare > 0
	if showUp {
		spare--
	}
	showDown := end < len(items) && spare > 0

	rows := make([]string, 0, len(items))
	if showUp {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp, start)))
	}
	for i := start; i < end; i++ {
		item := items[i]
		cursor := "  "
		if i == cursorIdx {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		name := inactiveStyle.Render(item.name)
		desc := dimStyle.Render(item.desc)
		if i == cursorIdx {
			name = highlightStyle.Render(item.name)
		}

		// Dim "Commit" when no changes. By NAME, not by index: an unfinished
		// merge puts its own entry at the top, and dimming that one would fade
		// out the only thing on the screen that has to be acted on.
		if item.name == "Commit" && len(m.files) == 0 {
			name = dimStyle.Render(item.name)
			desc = dimStyle.Render(item.desc)
		}
		// The merge entry is the loud one for as long as it exists.
		if item.name == "Resolve conflicts" {
			desc = modifiedStyle.Render(item.desc)
		}

		rows = append(rows, fmt.Sprintf("%s%-12s %s", cursor, name, desc))
	}
	if showDown {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown, len(items)-end)))
	}
	blocks = append(blocks, strings.Join(rows, "\n"))
	blocks = append(blocks, below...)

	// ── Fit the blocks to the rows we have ───────────
	budget = m.contentHeight()
	used := 0
	for _, blk := range blocks {
		used += lipgloss.Height(blk)
	}

	// Blank lines between blocks are the first thing to go.
	sep := "\n"
	if gaps := len(blocks) - 1; used+gaps <= budget {
		sep = "\n\n"
		used += gaps
	}
	out := strings.Join(blocks, sep)

	// Help bar: a blank line plus its row(s), dropped whole when they don't fit.
	// The keys come from menuHelp, which the `?` overlay renders too.
	if help != "" {
		if cost := lipgloss.Height(help) + 1; used+cost <= budget {
			out += "\n\n" + help
			used += cost
		}
	}

	// Graph section: takes whatever is left after its own blank line, and
	// hides itself when that isn't enough for a useful panel.
	if graph := m.renderGraphSection(budget - used - 1); graph != "" {
		out += "\n\n" + graph
	}

	return m.styledBox(out)
}
