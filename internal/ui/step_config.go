package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
)

type configItem struct {
	label    string
	key      string
	value    string
	set      bool // false = key not configured (distinct from value == "")
	readonly bool
	toggle   bool
	pick     bool
	remote   bool // special-case: edits origin URL via git remote, not git config
}

// configValue is a small wrapper around git.GetConfigValue that collapses
// errors and unset into the same display value. The set flag is preserved
// so the view can render "not set" hints; actual errors are silently
// treated as "not set" because the editor is not the right place to
// surface a config-read failure.
func configValue(key string, global bool) (value string, set bool) {
	v, s, _ := git.GetConfigValue(key, global)
	return v, s
}

func (m *Model) loadConfigItems() {
	global := m.configGlobal
	name, nameSet := configValue("user.name", global)
	email, emailSet := configValue("user.email", global)
	defBranch, defBranchSet := configValue("init.defaultBranch", global)
	gpgsign, gpgsignSet := configValue("commit.gpgsign", global)
	editor, editorSet := configValue("core.editor", global)
	remoteURL := git.GetRemoteURL()
	m.configItems = []configItem{
		{"User name", "user.name", name, nameSet, false, false, false, false},
		{"User email", "user.email", email, emailSet, false, false, false, false},
		{"Default branch", "init.defaultBranch", defBranch, defBranchSet, false, false, true, false},
		{"Remote URL", "", remoteURL, remoteURL != "", false, false, false, true},
		{"GPG signing", "commit.gpgsign", gpgsign, gpgsignSet, false, true, false, false},
		{"Editor", "core.editor", editor, editorSet, false, false, false, false},
	}
}

// ── Update ──────────────────────────────────────────────

// scopeName is what the current scope toggle is called on screen.
func (m Model) scopeName() string {
	if m.configGlobal {
		return "global"
	}
	return "local"
}

func (m Model) updateConfig(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// Remove-origin confirmation. Submitting an empty Remote URL used to delete
	// origin on the spot: push vanished from the wizard and the menu started
	// offering "Connect to GitHub", with nothing on screen having asked.
	if m.configRemoveRemote {
		switch keyMsg.String() {
		case "y":
			m.configRemoveRemote = false
			m.configRemoveURL = ""
			if err := git.RemoveOriginRemote(); err != nil {
				m.err = err
			}
			m.loadConfigItems()
			m.hasRemote = git.HasRemote()
		default:
			m.configRemoveRemote = false
			m.configRemoveURL = ""
		}
		return m, nil
	}

	// Handle pick mode (branch selector)
	if m.configPickMode {
		switch keyMsg.String() {
		case "up", "k":
			if m.configPickCursor > 0 {
				m.configPickCursor--
			}
		case "down", "j":
			if m.configPickCursor < len(m.configPickItems)-1 {
				m.configPickCursor++
			}
		case "enter":
			if len(m.configPickItems) == 0 {
				// Zero-commit repo: `git branch` lists nothing, so there is
				// nothing to pick. Close the picker instead of indexing an
				// empty slice.
				m.configPickMode = false
				return m, nil
			}
			selected := m.configPickItems[m.configPickCursor]
			item := m.configItems[m.configCursor]
			if err := git.SetConfigValue(item.key, selected, m.configGlobal); err != nil {
				m.err = err
			}
			m.configPickMode = false
			m.loadConfigItems()
			return m, nil
		case "esc":
			m.configPickMode = false
			return m, nil
		}
		return m, nil
	}

	// Handle edit mode
	if m.configEditMode {
		switch keyMsg.String() {
		case "enter":
			value := strings.TrimSpace(m.configEditInput.Value())
			item := m.configItems[m.configCursor]
			m.configEditMode = false
			m.configEditInput.Blur()
			if item.remote {
				// Remote URL is per-repo (ignores the scope toggle).
				if value == "" {
					if !item.set {
						return m, nil // nothing configured, nothing to remove
					}
					// Deleting the remote is not something to infer from an
					// empty field — ask first.
					m.configRemoveRemote = true
					m.configRemoveURL = item.value
					return m, nil
				}
				if err := git.AddOriginRemote(value); err != nil {
					m.err = err
				}
				m.loadConfigItems()
				// Remote URL change affects menu banner / push availability.
				m.hasRemote = git.HasRemote()
				return m, nil
			}
			// Clearing a field means UNSET, not "set to the empty string".
			// `git config user.name ""` shadows the global value with an empty
			// one, and `git commit` then dies with "empty ident name" while the
			// editor cheerfully shows the key as set-but-blank.
			if value == "" {
				if item.set {
					if err := git.UnsetConfigValue(item.key, m.configGlobal); err != nil {
						m.err = err
					}
				}
			} else if err := git.SetConfigValue(item.key, value, m.configGlobal); err != nil {
				m.err = err
			}
			m.loadConfigItems()
			return m, nil
		case "tab":
			// Scope switching mid-edit is ambiguous — writing the half-typed
			// value to the scope being left is wrong, writing it to the one
			// being entered is worse. Cancel the edit, then switch, and say so.
			m.configEditMode = false
			m.configEditInput.Blur()
			m.configGlobal = !m.configGlobal
			m.loadConfigItems()
			m.err = fmt.Errorf("%s edit cancelled — now showing %s config", symWarn, m.scopeName())
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
			// git accepts true/yes/on/1 (any case) as "on". Comparing the raw
			// string to "true" reported a repo with commit.gpgsign = 1 as off —
			// while git was signing every commit — and made the first press a
			// no-op that wrote the value it already had. Read with git's rules,
			// write the canonical spelling.
			newVal := "true"
			if git.ConfigBool(item.value) {
				newVal = "false"
			}
			if err := git.SetConfigValue(item.key, newVal, m.configGlobal); err != nil {
				m.err = err
			}
			m.loadConfigItems()
			return m, nil
		}
		if item.pick {
			// Load local branch names for the picker
			entries := git.GetAllBranches()
			m.configPickItems = nil
			for _, e := range entries {
				if !e.IsRemote {
					m.configPickItems = append(m.configPickItems, e.Name)
				}
			}
			m.configPickCursor = 0
			// Pre-select the current value if it exists
			for i, name := range m.configPickItems {
				if name == item.value {
					m.configPickCursor = i
					break
				}
			}
			m.configPickMode = true
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
		cmd := m.returnToMenu()
		return m, cmd
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewConfig() string {
	var b strings.Builder

	// Header — the same one every other screen draws. This was the only screen
	// without the app title and the branch, and it is the screen whose Local
	// scope edits that very repository.
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Config — git settings"))
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
		if m.configPickMode && i == m.configCursor {
			// Show inline branch picker
			b.WriteString(fmt.Sprintf("%s%s\n", cursor, label))
			if len(m.configPickItems) == 0 {
				b.WriteString("     " + dimStyle.Render("(no branches yet)") + "\n")
				continue
			}
			for pi, name := range m.configPickItems {
				pickCursor := "     "
				nameStyle := dimStyle.Render(name)
				if pi == m.configPickCursor {
					pickCursor = "   " + cursorStyle.Render(symCursor+" ")
					nameStyle = highlightStyle.Render(name)
				}
				b.WriteString(fmt.Sprintf("%s%s\n", pickCursor, nameStyle))
			}
			continue
		}
		if !item.set {
			value = dimStyle.Render("not set")
		} else if item.toggle {
			// git's own bool rules — see the toggle handler.
			if git.ConfigBool(value) {
				value = successStyle.Render("on")
			} else {
				value = dimStyle.Render("off")
			}
		} else {
			value = dimStyle.Render(value)
		}

		b.WriteString(fmt.Sprintf("%s%s %s\n", cursor, label, value))
	}

	// Remove-origin confirmation
	if m.configRemoveRemote {
		b.WriteString("\n  " + modifiedStyle.Render("Remove remote origin?") + "\n")
		b.WriteString("  " + dimStyle.Render(m.configRemoveURL) + "\n")
		b.WriteString("  " + dimStyle.Render("Pushing disappears from the wizard until a remote is set again.") + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	if m.configEditMode && !m.configPickMode && !m.configRemoveRemote {
		// Not a key, a consequence: an empty field UNSETS the key rather than
		// writing an empty value.
		b.WriteString("\n")
		b.WriteString("  " + dimStyle.Render("Clearing the field unsets the key.") + "\n")
	}

	return m.styledBox(b.String())
}
