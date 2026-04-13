package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"git-assist/internal/types"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateType(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	totalItems := len(types.CommitTypes) + 1 // +1 for "custom"

	switch keyMsg.String() {
	case "up", "k":
		if m.typeIdx > 0 {
			m.typeIdx--
		}
	case "down", "j":
		if m.typeIdx < totalItems-1 {
			m.typeIdx++
		}
	case "!":
		m.breaking = !m.breaking
	case "enter":
		if m.typeIdx == len(types.CommitTypes) {
			// Custom type
			m.step = stepCustom
			m.customInput.Focus()
			return m, nil
		}
		m.commitType = types.CommitTypes[m.typeIdx].Name
		m.step = stepScope
		m.scopeInput.Focus()
		return m, nil
	case "esc":
		m.step = stepFiles
		m.cursor = 0
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewType() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Choose commit type"))
	if m.breaking {
		b.WriteString("   " + errorStyle.Render("! BREAKING CHANGE"))
	}
	b.WriteString("\n\n")

	// Commit type list
	for i, ct := range types.CommitTypes {
		cursor := "  "
		if i == m.typeIdx {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		radio := inactiveStyle.Render(symUnselected)
		name := inactiveStyle.Render(ct.Name)
		desc := dimStyle.Render(ct.Description)
		if i == m.typeIdx {
			radio = activeStyle.Render(symSelected)
			name = activeStyle.Render(ct.Name)
			desc = lipgloss.NewStyle().Foreground(lightGray).Render(ct.Description)
		}

		b.WriteString(fmt.Sprintf("%s%s  %-12s %s\n", cursor, radio, name, desc))
	}

	// Custom option
	customIdx := len(types.CommitTypes)
	cursor := "  "
	if m.typeIdx == customIdx {
		cursor = cursorStyle.Render(symCursor + " ")
	}
	radio := inactiveStyle.Render(symUnselected)
	name := inactiveStyle.Render("custom")
	desc := dimStyle.Render("Write your own...")
	if m.typeIdx == customIdx {
		radio = activeStyle.Render(symSelected)
		name = activeStyle.Render("custom")
		desc = lipgloss.NewStyle().Foreground(lightGray).Render("Write your own...")
	}
	b.WriteString(fmt.Sprintf("%s%s  %-12s %s\n", cursor, radio, name, desc))

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{symArrows, "navigate"},
		{"enter", "select"},
		{"!", "breaking"},
		{"esc", "back"},
		{"q", "quit"},
	}))

	return boxBorder.Render(b.String())
}

// ── Custom type input ───────────────────────────────────

func (m Model) updateCustom(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			val := strings.TrimSpace(m.customInput.Value())
			if val != "" {
				m.commitType = val
				m.step = stepScope
				m.scopeInput.Focus()
				return m, nil
			}
			return m, nil
		case "esc":
			m.step = stepType
			m.customInput.Reset()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.customInput, cmd = m.customInput.Update(msg)
	return m, cmd
}

func (m Model) viewCustom() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Enter custom commit type"))
	b.WriteString("\n\n")

	b.WriteString("  " + m.customInput.View() + "\n")

	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "confirm"},
		{"esc", "back"},
	}))

	return boxBorder.Render(b.String())
}
