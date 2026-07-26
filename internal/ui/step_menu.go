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

func (m Model) menuItems() []menuItem {
	// Detached HEAD: every other entry needs a branch. Commit and Amend would
	// write commits nothing points at, Push would try to create a remote branch
	// literally named "HEAD (detached)", and Sync has no upstream to measure
	// against. One way out is offered instead — the branch manager, where
	// switching to a branch is the cure.
	if m.detached {
		items := []menuItem{
			{"Branch", "switch to a branch to continue"},
		}
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

	items := []menuItem{
		{"Commit", commitDesc},
	}
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
func (m Model) canSyncMain() bool {
	return m.behindMain > 0 && m.mainRef != "" && !m.detached
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
	case "down", "j":
		if m.menuCursor < len(items)-1 {
			m.menuCursor++
		}
	case "enter":
		if m.menuCursor >= len(items) {
			return m, nil
		}
		// Dispatch by item name rather than index — the menu order
		// shifts when conditional entries (Amend, Connect to GitHub)
		// are present, so name-based dispatch is harder to break.
		switch items[m.menuCursor].name {
		case "Commit":
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
			m.refreshFiles()
			return m.startAmend(), nil
		case "Push":
			// Same step the wizard uses; enterPush reads what this particular
			// push would send and decides whether it has to be a force.
			m.enterPush(true)
			return m, nil
		case "Branch":
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
		if m.detached {
			return m, nil
		}
		if m.populateSyncDialog() && m.syncPullCurrent {
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

// ── View ────────────────────────────────────────────────

// viewMenu renders the dashboard within the rows styledBox will actually
// draw. Sections are assembled as whole blocks against a measured budget so
// that overflow drops a section, never the box's own bottom border — the old
// fixed "m.height - 14" guess ignored the fifth menu item, the error banner
// and the sync spinner, and the box lost its floor exactly when an error was
// on screen.
func (m Model) viewMenu() string {
	// ── Header ───────────────────────────────────────
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

	blocks := []string{head.String()}

	// Detached HEAD. Loud, and above everything else on the screen: a commit
	// made here belongs to no branch and the next checkout leaves it reachable
	// only through the reflog. The menu below is already down to its one exit.
	if m.detached {
		blocks = append(blocks,
			"  "+errorStyle.Render(symWarn+" detached HEAD — you're not on any branch; commits made here can be lost")+
				"\n  "+dimStyle.Render("Open Branch and switch to a branch to carry on."))
	}

	// One-shot success banner from the init flow. Cleared on next keypress
	// by the main Update handler, same lifecycle as m.err.
	if m.initSuccessMsg != "" {
		blocks = append(blocks, "  "+successStyle.Render(symDone+" "+m.initSuccessMsg))
	}

	// What the last operation did — merge, pull, delete, stashed switch,
	// .gitignore edit. All of those used to finish in complete silence.
	if note := m.renderStatusNote(); note != "" {
		blocks = append(blocks, note)
	}

	// ── Menu items ───────────────────────────────────
	items := m.menuItems()
	cursorIdx := clampMenuCursor(m.menuCursor, len(items))
	rows := make([]string, 0, len(items))
	for i, item := range items {
		cursor := "  "
		if i == cursorIdx {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		name := inactiveStyle.Render(item.name)
		desc := dimStyle.Render(item.desc)
		if i == cursorIdx {
			name = highlightStyle.Render(item.name)
		}

		// Dim "Commit" when no changes
		if i == 0 && len(m.files) == 0 {
			name = dimStyle.Render(item.name)
			desc = dimStyle.Render(item.desc)
		}

		rows = append(rows, fmt.Sprintf("%s%-12s %s", cursor, name, desc))
	}
	blocks = append(blocks, strings.Join(rows, "\n"))

	// Spinner for sync
	if m.branchMerging {
		blocks = append(blocks, "  "+m.spinner.View()+" "+dimStyle.Render("Syncing with "+m.mainLabel()+"..."))
	}

	// Error
	if m.err != nil {
		blocks = append(blocks, "  "+formatError(m.err))
	}

	// ── Fit the blocks to the rows we have ───────────
	budget := m.contentHeight()
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
	if help := renderHelpRows(m.helpRows()); help != "" {
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
