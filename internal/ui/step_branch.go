package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/git"
	"git-assist/internal/types"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Async commands ─────────────────────────────────────

func doSwitchBranch(name string, isRemote bool) tea.Cmd {
	return func() tea.Msg {
		stashed := false
		stashRef := ""
		dirty, err := git.HasUncommittedChanges()
		if err != nil {
			return branchSwitchResultMsg{err: err}
		}
		if dirty {
			ref, stashErr := git.StashChanges()
			if stashErr != nil {
				return branchSwitchResultMsg{err: stashErr}
			}
			stashed = true
			stashRef = ref
		}
		if err := git.SwitchBranch(name, isRemote); err != nil {
			if !stashed {
				return branchSwitchResultMsg{err: err}
			}
			// The stash was taken for a checkout that never happened, so the
			// changes belong right back where they were. If even that fails,
			// say where they went — dropping the ref here used to leave the
			// user's work in an unmentioned stash behind an unrelated error.
			if popErr := git.StashPop(); popErr != nil {
				git.CleanupFailedStashPop()
				return branchSwitchResultMsg{
					stashRef: stashRef,
					err: recoveryError{fmt.Errorf(
						"could not switch to %s (%v), and restoring your uncommitted changes also failed — the working tree was reset clean and nothing was lost. Your changes are in stash %s; recover with: git stash apply %s",
						name, err, stashRef, stashRef)},
				}
			}
			return branchSwitchResultMsg{err: fmt.Errorf(
				"%v. You are still on the same branch; your uncommitted changes were stashed and restored", err)}
		}
		stashConflict := false
		if stashed {
			if err := git.StashPop(); err != nil {
				// Clean up the conflicted state — stash stays in stack
				git.CleanupFailedStashPop()
				stashConflict = true
			}
		}
		return branchSwitchResultMsg{
			newBranch:     name,
			stashConflict: stashConflict,
			stashRef:      stashRef,
		}
	}
}

func doCreateBranch(name string) tea.Cmd {
	return func() tea.Msg {
		if err := git.CreateBranch(name); err != nil {
			return branchCreateResultMsg{err: err}
		}
		return branchCreateResultMsg{newBranch: name}
	}
}

func doDeleteBranch(name string, force bool) tea.Cmd {
	return func() tea.Msg {
		err := git.DeleteBranch(name, force)
		return branchDeleteResultMsg{err: err, name: name, forced: force}
	}
}

// doMergeBranch merges name into the current branch, auto-stashing a dirty
// working tree first. Without the stash git either refuses outright ("your
// local changes would be overwritten by merge") — which the target-picker flow
// hit *after* switching, stranding the user on the target with their edits
// relocated and no merge performed — or merges over a tree the user never
// meant to involve.
//
// Failure hands the stash back to the handler (it has to `git merge --abort`
// first, and only then does the tree sit where the stash applies cleanly);
// success restores it here.
func doMergeBranch(name string) tea.Cmd {
	return func() tea.Msg {
		stashed := false
		stashRef := ""
		dirty, err := git.HasUncommittedChanges()
		if err != nil {
			return branchMergeResultMsg{err: err, source: name}
		}
		if dirty {
			ref, stashErr := git.StashChanges()
			if stashErr != nil {
				return branchMergeResultMsg{err: stashErr, source: name}
			}
			stashed = true
			stashRef = ref
		}
		upToDate, err := git.MergeBranch(name)
		if err != nil {
			return branchMergeResultMsg{
				err:           err,
				conflictFiles: git.GetConflictFiles(),
				source:        name,
				stashed:       stashed,
				stashRef:      stashRef,
			}
		}
		if stashed {
			if popErr := git.StashPop(); popErr != nil {
				git.CleanupFailedStashPop()
				return branchMergeResultMsg{
					source:   name,
					merged:   true,
					upToDate: upToDate,
					err: recoveryError{fmt.Errorf(
						"merged %s, but restoring your uncommitted changes conflicted — the working tree was reset clean and nothing was lost. Your changes are in stash %s; recover with: git stash apply %s",
						name, stashRef, stashRef)},
				}
			}
			return branchMergeResultMsg{source: name, merged: true, upToDate: upToDate, stashRestored: true}
		}
		return branchMergeResultMsg{source: name, merged: true, upToDate: upToDate}
	}
}

// annotateErr appends a note to err while preserving whether it carries
// recovery instructions — a plain fmt.Errorf wrap would strip the marker and
// let the next arrow key wipe a stash SHA off the screen.
func annotateErr(err error, note string) error {
	wrapped := fmt.Errorf("%v %s", err, note)
	if isRecoveryError(err) {
		return recoveryError{wrapped}
	}
	return wrapped
}

// ── Update ─────────────────────────────────────────────

func (m Model) updateBranch(msg tea.Msg) (tea.Model, tea.Cmd) {
	// While an async op is in flight, block all input (the model.go
	// outer Update still handles ctrl+c before we get here, so quitting
	// remains possible). Spinner ticks are non-key messages and are
	// forwarded so the animation keeps running.
	if m.branchSwitching || m.branchMerging || m.branchCreating || m.branchDeleting {
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

	// ── Create mode ────────────────────────────────────
	if m.branchCreateMode {
		switch keyMsg.String() {
		case "enter":
			name := strings.TrimSpace(m.branchCreateInput.Value())
			if name == "" {
				return m, nil
			}
			// git owns the ref-name rules; ask it instead of guessing, then
			// lead with something a beginner can act on. Without this the
			// user saw a bare "fatal: 'my new feature' is not a valid branch
			// name" — formatError's hint for exactly this case never matched.
			if err := git.CheckRefFormatBranch(name); err != nil {
				m.err = fmt.Errorf("branch names can't contain spaces, ':', '..', or end with '/'\n  %v", err)
				return m, nil
			}
			m.branchCreating = true
			return m, tea.Batch(doCreateBranch(name), m.spinner.Tick)
		case "esc":
			m.branchCreateMode = false
			m.branchCreateInput.Reset()
			return m, nil
		default:
			var cmd tea.Cmd
			m.branchCreateInput, cmd = m.branchCreateInput.Update(msg)
			return m, cmd
		}
	}

	// ── Delete confirmation ────────────────────────────
	if m.branchDeleteMode {
		switch keyMsg.String() {
		case "y":
			m.branchDeleteMode = false
			if m.branchCursor >= len(m.branchEntries) {
				return m, nil
			}
			entry := m.branchEntries[m.branchCursor]
			m.branchDeleting = true
			return m, tea.Batch(doDeleteBranch(entry.Name, false), m.spinner.Tick)
		default:
			m.branchDeleteMode = false
			return m, nil
		}
	}

	// ── Force-delete confirmation ──────────────────────
	// Reached only when the safe delete came back with ErrBranchNotMerged:
	// the branch holds commits nothing else contains. Any other delete
	// failure surfaces as an error, as before.
	if m.branchForceDeleteMode {
		switch keyMsg.String() {
		case "y":
			name := m.branchForceDeleteName
			m.branchForceDeleteMode = false
			m.branchForceDeleteName = ""
			m.branchDeleting = true
			return m, tea.Batch(doDeleteBranch(name, true), m.spinner.Tick)
		default:
			m.branchForceDeleteMode = false
			m.branchForceDeleteName = ""
			return m, nil
		}
	}

	// ── Merge target picker ────────────────────────────
	if m.mergeTargetMode {
		switch keyMsg.String() {
		case "up", "k":
			if m.mergeTargetCursor > 0 {
				m.mergeTargetCursor--
			}
		case "down", "j":
			if m.mergeTargetCursor < len(m.mergeTargets)-1 {
				m.mergeTargetCursor++
			}
		case "enter":
			if len(m.mergeTargets) == 0 {
				// Detached HEAD or a single-branch repo leaves nothing to
				// merge into — the picker renders an empty-state line and
				// enter must not index the empty slice.
				return m, nil
			}
			target := m.mergeTargets[m.mergeTargetCursor]
			m.mergeTarget = target.Name
			m.mergeTargetMode = false
			m.branchMergeMode = true
		case "esc":
			m.mergeTargetMode = false
		}
		return m, nil
	}

	// ── Merge confirmation ─────────────────────────────
	if m.branchMergeMode {
		switch keyMsg.String() {
		case "y":
			m.branchMergeMode = false
			if m.mergeTarget == m.branch {
				// Already on target, merge directly
				m.branchMerging = true
				return m, tea.Batch(doMergeBranch(m.mergeSource), m.spinner.Tick)
			}
			// Switch to target first, then merge will trigger via
			// branchMergePending. Targets are always local branches (see the
			// "m" handler), so the remote-tracking checkout form never
			// applies here — passing true would create a stray local branch.
			m.branchMergePending = m.mergeSource
			m.branchSwitching = true
			return m, tea.Batch(doSwitchBranch(m.mergeTarget, false), m.spinner.Tick)
		default:
			m.branchMergeMode = false
			return m, nil
		}
	}

	// ── Default branch list ────────────────────────────
	switch keyMsg.String() {
	case "up", "k":
		if m.branchCursor > 0 {
			m.branchCursor--
		}
	case "down", "j":
		if m.branchCursor < len(m.branchEntries)-1 {
			m.branchCursor++
		}
	case "enter":
		if len(m.branchEntries) == 0 {
			return m, nil
		}
		entry := m.branchEntries[m.branchCursor]
		if entry.IsCurrent {
			return m, nil // already on this branch
		}
		m.branchSwitching = true
		return m, tea.Batch(doSwitchBranch(entry.Name, entry.IsRemote), m.spinner.Tick)
	case "c":
		m.branchCreateMode = true
		m.branchCreateInput.Focus()
		m.branchCreateInput.Reset()
		return m, nil
	case "d":
		if len(m.branchEntries) == 0 {
			return m, nil
		}
		entry := m.branchEntries[m.branchCursor]
		if entry.IsCurrent {
			m.err = fmt.Errorf("cannot delete the current branch")
			return m, nil
		}
		if entry.IsRemote {
			m.err = fmt.Errorf("cannot delete a remote branch")
			return m, nil
		}
		if entry.Name == "main" || entry.Name == "master" {
			m.err = fmt.Errorf("cannot delete %s — protected branch", entry.Name)
			return m, nil
		}
		m.branchDeleteMode = true
		return m, nil
	case "m":
		if len(m.branchEntries) == 0 {
			return m, nil
		}
		entry := m.branchEntries[m.branchCursor]
		// "m" picks the merge SOURCE only — including the branch you are
		// standing on, which is the most common merge there is ("merge my
		// feature into main"). Self-merge is structurally impossible: the
		// target list below excludes the source by name.
		//
		// For remote-only sources, store the full "origin/<name>" ref so
		// the eventual `git merge` resolves the right commit; otherwise a
		// bare name collides with any local branch sharing that name.
		if entry.IsRemote {
			m.mergeSource = "origin/" + entry.Name
		} else {
			m.mergeSource = entry.Name
		}
		// Targets are LOCAL branches only. Picking a remote-only branch used
		// to run `git checkout -b <n> origin/<n>` behind the user's back and
		// merge into that brand-new local branch, leaving origin untouched —
		// nothing like the "merge into the remote branch" it looked like.
		m.mergeTargets = nil
		defaultIdx := -1
		mainIdx := -1
		for _, e := range m.branchEntries {
			if e.IsRemote || e.Name == entry.Name {
				continue
			}
			if e.IsCurrent {
				defaultIdx = len(m.mergeTargets)
			}
			if mainIdx < 0 && (e.Name == "main" || e.Name == "master") {
				mainIdx = len(m.mergeTargets)
			}
			m.mergeTargets = append(m.mergeTargets, e)
		}
		// Preselect the branch you're on — except when it IS the source, in
		// which case "merge this into main" is the overwhelmingly likely intent.
		if defaultIdx < 0 {
			defaultIdx = max(mainIdx, 0)
		}
		m.mergeTargetCursor = defaultIdx
		m.mergeTargetMode = true
		return m, nil
	case "esc":
		if m.branchStandalone {
			m.quitting = true
			return m, tea.Quit
		}
		// The branch may have changed under us — resolve it first, since the
		// shared return path reads status and graphs against it.
		m.branch, _ = git.GetCurrentBranch()
		cmd := m.returnToMenu()
		return m, cmd
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	// Adjust scroll
	visible := m.height - 12
	if visible < 5 {
		visible = 5
	}
	if m.branchCursor < m.branchScroll {
		m.branchScroll = m.branchCursor
	}
	if m.branchCursor >= m.branchScroll+visible {
		m.branchScroll = m.branchCursor - visible + 1
	}

	return m, nil
}

// ── View ───────────────────────────────────────────────

func (m Model) viewBranch() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Branch Manager"))
	b.WriteString("\n\n")

	// ── Create mode ────────────────────────────────────
	if m.branchCreateMode {
		b.WriteString("  Create new branch from " + branchStyle.Render(m.branch) + "\n\n")
		b.WriteString("  " + m.branchCreateInput.View() + "\n")
		if m.branchCreating {
			b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Creating...") + "\n")
		}
		// Name validation lands here, so the error has to render on this
		// screen — the list below is never reached while create mode is on.
		if m.err != nil {
			b.WriteString("\n  " + formatError(m.err) + "\n")
		}
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"enter", "create"},
			{"esc", "cancel"},
		}))
		return m.styledBox(b.String())
	}

	// ── Delete confirmation ────────────────────────────
	if m.branchDeleteMode && m.branchCursor < len(m.branchEntries) {
		entry := m.branchEntries[m.branchCursor]
		b.WriteString("  " + modifiedStyle.Render("Delete branch "+entry.Name+"?") + "\n")
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"y", "confirm"},
			{"any", "cancel"},
		}))
		return m.styledBox(b.String())
	}

	// ── Force-delete confirmation ──────────────────────
	if m.branchForceDeleteMode {
		b.WriteString("  " + modifiedStyle.Render(symWarn+" "+m.branchForceDeleteName+
			" has commits not merged into any other branch") + "\n\n")
		b.WriteString("  " + highlightStyle.Render("Force delete?") + " " +
			dimStyle.Render("(y/N)") + "\n")
		b.WriteString("  " + dimStyle.Render("Those commits become unreachable — only `git reflog` can find them.") + "\n")
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"y", "force delete"},
			{"any", "cancel"},
		}))
		return m.styledBox(b.String())
	}

	// ── Merge target picker ─────────────────────────────
	if m.mergeTargetMode {
		b.WriteString("  Merge " + branchStyle.Render(m.mergeSource) + " into:\n\n")
		if len(m.mergeTargets) == 0 {
			b.WriteString("  " + dimStyle.Render("no other branch to merge into") + "\n")
		}
		for i, entry := range m.mergeTargets {
			cursor := "  "
			if i == m.mergeTargetCursor {
				cursor = cursorStyle.Render(symCursor + " ")
			}
			name := inactiveStyle.Render(entry.Name)
			if i == m.mergeTargetCursor {
				name = highlightStyle.Render(entry.Name)
			}
			// No "(remote)" case: the picker only ever holds local branches.
			label := ""
			if entry.IsCurrent {
				label = dimStyle.Render(" (current)")
			}
			b.WriteString(fmt.Sprintf("%s%s%s\n", cursor, name, label))
		}
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "navigate"},
			{"enter", "select"},
			{"esc", "cancel"},
		}))
		return m.styledBox(b.String())
	}

	// ── Merge confirmation ─────────────────────────────
	if m.branchMergeMode {
		b.WriteString("  " + branchStyle.Render(m.mergeSource) + " " + dimStyle.Render(symArrowRight) + " " + branchStyle.Render(m.mergeTarget) + "\n\n")
		if m.mergeTarget != m.branch {
			// Confirming does not just merge: it checks the target out first
			// and leaves you there. Saying so beats discovering it when the
			// next commit lands on a branch you didn't choose.
			b.WriteString("  " + modifiedStyle.Render(symWarn) + " " +
				highlightStyle.Render("switch to "+m.mergeTarget+" and merge "+m.mergeSource+" into it") + "\n")
			b.WriteString("  " + dimStyle.Render("You are on '"+m.branch+"' now and will stay on '"+m.mergeTarget+"' afterwards.") + "\n")
		} else {
			b.WriteString("  " + dimStyle.Render("This will bring all changes from") + "\n")
			b.WriteString("  " + dimStyle.Render("'"+m.mergeSource+"' into '"+m.mergeTarget+"'") + "\n")
		}
		b.WriteString("  " + dimStyle.Render("Creates a merge commit (--no-ff). Uncommitted changes are stashed and restored.") + "\n")
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"y", "confirm"},
			{"any", "cancel"},
		}))
		return m.styledBox(b.String())
	}

	// ── Branch list ────────────────────────────────────
	if len(m.branchEntries) == 0 {
		b.WriteString("  " + dimStyle.Render("No branches found") + "\n")
	} else {
		// Separate local and remote
		var localEnd int
		for i, e := range m.branchEntries {
			if e.IsRemote {
				localEnd = i
				break
			}
			localEnd = i + 1
		}

		// Scrolling
		visible := m.height - 12
		if visible < 5 {
			visible = 5
		}
		start := 0
		end := len(m.branchEntries)
		if len(m.branchEntries) > visible {
			start = m.branchScroll
			end = start + visible
			if end > len(m.branchEntries) {
				end = len(m.branchEntries)
			}
		}

		if start > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp, start)) + "\n")
		}

		for i := start; i < end; i++ {
			e := m.branchEntries[i]

			// Separator between local and remote
			if i == localEnd && i > start {
				b.WriteString("  " + dimStyle.Render("────────────────────") + "\n")
			}

			cursor := "  "
			if i == m.branchCursor {
				cursor = cursorStyle.Render(symCursor + " ")
			}

			indicator := symUnselected
			style := inactiveStyle
			if e.IsCurrent {
				indicator = symSelected
				style = activeStyle
			}

			name := style.Render(e.Name)
			if i == m.branchCursor {
				name = highlightStyle.Render(e.Name)
			}

			label := ""
			if e.IsCurrent {
				label = dimStyle.Render(" (current)")
			} else if e.IsRemote {
				label = dimStyle.Render(" (remote)")
			}

			b.WriteString(fmt.Sprintf("%s%s  %s%s\n", cursor, style.Render(indicator), name, label))
		}

		if end < len(m.branchEntries) {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown, len(m.branchEntries)-end)) + "\n")
		}

		b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(fmt.Sprintf("%d branches", len(m.branchEntries)))))
	}

	// Spinner
	if m.branchSwitching {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Switching...") + "\n")
	}
	if m.branchMerging {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Merging...") + "\n")
	}
	if m.branchDeleting {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Deleting...") + "\n")
	}

	// Post-operation note (branch created, merge result, stash disclosure, delete)
	if note := m.renderStatusNote(); note != "" {
		b.WriteString("\n" + note + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	entries := []helpEntry{
		{symArrows, "navigate"},
		{"enter", "switch"},
		{"c", "create"},
		{"d", "delete"},
		{"m", "merge"},
	}
	if m.branchStandalone {
		entries = append(entries, helpEntry{"q", "quit"})
	} else {
		entries = append(entries, helpEntry{"esc", "back"})
	}
	b.WriteString(renderHelp(entries))

	return m.styledBox(b.String())
}

// branchSeparatorIndex returns the index where remote branches start.
func branchSeparatorIndex(entries []types.BranchEntry) int {
	for i, e := range entries {
		if e.IsRemote {
			return i
		}
	}
	return len(entries)
}
