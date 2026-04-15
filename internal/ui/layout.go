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

// renderGraphSection returns a single unified commit graph footer.
// Height-adaptive: reduces commits or hides entirely if terminal is too short.
func (m Model) renderGraphSection() string {
	if m.width < 40 {
		return ""
	}

	// Menu content uses ~14 lines (header, items, help, box overhead, spacing)
	available := m.height - 14
	if available < 6 {
		return "" // not enough room
	}

	// Each commit with connector = 2 lines. Title + separator + spacing = 3 lines.
	maxCommits := (available - 3) / 2
	if maxCommits < 1 {
		return ""
	}
	if maxCommits > 7 {
		maxCommits = 7
	}

	innerWidth := m.width - 12
	if innerWidth < 30 {
		innerWidth = 30
	}

	title := graphTitleStyle.Render("Commit Graph")
	if m.aheadBehind != "" {
		padding := innerWidth - lipgloss.Width(title) - len(m.branch) - len(m.aheadBehind) - 4
		if padding < 2 {
			padding = 2
		}
		title += strings.Repeat(" ", padding) + dimStyle.Render(m.branch+": "+m.aheadBehind)
	}

	content := transformGraph(m.localGraph, innerWidth-2, maxCommits*2)

	separator := dimStyle.Render(strings.Repeat("─", innerWidth))

	return separator + "\n" + title + "\n" + content
}

// ── Graph transformation ──────────────────────────────

type graphLine struct {
	chars      []byte
	message    string
	decoration string
	starCol    int
	connector  bool
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
		raw := strings.TrimSpace(line[graphEnd:])
		// Extract branch decoration from %d format: "message (HEAD -> main, origin/main)"
		if i := strings.LastIndex(raw, " ("); i >= 0 && strings.HasSuffix(raw, ")") {
			gl.message = strings.TrimSpace(raw[:i])
			gl.decoration = cleanDecoration(raw[i+1:])
		} else {
			gl.message = raw
		}
	}

	return gl
}

// cleanDecoration removes HEAD pointer and tag refs, keeping only branch names.
func cleanDecoration(raw string) string {
	// raw is "(HEAD -> main, origin/main)"
	inner := raw[1 : len(raw)-1]
	parts := strings.Split(inner, ", ")
	var cleaned []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "HEAD -> ")
		if p == "HEAD" || strings.HasPrefix(p, "tag: ") {
			continue
		}
		cleaned = append(cleaned, p)
	}
	if len(cleaned) == 0 {
		return ""
	}
	return "(" + strings.Join(cleaned, ", ") + ")"
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

	if gl.connector || (gl.message == "" && gl.decoration == "") {
		return visual.String()
	}

	prefixWidth := len(gl.chars)
	available := maxWidth - prefixWidth - 1
	if available < 10 {
		available = 10
	}

	msg := gl.message
	deco := gl.decoration

	if deco != "" {
		// Reserve space for decoration, truncate message if needed
		decoLen := len(deco) + 1
		msgSpace := available - decoLen
		if msgSpace < 10 {
			msgSpace = 10
		}
		if len(msg) > msgSpace {
			msg = msg[:msgSpace-3] + "..."
		}
		return visual.String() + " " + msg + " " + branchStyle.Render(deco)
	}

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
