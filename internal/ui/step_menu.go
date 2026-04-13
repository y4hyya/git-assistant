package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

type menuItem struct {
	name string
	desc string
}

func (m Model) menuItems() []menuItem {
	changeCount := 0
	for _, f := range m.files {
		_ = f
		changeCount++
	}

	commitDesc := "no changes"
	if changeCount > 0 {
		commitDesc = fmt.Sprintf("%d changes", changeCount)
	}

	branchCount := len(git.GetAllBranches())

	return []menuItem{
		{"Commit", commitDesc},
		{"Branch", fmt.Sprintf("%d branches", branchCount)},
	}
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	items := m.menuItems()

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
		switch m.menuCursor {
		case 0: // Commit
			if len(m.files) == 0 {
				m.err = fmt.Errorf("nothing to commit — working tree clean")
				return m, nil
			}
			m.step = stepFiles
			m.cursor = 0
			m.fileScroll = 0
		case 1: // Branch
			m.branchEntries = git.GetAllBranches()
			m.branchCursor = 0
			m.branchScroll = 0
			m.branchStandalone = false
			m.step = stepBranch
		}
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewMenu() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))

	status := ""
	if len(m.files) == 0 {
		status = successStyle.Render("  clean")
	} else {
		status = modifiedStyle.Render(fmt.Sprintf("  %d changes", len(m.files)))
	}
	b.WriteString(status)
	b.WriteString("\n\n")

	// Menu items
	items := m.menuItems()
	for i, item := range items {
		cursor := "  "
		if i == m.menuCursor {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		name := inactiveStyle.Render(item.name)
		desc := dimStyle.Render(item.desc)
		if i == m.menuCursor {
			name = highlightStyle.Render(item.name)
		}

		// Dim "Commit" when no changes
		if i == 0 && len(m.files) == 0 {
			name = dimStyle.Render(item.name)
			desc = dimStyle.Render(item.desc)
		}

		b.WriteString(fmt.Sprintf("%s%-12s %s\n", cursor, name, desc))
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{symArrows, "navigate"},
		{"enter", "select"},
		{"q", "quit"},
	}))

	// Graph section
	graphSection := m.renderGraphSection()
	if graphSection != "" {
		b.WriteString("\n\n")
		b.WriteString(graphSection)
	}

	return m.styledBox(b.String())
}
