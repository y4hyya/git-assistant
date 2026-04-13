package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateScope(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			m.scope = strings.TrimSpace(m.scopeInput.Value())
			m.step = stepMessage
			m.msgInput.Focus()
			return m, nil
		case "esc":
			m.scope = ""
			m.scopeInput.Reset()
			m.step = stepType
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.scopeInput, cmd = m.scopeInput.Update(msg)
	return m, cmd
}

// ── View ────────────────────────────────────────────────

func (m Model) viewScope() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render("⎇ " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Add scope (optional)"))
	b.WriteString("\n\n")

	// Current type
	typeLabel := activeStyle.Render(m.commitType)
	if m.breaking {
		typeLabel += errorStyle.Render("!")
	}
	b.WriteString("  Type: " + typeLabel + "\n\n")

	// Input
	b.WriteString("  " + m.scopeInput.View() + "\n")

	// Live preview
	val := m.scopeInput.Value()
	prefix := m.commitType
	if val != "" {
		prefix += "(" + val + ")"
	}
	if m.breaking {
		prefix += "!"
	}
	b.WriteString("\n  " + previewStyle.Render("→ "+prefix+": ...") + "\n")

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "next (empty to skip)"},
		{"esc", "back"},
	}))

	return boxBorder.Render(b.String())
}
