package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
	"git-assist/internal/types"
)

// ── Async commands ─────────────────────────────────────

func doSwitchBranch(name string, isRemote bool) tea.Cmd {
	return func() tea.Msg {
		stashed := false
		if git.HasUncommittedChanges() {
			if err := git.StashChanges(); err != nil {
				return branchSwitchResultMsg{err: err}
			}
			stashed = true
		}
		if err := git.SwitchBranch(name, isRemote); err != nil {
			// Try to restore stash if switch failed
			if stashed {
				git.StashPop()
			}
			return branchSwitchResultMsg{err: err}
		}
		stashConflict := false
		if stashed {
			if err := git.StashPop(); err != nil {
				// Clean up the conflicted state — stash stays in stack
				git.CleanupFailedStashPop()
				stashConflict = true
			}
		}
		return branchSwitchResultMsg{newBranch: name, stashConflict: stashConflict}
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

func doDeleteBranch(name string) tea.Cmd {
	return func() tea.Msg {
		err := git.DeleteBranch(name)
		return branchDeleteResultMsg{err: err}
	}
}

func doMergeBranch(name string) tea.Cmd {
	return func() tea.Msg {
		if err := git.MergeBranch(name); err != nil {
			conflicts := git.GetConflictFiles()
			return branchMergeResultMsg{err: err, conflictFiles: conflicts}
		}
		return branchMergeResultMsg{}
	}
}

// ── Update ─────────────────────────────────────────────

func (m Model) updateBranch(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks during async operations
	if m.branchSwitching || m.branchMerging {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// ── Conflict view ──────────────────────────────────
	if m.branchConflict {
		switch keyMsg.String() {
		case "a":
			git.MergeAbort()
			m.branchConflict = false
			m.branchConflFiles = nil
			m.branchEntries = git.GetAllBranches()
		case "q":
			m.branchConflict = false
			m.branchConflFiles = nil
		}
		return m, nil
	}

	// ── Create mode ────────────────────────────────────
	if m.branchCreateMode {
		switch keyMsg.String() {
		case "enter":
			name := strings.TrimSpace(m.branchCreateInput.Value())
			if name != "" {
				return m, doCreateBranch(name)
			}
			return m, nil
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
			entry := m.branchEntries[m.branchCursor]
			m.branchDeleteMode = false
			return m, doDeleteBranch(entry.Name)
		default:
			m.branchDeleteMode = false
			return m, nil
		}
	}

	// ── Merge confirmation ─────────────────────────────
	if m.branchMergeMode {
		switch keyMsg.String() {
		case "y":
			entry := m.branchEntries[m.branchCursor]
			m.branchMergeMode = false
			m.branchMerging = true
			return m, tea.Batch(doMergeBranch(entry.Name), m.spinner.Tick)
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
		m.branchDeleteMode = true
		return m, nil
	case "m":
		if len(m.branchEntries) == 0 {
			return m, nil
		}
		entry := m.branchEntries[m.branchCursor]
		if entry.IsCurrent {
			m.err = fmt.Errorf("cannot merge a branch into itself")
			return m, nil
		}
		m.branchMergeMode = true
		return m, nil
	case "esc":
		if m.branchStandalone {
			m.quitting = true
			return m, tea.Quit
		}
		// Return to menu, refresh files + branch
		m.step = stepMenu
		m.branch, _ = git.GetCurrentBranch()
		freshFiles, _ := git.GetStatus()
		m.files = freshFiles
		m.cursor = 0
		m.fileScroll = 0
		return m, nil
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

	// ── Conflict view ──────────────────────────────────
	if m.branchConflict {
		b.WriteString("  " + errorStyle.Render("Merge conflicts detected") + "\n\n")
		for _, f := range m.branchConflFiles {
			b.WriteString("    " + errorStyle.Render("U") + "  " + filePathStyle.Render(f) + "\n")
		}
		b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(fmt.Sprintf("%d conflicting file(s)", len(m.branchConflFiles)))))
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"a", "abort merge"},
			{"q", "close"},
		}))
		return m.styledBox(b.String())
	}

	// ── Create mode ────────────────────────────────────
	if m.branchCreateMode {
		b.WriteString("  Create new branch from " + branchStyle.Render(m.branch) + "\n\n")
		b.WriteString("  " + m.branchCreateInput.View() + "\n")
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

	// ── Merge confirmation ─────────────────────────────
	if m.branchMergeMode && m.branchCursor < len(m.branchEntries) {
		entry := m.branchEntries[m.branchCursor]
		b.WriteString("  " + modifiedStyle.Render("Merge "+entry.Name+" into "+m.branch+"?") + "\n")
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
