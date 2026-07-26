package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/git"
	"git-assist/internal/types"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Update ──────────────────────────────────────────────

// enterConfirm routes into the confirm step, reading everything the screen
// needs out of git once, here.
//
// The index-vs-HEAD file list: `git commit --amend` commits the WHOLE index, so
// anything staged outside the wizard (from another terminal, or a `git add`
// before launching git-assist) rides along in the rewritten commit and has to
// be disclosed.
//
// The commit's short SHA and whether it is already on a remote: viewConfirm
// used to call GetLastCommitHash and IsLastCommitPushed itself, and a View func
// runs on every keypress, every resize — and on every spinner tick, which is
// ten times a second for the whole of "Committing...". `git branch -r
// --contains HEAD` is not a cheap question to ask 10x/sec. Neither answer can
// change while the screen is up: the commit is only rewritten when the user
// confirms, and that leaves the step.
func (m *Model) enterConfirm() {
	m.amendStaged = nil
	m.amendSHA = ""
	m.amendPushed = false
	if m.amendMode {
		if staged, err := git.GetStagedFiles(); err == nil {
			m.amendStaged = staged
		}
		m.amendSHA = git.GetLastCommitHash()
		m.amendPushed = git.IsLastCommitPushed()
	}
	m.step = stepConfirm
}

// extraStagedFiles returns the staged paths the wizard did not select — the
// ones the user has no other way of knowing about.
func (m Model) extraStagedFiles() []string {
	if len(m.amendStaged) == 0 {
		return nil
	}
	selected := make(map[string]bool, len(m.files))
	for _, f := range m.files {
		if f.Selected {
			selected[f.Path] = true
			if f.OrigPath != "" {
				selected[f.OrigPath] = true
			}
		}
	}
	var extra []string
	for _, p := range m.amendStaged {
		if !selected[p] {
			extra = append(extra, p)
		}
	}
	return extra
}

func (m Model) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks while committing
	if m.committing {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "enter":
		m.committing = true
		val := strings.TrimSpace(m.msgInput.Value())
		fullMsg := m.buildCommitMessage(val)
		// Pass whole entries, not paths: a rename needs its original path
		// staged alongside the new one.
		var selected []types.FileEntry
		for _, f := range m.files {
			if f.Selected {
				selected = append(selected, f)
			}
		}
		if m.amendMode {
			return m, tea.Batch(doAmend(selected, fullMsg), m.spinner.Tick)
		}
		return m, tea.Batch(doCommit(selected, m.gitignoreCached, fullMsg), m.spinner.Tick)
	case "esc":
		m.step = stepMessage
		m.bodyFocused = false
		m.bodyInput.Blur()
		m.msgInput.Focus()
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewConfirm() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	if m.amendMode {
		// Both of these are cached by enterConfirm — see the note there.
		b.WriteString(stepStyle.Render("  Amend " + m.amendSHA))
	} else {
		b.WriteString(stepStyle.Render("  Review before committing"))
	}
	b.WriteString("\n\n")

	// Warn before amending a commit that's already on a remote — the
	// next push will need --force-with-lease to update upstream, and
	// surfacing that here avoids the "why is my push rejected" loop.
	if m.amendMode && m.amendPushed {
		b.WriteString("  " + modifiedStyle.Render(symArrowUp+" This commit is on origin. Amending will require:") + "\n")
		b.WriteString("  " + modifiedStyle.Render("    git push --force-with-lease") + "\n\n")
	}

	// Full commit message preview
	val := strings.TrimSpace(m.msgInput.Value())
	b.WriteString("  " + highlightStyle.Render(m.subjectLine(val)) + "\n")
	if m.amendRaw {
		b.WriteString("  " + dimStyle.Render("(keeping original format)") + "\n")
	}

	// Body if present
	if m.showBody {
		body := strings.TrimSpace(m.bodyInput.Value())
		if body != "" {
			b.WriteString("\n")
			for _, line := range strings.Split(body, "\n") {
				b.WriteString("  " + dimStyle.Render(line) + "\n")
			}
		}
	}

	// Selected files
	b.WriteString("\n")
	var selected []int
	for i, f := range m.files {
		if f.Selected {
			selected = append(selected, i)
		}
	}

	// An amend keeps everything the commit already had, so a bare "0 file(s):"
	// reads like the commit is about to be emptied. Say what actually happens.
	switch {
	case m.amendMode && len(selected) == 0:
		b.WriteString(fmt.Sprintf("  %s\n", dimStyle.Render("Message-only amend — the commit keeps its current files")))
	case m.amendMode:
		b.WriteString(fmt.Sprintf("  %s\n", dimStyle.Render(fmt.Sprintf("%d file(s) to add to this commit:", len(selected)))))
	default:
		b.WriteString(fmt.Sprintf("  %s\n", dimStyle.Render(fmt.Sprintf("%d file(s):", len(selected)))))
	}

	maxShow := 5
	for j, idx := range selected {
		if j >= maxShow {
			remaining := len(selected) - maxShow
			b.WriteString(fmt.Sprintf("    %s\n", dimStyle.Render(fmt.Sprintf("... and %d more", remaining))))
			break
		}
		f := m.files[idx]
		status := fileStatusStyle(f.Status).Render(fmt.Sprintf("%-2s", f.Status.Symbol()))
		b.WriteString(fmt.Sprintf("    %s  %s\n", status, filePathStyle.Render(f.Path)))
	}

	// `git commit --amend` commits the whole index — disclose anything staged
	// outside this wizard rather than folding it in silently.
	if extra := m.extraStagedFiles(); len(extra) > 0 {
		b.WriteString("\n  " + modifiedStyle.Render(fmt.Sprintf("Also included (already staged): %d file(s)", len(extra))) + "\n")
		for j, p := range extra {
			if j >= maxShow {
				b.WriteString(fmt.Sprintf("    %s\n", dimStyle.Render(fmt.Sprintf("... and %d more", len(extra)-maxShow))))
				break
			}
			b.WriteString(fmt.Sprintf("    %s\n", filePathStyle.Render(p)))
		}
	}

	// Committing spinner
	if m.committing {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Committing...") + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "commit"},
		{"esc", "back"},
		{"q", "quit"},
	}))

	return m.styledBox(b.String())
}
