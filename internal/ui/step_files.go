package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"git-assist/internal/git"
	"git-assist/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// ── List geometry ───────────────────────────────────────
//
// Update and View must agree on how many rows a list shows. When they didn't,
// the cursor moved through rows the view never drew: the arrow vanished and
// keys appeared to do nothing.

// listRows is how many list (or diff) rows a full-screen selector may draw —
// the terminal minus box chrome, header, breadcrumb, counter and help bar.
func (m Model) listRows() int {
	rows := m.height - 13
	if rows < 5 {
		rows = 5
	}
	return rows
}

// fileListRows is listRows minus the commit-mode footer's second row (the
// honest key set does not fit one 80-column line).
func (m Model) fileListRows() int {
	rows := m.listRows() - 1
	if rows < 4 {
		rows = 4
	}
	return rows
}

// gitignoreRows is listRows minus what the .gitignore screen additionally
// spends: the "Already in .gitignore:" header, the counter and the
// rm --cached warning.
func (m Model) gitignoreRows() int {
	rows := m.listRows() - 5
	if rows < 4 {
		rows = 4
	}
	return rows
}

// listWindow returns the [start,end) slice of a total-length list that is
// visible at the given scroll offset.
func listWindow(total, scroll, rows int) (start, end int) {
	if total <= rows {
		return 0, total
	}
	start = scroll
	if start > total-rows {
		start = total - rows
	}
	if start < 0 {
		start = 0
	}
	end = start + rows
	if end > total {
		end = total
	}
	return start, end
}

// followCursor scrolls the file list so the cursor stays inside the window the
// view will draw. Every path that moves the cursor calls it — a jump out of the
// filter used to leave the cursor outside the window, rendering no cursor at
// all until the next ordinary keypress happened to repair the offset.
func (m *Model) followCursor(total, rows int) {
	if m.cursor < m.fileScroll {
		m.fileScroll = m.cursor
	}
	if m.cursor >= m.fileScroll+rows {
		m.fileScroll = m.cursor - rows + 1
	}
	if m.fileScroll > total-rows {
		m.fileScroll = total - rows
	}
	if m.fileScroll < 0 {
		m.fileScroll = 0
	}
}

// diffMaxScroll is the largest offset that still renders a full window ending
// at the last line. The old bound stopped one window short: `down` refused to
// advance past len-1-visible, so the tail of every long diff was unreachable
// and the counter said so — "Lines 1-40 of 41", with no key that moved it.
func (m Model) diffMaxScroll() int {
	n := len(strings.Split(m.diffContent, "\n")) - m.listRows()
	if n < 0 {
		n = 0
	}
	return n
}

// diffScrollMemory is how deep a remembered diff position is still worth
// restoring. Closing a diff to check something and reopening it should come
// back where you were; being dropped 300 lines into a file you just reopened
// is disorienting, and the deeper the offset the likelier it is stale after an
// edit. Positions at or past this line start from the top instead.
const diffScrollMemory = 15

// rememberDiffScroll stores where the user was in the diff being closed, for
// the rest of this visit to the file selector (resetWizard drops the map).
func (m *Model) rememberDiffScroll() {
	if m.diffFile == "" {
		return
	}
	if m.diffScroll <= 0 || m.diffScroll >= diffScrollMemory {
		delete(m.diffScrollByFile, m.diffFile)
		return
	}
	if m.diffScrollByFile == nil {
		m.diffScrollByFile = make(map[string]int)
	}
	m.diffScrollByFile[m.diffFile] = m.diffScroll
}

// rememberedDiffScroll returns the offset to reopen path at.
func (m Model) rememberedDiffScroll(path string) int {
	s := m.diffScrollByFile[path]
	if s < 0 || s >= diffScrollMemory {
		return 0
	}
	return s
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateFiles(msg tea.Msg) (tea.Model, tea.Cmd) {
	// While undo or the .gitignore apply is in flight, block key input so a
	// second press can't dispatch the same command twice (two soft resets
	// move HEAD back two commits; a second `git rm --cached` fails on paths
	// the first pass already removed). Spinner ticks are non-key messages
	// and are forwarded so the animation keeps running.
	if m.undoing || m.gitignoring {
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

	// Handle undo confirmation
	if m.confirmUndo {
		switch keyMsg.String() {
		case "y":
			m.confirmUndo = false
			m.undoing = true
			return m, tea.Batch(doUndo(), m.spinner.Tick)
		default:
			m.confirmUndo = false
			m.undoPushed = false
			m.undoSubject = ""
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
			if m.cursor >= totalItems {
				// Nothing to toggle — no changed files and an empty (or
				// missing) .gitignore, or a stale cursor. Both branches
				// below would index past the end of a slice.
				return m, nil
			}
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
				// Same silence as the commit-mode enter used to have: say what
				// the key is waiting for instead of doing nothing.
				m.err = fmt.Errorf("%s nothing toggled yet — press space to pick what .gitignore should cover", symWarn)
				return m, nil
			}
			m.gitignoreCached = cachedPaths
			m.gitignoring = true
			return m, tea.Batch(doGitignore(addPaths, cachedPaths, removePaths), m.spinner.Tick)
		case "g", "esc":
			for i := range m.files {
				m.files[i].Gitignored = false
			}
			m.removeIgnored = nil
			m.existingIgnored = nil
			m.gitignoreMode = false
			// The cursor may be parked in the existing-ignored zone, which
			// doesn't exist back in commit mode — space/d there would index
			// past the end of m.files. Mirror the confirm path's reset.
			if m.cursor >= len(m.files) {
				m.cursor = max(0, len(m.files)-1)
			}
			// Back in commit mode the window is a different size, so re-follow
			// the cursor there rather than with the .gitignore geometry below.
			m.followCursor(len(m.files), m.fileListRows())
			return m, nil
		case "q":
			m.quitting = true
			return m, tea.Quit
		}
		// The combined list (changed files + existing .gitignore entries) is
		// windowed exactly like the commit-mode list. It used to render whole,
		// so styledBox cut it off at the terminal edge while up/down happily
		// walked the cursor into the part nobody could see.
		m.followCursor(totalItems, m.gitignoreRows())
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
				// Jumping to a match 40 files down used to leave fileScroll
				// where it was, so the selector came back with no visible
				// cursor at all until some other key repaired the window.
				m.followCursor(len(m.files), m.fileListRows())
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
		maxScroll := m.diffMaxScroll()

		switch keyMsg.String() {
		case "up", "k":
			if m.diffScroll > 0 {
				m.diffScroll--
			}
		case "down", "j":
			if m.diffScroll < maxScroll {
				m.diffScroll++
			}
		case "e":
			// An empty diff is the binary sentinel (GetFileDiff returns
			// ErrBinaryFile, and the "d" handler stores ""). The editor
			// would round-trip the bytes through a UTF-8 string, replacing
			// every invalid byte with U+FFFD, and ctrl+s would write that
			// back over the real file — unrecoverable for untracked files.
			if m.diffContent == "" {
				m.err = errors.New("binary file — cannot edit")
				return m, nil
			}
			// Block edit for deleted files and files not on disk
			if m.cursor >= len(m.files) {
				return m, nil
			}
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
			// Defense in depth: the sentinel above covers files git reports
			// as binary, but re-check what we actually read — the file may
			// have changed since the diff was taken, and anything with a NUL
			// byte must never reach the textarea.
			if git.IsBinaryContent(content) {
				m.err = errors.New("binary file — cannot edit")
				return m, nil
			}
			m.editArea.SetValue(content)
			// Same sizing the resize handler applies, from one place: entry and
			// resize used to compute it separately and could disagree.
			m.editArea.SetWidth(editAreaWidth(m.width))
			m.editArea.SetHeight(editAreaHeight(m.height))
			m.editArea.Focus()
			m.editMode = true
			m.editDirty = false
			return m, nil
		case "esc":
			m.rememberDiffScroll()
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
		// The list can legitimately be empty (everything ignored, or a
		// clean tree) — indexing it would kill the TUI.
		if m.cursor >= len(m.files) {
			return m, nil
		}
		m.files[m.cursor].Selected = !m.files[m.cursor].Selected
	case "d":
		if m.cursor >= len(m.files) {
			return m, nil
		}
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
		m.diffScroll = m.rememberedDiffScroll(f.Path)
		if maxScroll := m.diffMaxScroll(); m.diffScroll > maxScroll {
			m.diffScroll = maxScroll
		}
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
		// Both read once, here — never from a View func. Undoing a commit that
		// is already on origin makes the branch diverge, which is worth
		// saying out loud before the soft reset.
		m.undoPushed = git.IsLastCommitPushed()
		m.undoSubject = git.GetLastCommitMessage()
	case "enter":
		hasSelected := false
		for _, f := range m.files {
			if f.Selected {
				hasSelected = true
				break
			}
		}
		// In amend mode the user can advance without selecting anything —
		// they may just want to edit the message of the existing commit.
		if !hasSelected && !m.amendMode {
			// Silence was the whole problem: enter did nothing, said nothing,
			// and the "0/N selected" counter never connected the two.
			m.err = fmt.Errorf("%s nothing selected yet — press space to select a file, a to select all", symWarn)
			return m, nil
		}
		if m.amendMode {
			m.step = m.firstAmendStep()
			if m.step == stepMessage {
				m.msgInput.Focus()
				m.msgInput.CursorEnd()
			}
		} else {
			m.step = stepType
		}
		m.cursor = 0
	case "esc":
		// Abandoning the wizard: drop every field it filled in (an abandoned
		// amend used to leave the old commit's message pre-loaded for the
		// next ordinary commit) and re-read the tree on the way out.
		cmd := m.returnToMenu()
		return m, cmd
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	// Adjust scroll to keep cursor visible
	m.followCursor(len(m.files), m.fileListRows())

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
	b.WriteString(m.renderProgress())
	b.WriteString("\n")
	if m.gitignoreMode {
		b.WriteString(stepStyle.Render("  Select files to add .gitignore"))
	} else if m.amendMode {
		b.WriteString(stepStyle.Render("  Amending last commit — optionally add files"))
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

	// Visible window. In .gitignore mode it spans the COMBINED list — changed
	// files first, existing entries after — so one offset scrolls through both
	// sections and the cursor is on screen wherever it is. That list used to
	// render whole, which meant styledBox cut it at the terminal edge while
	// up/down walked the cursor on into the invisible part.
	total := len(m.files)
	rows := m.fileListRows()
	if m.gitignoreMode {
		total += len(m.existingIgnored)
		rows = m.gitignoreRows()
	}
	start, end := listWindow(total, m.fileScroll, rows)

	if start > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp, start)) + "\n")
	}

	// File list — the part of the window inside the changed-files half
	for i := start; i < min(end, len(m.files)); i++ {
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

	// Existing gitignore entries — the part of the same window past the files
	if m.gitignoreMode && end > len(m.files) {
		b.WriteString("\n  " + dimStyle.Render("Already in .gitignore:") + "\n\n")
		for idx := max(start, len(m.files)); idx < end; idx++ {
			entry := m.existingIgnored[idx-len(m.files)]

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

	// Scroll-down indicator
	if end < total {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown, total-end)) + "\n")
	}

	// Counter
	if m.gitignoreMode {
		removing := 0
		for _, r := range m.removeIgnored {
			if r {
				removing++
			}
		}
		// Count tracked files in the add-set. Adding a tracked file to
		// .gitignore alone doesn't untrack it — git keeps tracking files it
		// already knows about. The confirm handler runs `git rm --cached`
		// for these (see m.gitignoreCached), so warn the user up-front that
		// "ignore" here means "remove from the index, then ignore future
		// changes" — that surprise was easy to walk into silently.
		trackedAdds := 0
		for _, f := range m.files {
			if f.Gitignored && f.Status != types.StatusUntracked {
				trackedAdds++
			}
		}
		counter := fmt.Sprintf("%d to add", ignored)
		if removing > 0 {
			counter += fmt.Sprintf(" · %d to remove", removing)
		}
		b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(counter)))
		if trackedAdds > 0 {
			b.WriteString(fmt.Sprintf("  %s %s\n",
				modifiedStyle.Render("!"),
				modifiedStyle.Render(fmt.Sprintf("%d tracked file(s) will be removed from the index (git rm --cached)", trackedAdds))))
		}
	} else if len(m.files) == 0 {
		b.WriteString("\n  " + dimStyle.Render("Working tree clean — nothing to select") + "\n")
	} else {
		b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(fmt.Sprintf("%d/%d selected", selected, len(m.files)))))
	}

	// What just happened here — the .gitignore apply and the undo both land back
	// on this screen, and both used to wait for the dashboard to say anything.
	if note := m.renderStatusNote(); note != "" {
		b.WriteString("\n" + note + "\n")
	}

	// Async op spinners
	if m.undoing {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Undoing last commit...") + "\n")
	}
	if m.gitignoring {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Updating .gitignore...") + "\n")
	}

	// Undo confirmation
	if m.confirmUndo {
		// Both cached when the prompt opened (see the "u" handler) — this used
		// to shell out to git on every frame the prompt was up.
		if m.undoSubject != "" {
			b.WriteString("\n  " + dimStyle.Render("Last: "+m.undoSubject))
		}
		b.WriteString("\n  " + modifiedStyle.Render("Undo last commit?") + "\n")
		// Say what undo does, in terms of where the work ends up: it is
		// `git reset --soft`, so the commit's changes come back staged and
		// ready to re-commit. "Changes will be kept" left people guessing.
		b.WriteString("  " + dimStyle.Render("Moves the last commit's changes back to staged — nothing is lost.") + "\n")
		if m.undoPushed {
			b.WriteString("  " + modifiedStyle.Render(symWarn+" already pushed to origin — undoing will make your branch diverge") + "\n")
		}
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"y", "confirm"},
			{"any", "cancel"},
		}))
		return m.styledBox(b.String())
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar. Every key listed here is handled in updateFiles, and every key
	// handled there is listed — "a" (select all) and "esc" (back to the menu)
	// were both live and undocumented. Two rows because the honest set does not
	// fit one 80-column line, and a wrapped help bar is worse than two tidy ones.
	b.WriteString("\n")
	if m.gitignoreMode {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "navigate"},
			{"space", "toggle"},
			{"a", "all"},
			{"enter", "confirm"},
			{"g/esc", "cancel"},
			{"q", "quit"},
		}))
	} else {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "navigate"},
			{"space", "select"},
			{"a", "all"},
			{"/", "filter"},
			{"enter", "next"},
		}))
		b.WriteString("\n")
		b.WriteString(renderHelp([]helpEntry{
			{"d", "diff"},
			{"b", "branch"},
			{"g", "ignore"},
			{"u", "undo"},
			{"esc", "menu"},
			{"q", "quit"},
		}))
	}

	return m.styledBox(b.String())
}

// ── Diff view ──────────────────────────────────────────

func (m Model) viewDiff() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(m.renderProgress())
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
		return m.styledBox(b.String())
	}

	// Diff content with colors. The window comes from the same helper the key
	// handler bounds itself with, so the last line is always reachable and the
	// counter can never promise a line the keys cannot reach.
	lines := strings.Split(m.diffContent, "\n")
	start, end := listWindow(len(lines), m.diffScroll, m.listRows())

	for i, line := range lines[start:end] {
		lineNum := dimStyle.Render(fmt.Sprintf("%4d ", start+i+1))
		styled := styleDiffLine(line)
		b.WriteString("  " + lineNum + styled + "\n")
	}

	// Line counter
	b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(
		fmt.Sprintf("Lines %d-%d of %d", start+1, end, len(lines)),
	)))

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	deleted := true // no entry to check → treat as non-editable
	if m.cursor < len(m.files) {
		deleted = m.files[m.cursor].Status == types.StatusDeleted
	}
	_, fileExists := os.Stat(m.diffFile)
	if deleted || fileExists != nil {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "scroll"},
			{"esc", "back"},
			{"q", "quit"},
		}))
	} else {
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "scroll"},
			{"e", "edit"},
			{"esc", "back"},
			{"q", "quit"},
		}))
	}

	return m.styledBox(b.String())
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

	b.WriteString(m.renderProgress())
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
		return m.styledBox(b.String())
	}

	// Saving indicator
	if m.saving {
		b.WriteString(m.editArea.View())
		b.WriteString("\n\n")
		b.WriteString("  " + dimStyle.Render("Saving...") + "\n")
		// Only the force-quit warning can appear while a save is in flight.
		if m.err != nil {
			b.WriteString("\n  " + formatError(m.err) + "\n")
		}
		return m.styledBox(b.String())
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

	return m.styledBox(b.String())
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
	return formatErrorCtx(err, false)
}

// formatErrorCtx is formatError with the one thing the app knows about itself
// that changes the advice: whether this session rewrote a commit origin already
// had (see Model.historyRewritten). A push rejected after an amend or an undo
// must not be answered with "run git pull first" — that pull merges the
// pre-rewrite commit straight back in, undoing exactly what the user did.
func formatErrorCtx(err error, historyRewritten bool) string {
	msg := err.Error()
	hint := ""

	// Advisory warnings (prefixed with symWarn) ride the same status line as
	// errors but aren't failures — render them without the "Error:" prefix.
	if strings.HasPrefix(msg, symWarn) {
		return modifiedStyle.Render(msg)
	}

	switch {
	case strings.Contains(msg, "nothing to commit"):
		hint = "Go back and select at least one file."
	case strings.Contains(msg, "CONFLICT"):
		hint = "Resolve merge conflicts before committing."
	case strings.Contains(msg, "rejected"), strings.Contains(msg, "non-fast-forward"):
		if historyRewritten {
			// Two lines on purpose: one long line wraps mid-command inside the
			// box, and a wrapped `git push --force-with-lease` is not copyable.
			hint = "You rewrote a pushed commit (amend/undo) — origin still has the old one.\n  Update it with: git push --force-with-lease"
		} else {
			hint = "Remote has newer changes. Run git pull first."
		}
	case strings.Contains(msg, "would be overwritten by merge"):
		hint = "Commit or stash your changes first — the merge would overwrite them."
	case strings.Contains(msg, "Authentication failed"):
		hint = "Check your git credentials or SSH key."
	case strings.Contains(msg, "Permission denied"):
		hint = "Check file or repository permissions."
	case strings.Contains(msg, "rm --cached failed"):
		hint = "Some files could not be untracked. Check paths."
	case strings.Contains(msg, "could not stage"), strings.Contains(msg, "staging failed"):
		hint = "Nothing was committed. Refresh the file list (esc, then enter) and try again."
	case strings.Contains(msg, "nothing to undo"):
		hint = "Use Amend from the menu to change the first commit instead."
	// git's own wording is "'my branch' is not a valid branch name"; the old
	// pattern looked for "invalid branch name", which git never says, so this
	// hint could not fire at all.
	case strings.Contains(msg, "not a valid branch name"):
		hint = "Branch names cannot contain spaces or special characters like ~, ^, :, ?, *, ["
	}
	// No "saved in stash" hint here on purpose: every stash message now
	// carries its own exact recovery command, and the generic hint used to
	// contradict it ("git stash pop" on a branch where the entry conflicts).

	result := errorStyle.Render("Error: " + msg)
	if hint != "" {
		result += "\n  " + dimStyle.Render("Hint: "+hint)
	}
	// Recovery errors survive ordinary keypresses (see Update) — say so, or
	// the banner looks stuck.
	if isRecoveryError(err) {
		result += "\n  " + helpKeyStyle.Render("esc") + " " + helpStyle.Render("dismiss")
	}
	return result
}

// ── Progress breadcrumb ───────────────────────────────

// progressPlan returns the breadcrumb labels that apply to THIS wizard run,
// plus the index of the step on screen. The list used to be the fixed
// Files/Type/Message/Confirm/Push regardless of context, so the Done screen
// check-marked a Push that had not happened — and after an amend, one that
// deliberately never will: amends route straight to Done and leave the
// force-with-lease push to the user.
func (m Model) progressPlan() (names []string, idx int) {
	entries := []struct {
		name  string
		steps []step
		skip  bool
	}{
		{name: "Files", steps: []step{stepFiles}},
		// A raw amend never picks a type — the subject goes back verbatim.
		{name: "Type", steps: []step{stepType, stepCustom}, skip: m.amendRaw},
		{name: "Message", steps: []step{stepMessage}},
		{name: "Confirm", steps: []step{stepConfirm}},
		{name: "Push", steps: []step{stepPush}, skip: m.amendMode || !m.hasRemote},
	}
	idx = -1
	for _, e := range entries {
		if e.skip {
			continue
		}
		for _, s := range e.steps {
			if s == m.step {
				idx = len(names)
			}
		}
		names = append(names, e.name)
	}
	// Done — and anything else past the wizard — means every listed step is
	// behind us.
	if idx < 0 {
		idx = len(names)
	}
	return names, idx
}

func (m Model) renderProgress() string {
	names, idx := m.progressPlan()

	var parts []string
	for i, name := range names {
		switch {
		case m.step == stepDone && name == "Push" && !m.pushed:
			// The wizard finished without pushing: skipped, or the push failed.
			// A checkmark here is the same lie the Done body used to tell.
			style := dimStyle
			if m.pushFailed {
				style = errorStyle
			}
			parts = append(parts, style.Render(symSkip+" "+name))
		case i < idx:
			parts = append(parts, successStyle.Render(symDone+" "+name))
		case i == idx:
			parts = append(parts, activeStyle.Render(symCursor+" "+name))
		default:
			parts = append(parts, dimStyle.Render(symUnselected+" "+name))
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
	b.WriteString(m.renderProgress())
	b.WriteString("\n\n")

	// Search input
	b.WriteString("  " + dimStyle.Render("/") + " " + m.filterInput.View() + "\n\n")

	// Calculate visible window — same row budget as the unfiltered list.
	visibleCount := m.listRows()
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
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp, start)) + "\n")
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
		b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown, len(m.filterMatches)-end)) + "\n")
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

	return m.styledBox(b.String())
}
