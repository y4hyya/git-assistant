package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter", "esc":
			m.menuCursor = 0
			m.committing = false
			m.pushing = false
			// Wizard reset, fresh file list, and graph refresh all live in
			// returnToMenu so no exit path can skip one of them.
			cmd := m.returnToMenu()
			return m, cmd
		case "q":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewDone() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(m.renderProgress())
	b.WriteString("\n\n")

	// Commit / amend summary
	msg := m.subjectLine(strings.TrimSpace(m.msgInput.Value()))
	verb := "Committed"
	if m.amendMode {
		verb = "Amended"
	}
	b.WriteString("  " + successStyle.Render(symDone) + " " + verb + ": " + msg + "\n")

	// Commit hash and stats, both read once by the commit command (see
	// doCommit). This screen used to fork two git processes per render.
	if m.commitHash != "" || m.commitStats != "" {
		detail := "    "
		if m.commitHash != "" {
			detail += dimStyle.Render(m.commitHash)
		}
		if m.commitHash != "" && m.commitStats != "" {
			detail += dimStyle.Render(" " + symMidDot + " ")
		}
		if m.commitStats != "" {
			detail += dimStyle.Render(m.commitStats)
		}
		b.WriteString(detail + "\n")
	}

	// Push summary — what actually happened, and nothing else. A failed push
	// used to arrive here as the neutral "Push skipped" with its reason already
	// wiped by the keypress that navigated, so a push that did not happen and a
	// push that broke read identically.
	switch {
	case m.pushed:
		b.WriteString("  " + successStyle.Render(symDone) + " Pushed to " + branchStyle.Render("origin/"+m.pushBranch) + "\n")
	case m.pushFailed:
		reason := "see the push step for details"
		if m.pushErr != nil {
			reason = firstLine(m.pushErr.Error())
		}
		b.WriteString("  " + errorStyle.Render(symWarn+" Push failed — "+reason) + "\n")
		b.WriteString("  " + dimStyle.Render("The commit is safe locally. Try Push again from the menu.") + "\n")
	case m.amendMode && m.amendPushed:
		// The amend flow never pushes: the rewritten commit is already on
		// origin, so the only correct push is a force-with-lease the user runs
		// deliberately. Say that instead of "Push skipped".
		b.WriteString("  " + modifiedStyle.Render(symArrowUp+" This commit is on origin — update it with:") + "\n")
		b.WriteString("  " + modifiedStyle.Render("    git push --force-with-lease") + "\n")
	case m.amendMode && m.hasRemote:
		b.WriteString("  " + dimStyle.Render(symSkip+" Amended locally — push when you're ready") + "\n")
	case m.hasRemote:
		b.WriteString("  " + dimStyle.Render(symSkip+" Push skipped — the commit is local only") + "\n")
	}

	if m.pushFailed {
		b.WriteString("\n  " + modifiedStyle.Render("Committed, not pushed.") + "\n")
	} else {
		b.WriteString("\n  " + successStyle.Render("All done!") + "\n")
	}

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter/esc", "menu"},
		{"q", "quit"},
	}))

	return m.styledBox(b.String())
}

// firstLine trims an error down to its first line for the one-line summary on
// the Done screen. Git's push failures are several lines of remote output; the
// full text still lives on the push step.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
