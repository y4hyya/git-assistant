package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

type configItem struct {
	label    string
	key      string
	value    string
	readonly bool
	toggle   bool
}

func (m *Model) loadConfigItems() {
	global := m.configGlobal
	m.configItems = []configItem{
		{"User name", "user.name", git.GetConfigValue("user.name", global), false, false},
		{"User email", "user.email", git.GetConfigValue("user.email", global), false, false},
		{"Default branch", "init.defaultBranch", git.GetConfigValue("init.defaultBranch", global), false, false},
		{"Remote URL", "", git.GetRemoteURL(), true, false},
		{"GPG signing", "commit.gpgsign", git.GetConfigValue("commit.gpgsign", global), false, true},
		{"Editor", "core.editor", git.GetConfigValue("core.editor", global), false, false},
	}
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateConfig(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// Handle edit mode
	if m.configEditMode {
		switch keyMsg.String() {
		case "enter":
			value := m.configEditInput.Value()
			item := m.configItems[m.configCursor]
			if err := git.SetConfigValue(item.key, value, m.configGlobal); err != nil {
				m.err = err
			}
			m.configEditMode = false
			m.configEditInput.Blur()
			m.loadConfigItems()
			return m, nil
		case "esc":
			m.configEditMode = false
			m.configEditInput.Blur()
			return m, nil
		default:
			var cmd tea.Cmd
			m.configEditInput, cmd = m.configEditInput.Update(msg)
			return m, cmd
		}
	}

	switch keyMsg.String() {
	case "up", "k":
		if m.configCursor > 0 {
			m.configCursor--
		}
	case "down", "j":
		if m.configCursor < len(m.configItems)-1 {
			m.configCursor++
		}
	case "enter":
		item := m.configItems[m.configCursor]
		if item.readonly {
			return m, nil
		}
		if item.toggle {
			newVal := "true"
			if item.value == "true" {
				newVal = "false"
			}
			if err := git.SetConfigValue(item.key, newVal, m.configGlobal); err != nil {
				m.err = err
			}
			m.loadConfigItems()
			return m, nil
		}
		// Enter inline edit mode
		m.configEditMode = true
		m.configEditInput.SetValue(item.value)
		m.configEditInput.Focus()
		m.configEditInput.CursorEnd()
		return m, nil
	case "tab":
		m.configGlobal = !m.configGlobal
		m.loadConfigItems()
		return m, nil
	case "esc":
		m.step = stepMenu
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewConfig() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" Config "))
	b.WriteString("  ")
	b.WriteString(dimStyle.Render("git settings"))
	b.WriteString("\n\n")

	// Scope toggle
	localLabel := "Local"
	globalLabel := "Global"
	if m.configGlobal {
		localLabel = dimStyle.Render(localLabel)
		globalLabel = highlightStyle.Render(globalLabel)
	} else {
		localLabel = highlightStyle.Render(localLabel)
		globalLabel = dimStyle.Render(globalLabel)
	}
	b.WriteString(fmt.Sprintf("  Scope: %s %s %s\n\n", localLabel, dimStyle.Render("/"), globalLabel))

	// Config items
	for i, item := range m.configItems {
		cursor := "  "
		if i == m.configCursor {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		paddedLabel := fmt.Sprintf("%-16s", item.label)
		label := inactiveStyle.Render(paddedLabel)
		if i == m.configCursor {
			label = highlightStyle.Render(paddedLabel)
		}
		if item.readonly {
			label = dimStyle.Render(paddedLabel)
		}

		value := item.value
		if m.configEditMode && i == m.configCursor {
			// Show inline text input
			b.WriteString(fmt.Sprintf("%s%s %s\n", cursor, label, m.configEditInput.View()))
			continue
		}
		if value == "" {
			value = dimStyle.Render("not set")
		} else if item.toggle {
			if value == "true" {
				value = successStyle.Render("on")
			} else {
				value = dimStyle.Render("off")
			}
		} else {
			value = dimStyle.Render(value)
		}

		b.WriteString(fmt.Sprintf("%s%s %s\n", cursor, label, value))
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	if m.configEditMode {
		b.WriteString(renderHelp([]helpEntry{
			{"enter", "save"},
			{"esc", "cancel"},
		}))
	} else {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "navigate"},
			{"enter", "edit"},
			{"tab", "scope"},
			{"esc", "back"},
		}))
	}

	return m.styledBox(b.String())
}
