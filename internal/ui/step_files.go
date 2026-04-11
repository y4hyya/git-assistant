package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"git-assist/internal/git"
	"git-assist/internal/types"
)

type gitignoreResultMsg struct{ err error }

func fileStatusStyle(s types.FileStatus) lipgloss.Style {
	switch s {
	case types.StatusModified:
		return modifiedStyle
	case types.StatusAdded:
		return addedStyle
	case types.StatusDeleted:
		return deletedStyle
	case types.StatusRenamed:
		return renamedStyle
	case types.StatusUntracked:
		return untrackedStyle
	default:
		return modifiedStyle
	}
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateFiles(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// Handle undo confirmation
	if m.confirmUndo {
		switch keyMsg.String() {
		case "y":
			return m, doUndo()
		default:
			m.confirmUndo = false
			return m, nil
		}
	}

	// ── Gitignore mode ─────────────────────────────────
	if m.gitignoreMode {
		totalItems := len(m.files) + len(m.existingIgnored)

		switch keyMsg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < totalItems-1 {
				m.cursor++
			}
		case " ":
			if m.cursor < len(m.files) {
				// Toggle new file for gitignore
				m.files[m.cursor].Gitignored = !m.files[m.cursor].Gitignored
			} else {
				// Toggle existing gitignore entry for removal
				entry := m.existingIgnored[m.cursor-len(m.files)]
				m.removeIgnored[entry] = !m.removeIgnored[entry]
			}
		case "a":
			// Only toggle new files
			allIgnored := true
			for _, f := range m.files {
				if !f.Gitignored {
					allIgnored = false
					break
				}
			}
			for i := range m.files {
				m.files[i].Gitignored = !allIgnored
			}
		case "enter":
			var addPaths []string
			var cachedPaths []string
			for _, f := range m.files {
				if f.Gitignored {
					addPaths = append(addPaths, f.Path)
					if f.Status != types.StatusUntracked {
						cachedPaths = append(cachedPaths, f.Path)
					}
				}
			}
			var removePaths []string
			for entry, remove := range m.removeIgnored {
				if remove {
					removePaths = append(removePaths, entry)
				}
			}
			if len(addPaths) == 0 && len(removePaths) == 0 {
				return m, nil
			}
			m.gitignoreCached = cachedPaths
			return m, doGitignore(addPaths, cachedPaths, removePaths)
		case "g", "esc":
			for i := range m.files {
				m.files[i].Gitignored = false
			}
			m.removeIgnored = nil
			m.existingIgnored = nil
			m.gitignoreMode = false
		case "q":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}

	// ── Commit mode (default) ──────────────────────────
	switch keyMsg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.files)-1 {
			m.cursor++
		}
	case " ":
		m.files[m.cursor].Selected = !m.files[m.cursor].Selected
	case "g":
		m.existingIgnored = git.GetGitignoreEntries()
		m.removeIgnored = make(map[string]bool)
		m.gitignoreMode = true
	case "a":
		allSelected := true
		for _, f := range m.files {
			if !f.Selected {
				allSelected = false
				break
			}
		}
		for i := range m.files {
			m.files[i].Selected = !allSelected
		}
	case "u":
		m.confirmUndo = true
	case "enter":
		hasSelected := false
		for _, f := range m.files {
			if f.Selected {
				hasSelected = true
				break
			}
		}
		if !hasSelected {
			return m, nil
		}
		m.step = stepType
		m.cursor = 0
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

func doUndo() tea.Cmd {
	return func() tea.Msg {
		if err := git.UndoLastCommit(); err != nil {
			return undoResultMsg{err: err}
		}
		files, err := git.GetStatus()
		if err != nil {
			return undoResultMsg{err: err}
		}
		return undoResultMsg{files: files}
	}
}

func doGitignore(addPaths, cachedPaths, removePaths []string) tea.Cmd {
	return func() tea.Msg {
		if err := git.AddToGitignore(addPaths); err != nil {
			return gitignoreResultMsg{err: err}
		}
		if err := git.RemoveFromGitignore(removePaths); err != nil {
			return gitignoreResultMsg{err: err}
		}
		if err := git.RemoveCached(cachedPaths); err != nil {
			return gitignoreResultMsg{err: err}
		}
		return gitignoreResultMsg{}
	}
}

// ── View ────────────────────────────────────────────────

func (m Model) viewFiles() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render("⎇ " + m.branch))
	b.WriteString("\n")
	if m.gitignoreMode {
		b.WriteString(stepStyle.Render("  Step 1/5 · Select files to add .gitignore"))
	} else {
		b.WriteString(stepStyle.Render("  Step 1/5 · Select files to commit"))
	}
	b.WriteString("\n\n")

	// File list
	selected := 0
	ignored := 0
	for i, f := range m.files {
		// Cursor arrow
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		// Checkbox — depends on mode
		check := unselectedCheck.Render("○")
		if m.gitignoreMode {
			if f.Gitignored {
				check = gitignoreCheck.Render("●")
				ignored++
			} else if f.Selected {
				// Show commit selections as dimmed in gitignore mode
				check = dimmedCheck.Render("●")
			}
		} else {
			if f.Selected {
				check = selectedCheck.Render("●")
				selected++
			}
		}

		// Status badge
		status := fileStatusStyle(f.Status).Render(fmt.Sprintf("%-2s", f.Status.Symbol()))

		// File path (highlighted when cursor is on it)
		path := filePathStyle.Render(f.Path)
		if i == m.cursor {
			path = highlightStyle.Render(f.Path)
		}

		b.WriteString(fmt.Sprintf("%s%s  %s  %s\n", cursor, check, status, path))
	}

	// Existing gitignore entries (only in gitignore mode)
	if m.gitignoreMode && len(m.existingIgnored) > 0 {
		b.WriteString("\n  " + dimStyle.Render("Already in .gitignore:") + "\n\n")
		for j, entry := range m.existingIgnored {
			idx := len(m.files) + j

			cursor := "  "
			if idx == m.cursor {
				cursor = cursorStyle.Render("▸ ")
			}

			check := gitignoreCheck.Render("●")
			if m.removeIgnored[entry] {
				check = unselectedCheck.Render("○")
			}

			path := dimStyle.Render(entry)
			if idx == m.cursor {
				path = highlightStyle.Render(entry)
			}

			b.WriteString(fmt.Sprintf("%s%s      %s\n", cursor, check, path))
		}
	}

	// Counter
	if m.gitignoreMode {
		removing := 0
		for _, r := range m.removeIgnored {
			if r {
				removing++
			}
		}
		counter := fmt.Sprintf("%d to add", ignored)
		if removing > 0 {
			counter += fmt.Sprintf(" · %d to remove", removing)
		}
		b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(counter)))
	} else {
		b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(fmt.Sprintf("%d/%d selected", selected, len(m.files)))))
	}

	// Undo confirmation
	if m.confirmUndo {
		lastMsg := git.GetLastCommitMessage()
		if lastMsg != "" {
			b.WriteString("\n  " + dimStyle.Render("Last: "+lastMsg))
		}
		b.WriteString("\n  " + modifiedStyle.Render("Undo last commit? Changes will be kept.") + "\n")
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"y", "confirm"},
			{"any", "cancel"},
		}))
		return boxBorder.Render(b.String())
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + errorStyle.Render("Error: "+m.err.Error()) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	if m.gitignoreMode {
		b.WriteString(renderHelp([]helpEntry{
			{"↑↓", "navigate"},
			{"space", "toggle"},
			{"a", "all"},
			{"enter", "confirm"},
			{"g", "cancel"},
		}))
	} else {
		b.WriteString(renderHelp([]helpEntry{
			{"↑↓", "navigate"},
			{"space", "select"},
			{"g", "gitignore"},
			{"a", "all"},
			{"u", "undo"},
			{"enter", "next"},
			{"q", "quit"},
		}))
	}

	return boxBorder.Render(b.String())
}

// ── Helpers ─────────────────────────────────────────────

type helpEntry struct {
	key  string
	desc string
}

func renderHelp(entries []helpEntry) string {
	var parts []string
	for _, e := range entries {
		parts = append(parts, helpKeyStyle.Render(e.key)+" "+helpStyle.Render(e.desc))
	}
	return "  " + strings.Join(parts, "    ")
}
