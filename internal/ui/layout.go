package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// styledBox wraps content in the standard bordered box constrained to terminal size.
func (m Model) styledBox(content string) string {
	w := m.width - 4
	if w < 30 {
		w = 30
	}
	h := m.height
	if h < 10 {
		h = 10
	}
	return boxBorder.Width(w).MaxHeight(h).Render(content)
}

// renderGraphSection returns a compact side-by-side local/remote graph footer.
// Height-adaptive: reduces commits or hides entirely if terminal is too short.
func (m Model) renderGraphSection() string {
	if m.width < 60 {
		return ""
	}

	// Menu content uses ~14 lines (header, items, help, box overhead, spacing)
	available := m.height - 14
	if available < 6 {
		return "" // not enough room
	}

	// Each commit with connector = 2 lines. Titles + separator + spacing = 4 lines.
	maxCommits := (available - 4) / 2
	if maxCommits < 1 {
		return ""
	}
	if maxCommits > 5 {
		maxCommits = 5
	}

	innerWidth := m.width - 12
	if innerWidth < 30 {
		innerWidth = 30
	}
	halfWidth := innerWidth / 2

	localTitle := graphTitleStyle.Render("Local")
	localContent := transformGraph(m.localGraph, halfWidth-2, maxCommits*2)
	localPanel := localTitle + "\n" + localContent

	remoteTitle := graphTitleStyle.Render("Remote")
	remoteContent := transformGraph(m.remoteGraph, halfWidth-2, maxCommits*2)
	abLine := ""
	if m.aheadBehind != "" {
		abLine = "\n" + dimStyle.Render(m.aheadBehind)
	}
	remotePanel := remoteTitle + "\n" + remoteContent + abLine

	leftCol := lipgloss.NewStyle().Width(halfWidth).Render(localPanel)
	rightCol := lipgloss.NewStyle().Width(halfWidth).Render(remotePanel)

	separator := dimStyle.Render(strings.Repeat("─", innerWidth))

	return separator + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol)
}

// ── Graph transformation ──────────────────────────────

type graphLine struct {
	chars     []byte
	message   string
	starCol   int
	connector bool
}

func transformGraph(raw string, maxWidth, maxLines int) string {
	if raw == "" {
		return dimStyle.Render("(no commits)")
	}

	rawLines := strings.Split(raw, "\n")
	parsed := make([]graphLine, 0, len(rawLines))
	for _, line := range rawLines {
		parsed = append(parsed, parseLine(line))
	}

	withConnectors := insertConnectors(parsed)

	if len(withConnectors) > maxLines {
		withConnectors = withConnectors[:maxLines]
	}

	var result []string
	for _, gl := range withConnectors {
		result = append(result, renderGraphLine(gl, maxWidth))
	}

	return strings.Join(result, "\n")
}

func parseLine(line string) graphLine {
	gl := graphLine{starCol: -1}

	graphEnd := 0
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if ch == '*' || ch == '|' || ch == '/' || ch == '\\' || ch == ' ' || ch == '_' {
			gl.chars = append(gl.chars, ch)
			if ch == '*' {
				gl.starCol = i
			}
			graphEnd = i + 1
		} else {
			break
		}
	}

	if graphEnd < len(line) {
		gl.message = strings.TrimSpace(line[graphEnd:])
	}

	return gl
}

func insertConnectors(lines []graphLine) []graphLine {
	var result []graphLine
	for i, gl := range lines {
		result = append(result, gl)

		if gl.starCol < 0 {
			continue
		}

		if i < len(lines)-1 && lines[i+1].starCol >= 0 {
			conn := graphLine{
				starCol:   gl.starCol,
				connector: true,
			}
			for j := 0; j <= gl.starCol; j++ {
				if j == gl.starCol {
					conn.chars = append(conn.chars, '|')
				} else if j < len(gl.chars) && gl.chars[j] == '|' {
					conn.chars = append(conn.chars, '|')
				} else {
					conn.chars = append(conn.chars, ' ')
				}
			}
			result = append(result, conn)
		}
	}
	return result
}

func renderGraphLine(gl graphLine, maxWidth int) string {
	var visual strings.Builder

	for i, ch := range gl.chars {
		switch ch {
		case '*':
			if i == 0 {
				visual.WriteString(activeStyle.Render(symSelected))
			} else {
				visual.WriteString(modifiedStyle.Render(symSelected))
			}
		case '|':
			if gl.connector && i == gl.starCol {
				if gl.starCol == 0 {
					visual.WriteString(activeStyle.Render("│"))
				} else {
					visual.WriteString(modifiedStyle.Render("│"))
				}
			} else {
				visual.WriteString(dimStyle.Render("│"))
			}
		case '/':
			visual.WriteString(dimStyle.Render("/"))
		case '\\':
			visual.WriteString(dimStyle.Render("\\"))
		case ' ':
			visual.WriteString(" ")
		case '_':
			visual.WriteString(dimStyle.Render("_"))
		}
	}

	if gl.connector || gl.message == "" {
		return visual.String()
	}

	prefixWidth := len(gl.chars)
	available := maxWidth - prefixWidth - 1
	if available < 10 {
		available = 10
	}

	msg := gl.message
	if len(msg) > available {
		msg = msg[:available-3] + "..."
	}

	return visual.String() + " " + msg
}

func formatAheadBehind(ahead, behind int) string {
	if ahead == 0 && behind == 0 {
		return "up to date"
	}
	parts := []string{}
	if ahead > 0 {
		parts = append(parts, fmt.Sprintf("%d ahead", ahead))
	}
	if behind > 0 {
		parts = append(parts, fmt.Sprintf("%d behind", behind))
	}
	return strings.Join(parts, " · ")
}
