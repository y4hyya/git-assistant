package ui

import (
	"errors"
	"fmt"
	"os"
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

	// ── Edit mode ──────────────────────────────────────
	if m.editMode {
		if m.saving {
			return m, nil
		}

		// Unsaved changes prompt
		if m.confirmExit {
			switch keyMsg.String() {
			case "y":
				m.confirmExit = false
				m.editMode = false
				m.editDirty = false
				return m, nil
			default:
				m.confirmExit = false
				return m, nil
			}
		}

		switch keyMsg.String() {
		case "ctrl+s":
			m.saving = true
			var fileStatus types.FileStatus
			for _, f := range m.files {
				if f.Path == m.diffFile {
					fileStatus = f.Status
					break
				}
			}
			return m, doSave(m.diffFile, m.editArea.Value(), fileStatus)
		case "esc":
			if m.editDirty {
				m.confirmExit = true
				return m, nil
			}
			m.editMode = false
			return m, nil
		default:
			var cmd tea.Cmd
			prevValue := m.editArea.Value()
			m.editArea, cmd = m.editArea.Update(msg)
			if m.editArea.Value() != prevValue {
				m.editDirty = true
			}
			return m, cmd
		}
	}

	// ── Filter mode ───────────────────────────────────
	if m.filterMode {
		switch keyMsg.String() {
		case "up":
			if m.filterCursor > 0 {
				m.filterCursor--
			}
		case "down":
			if m.filterCursor < len(m.filterMatches)-1 {
				m.filterCursor++
			}
		case "tab":
			if len(m.filterMatches) > 0 {
				idx := m.filterMatches[m.filterCursor]
				m.files[idx].Selected = !m.files[idx].Selected
			}
		case "enter":
			if len(m.filterMatches) > 0 {
				m.cursor = m.filterMatches[m.filterCursor]
			}
			m.filterMode = false
			m.filterInput.Reset()
		case "esc":
			m.filterMode = false
			m.filterInput.Reset()
		default:
			var cmd tea.Cmd
			prevValue := m.filterInput.Value()
			m.filterInput, cmd = m.filterInput.Update(msg)
			if m.filterInput.Value() != prevValue {
				m.filterMatches = computeFilterMatches(m.files, m.filterInput.Value())
				if m.filterCursor >= len(m.filterMatches) {
					m.filterCursor = max(0, len(m.filterMatches)-1)
				}
			}
			return m, cmd
		}
		return m, nil
	}

	// ── Diff preview mode ──────────────────────────────
	if m.showDiff {
		diffLines := strings.Split(m.diffContent, "\n")
		maxScroll := len(diffLines) - 1
		if maxScroll < 0 {
			maxScroll = 0
		}

		// Visible lines based on terminal height (box padding + header + footer)
		visible := m.height - 13
		if visible < 5 {
			visible = 5
		}

		switch keyMsg.String() {
		case "up", "k":
			if m.diffScroll > 0 {
				m.diffScroll--
			}
		case "down", "j":
			if m.diffScroll < maxScroll-visible {
				m.diffScroll++
			}
		case "e":
			// Block edit for deleted files and files not on disk
			currentFile := m.files[m.cursor]
			if currentFile.Status == types.StatusDeleted {
				return m, nil
			}
			if _, err := os.Stat(m.diffFile); err != nil {
				return m, nil
			}
			content, err := git.ReadFileContent(m.diffFile)
			if err != nil {
				m.err = err
				return m, nil
			}
			m.editArea.SetValue(content)
			// Size the textarea to fill available space
			w := m.width - 10
			if w < 40 {
				w = 40
			}
			h := m.height - 10
			if h < 5 {
				h = 5
			}
			m.editArea.SetWidth(w)
			m.editArea.SetHeight(h)
			m.editArea.Focus()
			m.editMode = true
			m.editDirty = false
			return m, nil
		case "esc":
			m.showDiff = false
			m.diffContent = ""
			m.diffFile = ""
			m.diffScroll = 0
			return m, nil
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
	case "d":
		f := m.files[m.cursor]
		diff, err := git.GetFileDiff(f.Path, f.Status)
		if err != nil {
			if errors.Is(err, git.ErrBinaryFile) {
				m.diffContent = ""
				m.diffFile = f.Path
				m.showDiff = true
				return m, nil
			}
			m.err = err
			return m, nil
		}
		m.diffContent = diff
		m.diffFile = f.Path
		m.diffScroll = 0
		m.showDiff = true
	case "/":
		m.filterMode = true
		m.filterInput.Focus()
		m.filterInput.Reset()
		m.filterMatches = computeFilterMatches(m.files, "")
		m.filterCursor = 0
	case "b":
		m.branchEntries = git.GetAllBranches()
		m.branchCursor = 0
		m.branchScroll = 0
		m.branchStandalone = false
		m.step = stepBranch
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

	// Adjust scroll to keep cursor visible
	if !m.gitignoreMode {
		visible := m.height - 13
		if visible < 5 {
			visible = 5
		}
		if m.cursor < m.fileScroll {
			m.fileScroll = m.cursor
		}
		if m.cursor >= m.fileScroll+visible {
			m.fileScroll = m.cursor - visible + 1
		}
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

func doSave(path, content string, status types.FileStatus) tea.Cmd {
	return func() tea.Msg {
		if err := git.WriteFileContent(path, content); err != nil {
			return saveResultMsg{err: err}
		}
		files, err := git.GetStatus()
		if err != nil {
			return saveResultMsg{err: err}
		}
		diff, _ := git.GetFileDiff(path, status)
		return saveResultMsg{files: files, diff: diff}
	}
}

// ── View ────────────────────────────────────────────────

func (m Model) viewFiles() string {
	// ── Edit mode view ─────────────────────────────────
	if m.editMode {
		return m.viewEdit()
	}

	// ── Diff preview view ──────────────────────────────
	if m.showDiff {
		return m.viewDiff()
	}

	// ── Filter mode view ───────────────────────────────
	if m.filterMode {
		return m.viewFilter()
	}

	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	if m.gitignoreMode {
		b.WriteString(stepStyle.Render("  Select files to add .gitignore"))
	} else {
		b.WriteString(stepStyle.Render("  Select files to commit"))
	}
	b.WriteString("\n\n")

	// Count totals across all files
	selected := 0
	ignored := 0
	for _, f := range m.files {
		if f.Selected {
			selected++
		}
		if f.Gitignored {
			ignored++
		}
	}

	// Calculate visible window
	start := 0
	end := len(m.files)
	if !m.gitignoreMode {
		visibleCount := m.height - 13
		if visibleCount < 5 {
			visibleCount = 5
		}
		if len(m.files) > visibleCount {
			start = m.fileScroll
			end = start + visibleCount
			if end > len(m.files) {
				end = len(m.files)
			}
		}
		if start > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp,start)) + "\n")
		}
	}

	// File list
	for i := start; i < end; i++ {
		f := m.files[i]
		// Cursor arrow
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		// Checkbox — depends on mode
		check := unselectedCheck.Render(symUnselected)
		if m.gitignoreMode {
			if f.Gitignored {
				check = gitignoreCheck.Render(symSelected)
			} else if f.Selected {
				check = dimmedCheck.Render(symSelected)
			}
		} else {
			if f.Selected {
				check = selectedCheck.Render(symSelected)
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

	// Scroll-down indicator
	if !m.gitignoreMode && end < len(m.files) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown,len(m.files)-end)) + "\n")
	}

	// Existing gitignore entries (only in gitignore mode)
	if m.gitignoreMode && len(m.existingIgnored) > 0 {
		b.WriteString("\n  " + dimStyle.Render("Already in .gitignore:") + "\n\n")
		for j, entry := range m.existingIgnored {
			idx := len(m.files) + j

			cursor := "  "
			if idx == m.cursor {
				cursor = cursorStyle.Render(symCursor + " ")
			}

			check := gitignoreCheck.Render(symSelected)
			if m.removeIgnored[entry] {
				check = unselectedCheck.Render(symUnselected)
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
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	if m.gitignoreMode {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "navigate"},
			{"space", "toggle"},
			{"enter", "confirm"},
			{"g", "cancel"},
		}))
	} else {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "navigate"},
			{"space", "select"},
			{"/", "filter"},
			{"d", "diff"},
			{"b", "branch"},
			{"g", "ignore"},
			{"u", "undo"},
			{"enter", "next"},
			{"q", "quit"},
		}))
	}

	return boxBorder.Render(b.String())
}

// ── Diff view ──────────────────────────────────────────

func (m Model) viewDiff() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Diff: " + m.diffFile))
	b.WriteString("\n\n")

	// Binary file
	if m.diffContent == "" {
		b.WriteString("\n\n")
		b.WriteString("          ")
		b.WriteString(dimStyle.Render("Binary file — cannot preview or edit"))
		b.WriteString("\n\n\n")
		b.WriteString(renderHelp([]helpEntry{
			{"esc", "back"},
		}))
		return boxBorder.Render(b.String())
	}

	// Diff content with colors
	lines := strings.Split(m.diffContent, "\n")
	visible := m.height - 13
	if visible < 5 {
		visible = 5
	}

	end := m.diffScroll + visible
	if end > len(lines) {
		end = len(lines)
	}

	visibleLines := lines[m.diffScroll:end]
	for i, line := range visibleLines {
		lineNum := dimStyle.Render(fmt.Sprintf("%4d ", m.diffScroll+i+1))
		styled := styleDiffLine(line)
		b.WriteString("  " + lineNum + styled + "\n")
	}

	// Line counter
	b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(
		fmt.Sprintf("Lines %d-%d of %d", m.diffScroll+1, end, len(lines)),
	)))

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	currentFile := m.files[m.cursor]
	_, fileExists := os.Stat(m.diffFile)
	if currentFile.Status == types.StatusDeleted || fileExists != nil {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "scroll"},
			{"esc", "back"},
		}))
	} else {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "scroll"},
			{"e", "edit"},
			{"esc", "back"},
		}))
	}

	return boxBorder.Render(b.String())
}

func styleDiffLine(line string) string {
	switch {
	case strings.HasPrefix(line, "(new file)"), strings.HasPrefix(line, "(deleted file)"):
		return diffHunkStyle.Render(line)
	case strings.HasPrefix(line, "@@"):
		return diffHunkStyle.Render(line)
	case strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"),
		strings.HasPrefix(line, "index "):
		return diffHeaderStyle.Render(line)
	case strings.HasPrefix(line, "+"):
		return diffAddStyle.Render(line)
	case strings.HasPrefix(line, "-"):
		return diffRemoveStyle.Render(line)
	default:
		return line
	}
}

// ── Edit view ──────────────────────────────────────────

func (m Model) viewEdit() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")

	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	title := "  Editing: " + m.diffFile
	if m.editDirty {
		title += "  " + modifiedStyle.Render(symSelected)
	}
	b.WriteString(stepStyle.Render(title))
	b.WriteString("\n\n")

	// Unsaved changes prompt
	if m.confirmExit {
		b.WriteString(m.editArea.View())
		b.WriteString("\n\n")
		b.WriteString("  " + modifiedStyle.Render("You have unsaved changes. Discard?") + "\n")
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"y", "discard"},
			{"any", "cancel"},
		}))
		return boxBorder.Render(b.String())
	}

	// Saving indicator
	if m.saving {
		b.WriteString(m.editArea.View())
		b.WriteString("\n\n")
		b.WriteString("  " + dimStyle.Render("Saving...") + "\n")
		return boxBorder.Render(b.String())
	}

	// Textarea
	b.WriteString(m.editArea.View())

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n\n")
	b.WriteString(renderHelp([]helpEntry{
		{"ctrl+s", "save"},
		{"esc", "back"},
	}))

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

// ── Actionable errors ─────────────────────────────────

func formatError(err error) string {
	msg := err.Error()
	hint := ""

	switch {
	case strings.Contains(msg, "nothing to commit"):
		hint = "Go back and select at least one file."
	case strings.Contains(msg, "CONFLICT"):
		hint = "Resolve merge conflicts before committing."
	case strings.Contains(msg, "rejected"), strings.Contains(msg, "non-fast-forward"):
		hint = "Remote has newer changes. Run git pull first."
	case strings.Contains(msg, "Authentication failed"):
		hint = "Check your git credentials or SSH key."
	case strings.Contains(msg, "Permission denied"):
		hint = "Check file or repository permissions."
	case strings.Contains(msg, "rm --cached failed"):
		hint = "Some files could not be untracked. Check paths."
	case strings.Contains(msg, "staging failed"):
		hint = "Files could not be staged. Check paths and permissions."
	}

	result := errorStyle.Render("Error: " + msg)
	if hint != "" {
		result += "\n  " + dimStyle.Render("Hint: " + hint)
	}
	return result
}

// ── Progress breadcrumb ───────────────────────────────

func stepProgressIndex(s step) int {
	switch s {
	case stepFiles:
		return 0
	case stepType, stepCustom:
		return 1
	case stepScope:
		return 2
	case stepMessage:
		return 3
	case stepConfirm:
		return 4
	case stepPush:
		return 5
	default:
		return 6
	}
}

func renderProgress(current step) string {
	names := []string{"Files", "Type", "Scope", "Message", "Confirm", "Push"}
	currentIdx := stepProgressIndex(current)

	var parts []string
	for i, name := range names {
		if i < currentIdx {
			parts = append(parts, successStyle.Render(symDone + " "+name))
		} else if i == currentIdx {
			parts = append(parts, activeStyle.Render(symCursor + " "+name))
		} else {
			parts = append(parts, dimStyle.Render(symUnselected + " "+name))
		}
	}
	return "  " + strings.Join(parts, "  ")
}

// ── Fuzzy filter ──────────────────────────────────────

func fuzzyMatch(query, target string) bool {
	q := strings.ToLower(query)
	t := strings.ToLower(target)
	qi := 0
	for i := 0; i < len(t) && qi < len(q); i++ {
		if t[i] == q[qi] {
			qi++
		}
	}
	return qi == len(q)
}

func computeFilterMatches(files []types.FileEntry, query string) []int {
	query = strings.TrimSpace(query)
	if query == "" {
		matches := make([]int, len(files))
		for i := range files {
			matches[i] = i
		}
		return matches
	}
	var matches []int
	for i, f := range files {
		if fuzzyMatch(query, f.Path) {
			matches = append(matches, i)
		}
	}
	return matches
}

func (m Model) viewFilter() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n\n")

	// Search input
	b.WriteString("  " + dimStyle.Render("/") + " " + m.filterInput.View() + "\n\n")

	// Calculate visible window
	visibleCount := m.height - 13
	if visibleCount < 5 {
		visibleCount = 5
	}
	start := 0
	end := len(m.filterMatches)
	if len(m.filterMatches) > visibleCount {
		start = m.filterCursor - visibleCount + 1
		if start < 0 {
			start = 0
		}
		end = start + visibleCount
		if end > len(m.filterMatches) {
			end = len(m.filterMatches)
			start = end - visibleCount
		}
	}

	if start > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp,start)) + "\n")
	}

	// Matched files
	for j := start; j < end; j++ {
		idx := m.filterMatches[j]
		f := m.files[idx]

		cursor := "  "
		if j == m.filterCursor {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		check := unselectedCheck.Render(symUnselected)
		if f.Selected {
			check = selectedCheck.Render(symSelected)
		}

		status := fileStatusStyle(f.Status).Render(fmt.Sprintf("%-2s", f.Status.Symbol()))

		path := filePathStyle.Render(f.Path)
		if j == m.filterCursor {
			path = highlightStyle.Render(f.Path)
		}

		b.WriteString(fmt.Sprintf("%s%s  %s  %s\n", cursor, check, status, path))
	}

	if end < len(m.filterMatches) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown,len(m.filterMatches)-end)) + "\n")
	}

	// Counter
	b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(
		fmt.Sprintf("%d of %d matched", len(m.filterMatches), len(m.files)),
	)))

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{symArrows, "navigate"},
		{"tab", "select"},
		{"enter", "jump"},
		{"esc", "cancel"},
	}))

	return boxBorder.Render(b.String())
}
