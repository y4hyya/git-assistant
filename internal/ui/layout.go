package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// boxChromeHeight is what styledBox's frame costs vertically: the rounded
// border top and bottom plus one line of padding on each side.
const boxChromeHeight = 4

// styledBox wraps content in the standard bordered box constrained to terminal size.
//
// The content is clamped to the rows that are actually free *before* the box
// is rendered. lipgloss's MaxHeight keeps the FIRST n lines of whatever it is
// applied to, so clamping the rendered box instead deletes its bottom padding
// and border — the frame visibly loses its floor on short terminals, which is
// exactly where a user needs the layout to look intact. MaxHeight stays on as
// a backstop for content that wraps in ways the estimate cannot see.
func (m Model) styledBox(content string) string {
	w := m.width - 4
	if w < 30 {
		w = 30
	}
	h := m.height
	if h < 10 {
		h = 10
	}
	return boxBorder.Width(w).MaxHeight(h).
		Render(clampLines(content, h-boxChromeHeight, w-4))
}

// contentHeight is how many rows a view may write before styledBox has to
// start dropping them. Views that assemble optional sections budget against
// this so the section disappears whole instead of being sliced.
func (m Model) contentHeight() int {
	h := m.height
	if h < 10 {
		h = 10
	}
	return h - boxChromeHeight
}

// clampLines trims content to maxLines terminal rows. width is the box's inner
// text width: a line wider than that costs more than one row once lipgloss
// wraps it, and counting those extra rows is the difference between a budget
// that holds and one that overflows by exactly the number of long lines.
func clampLines(content string, maxLines, width int) string {
	if maxLines < 1 {
		maxLines = 1
	}
	lines := strings.Split(content, "\n")
	used := 0
	for i, line := range lines {
		cost := 1
		if width > 0 {
			if w := lipgloss.Width(line); w > width {
				cost = (w + width - 1) / width
			}
		}
		if used+cost > maxLines {
			if i == 0 {
				return line // always render something
			}
			return strings.Join(lines[:i], "\n")
		}
		used += cost
	}
	return content
}

// renderStatusNote renders the one-shot "what just happened" line for the
// steps that show it. Shared so a merge, a pull, a branch delete or a stash
// round-trip reads identically wherever it was started from — the menu used to
// have nowhere to put this and smuggled it out through the error banner.
// Returns "" when there is nothing to say.
func (m Model) renderStatusNote() string {
	if m.statusNote == "" {
		return ""
	}
	return "  " + successStyle.Render(symDone) + " " + m.statusNote
}

// renderGraphSection returns the unified commit graph footer, sized to fit
// maxLines rows (separator + title + commit rows). The caller owns the budget:
// it is the only code that knows how many rows the menu above already spent,
// and guessing at that from m.height is what used to push the box past the
// terminal and cost it the bottom border.
func (m Model) renderGraphSection(maxLines int) string {
	if m.width < 40 {
		return ""
	}

	// Two rows go to the separator and the title; the rest are commits.
	// Below three commits the panel costs more than it says.
	maxCommits := maxLines - 2
	if maxCommits < 3 {
		return ""
	}
	if maxCommits > 10 {
		maxCommits = 10
	}

	innerWidth := m.width - 12
	if innerWidth < 30 {
		innerWidth = 30
	}

	title := graphTitleStyle.Render("Commit Graph")
	if m.aheadBehind != "" {
		// Right-hand sync chip. Measured in cells, not bytes: the branch name
		// and the "·" separator are routinely multi-byte, and a byte count
		// over-reserves and pushes the chip off the row.
		room := innerWidth - lipgloss.Width(title) - 2
		if room >= 8 {
			suffix := truncateWidth(m.branch+": "+m.aheadBehind, room)
			pad := innerWidth - lipgloss.Width(title) - lipgloss.Width(suffix)
			if pad < 2 {
				pad = 2
			}
			title += strings.Repeat(" ", pad) + dimStyle.Render(suffix)
		}
	}

	content := transformGraph(m.localGraph, innerWidth-2, maxCommits)

	separator := dimStyle.Render(strings.Repeat(symHRule, innerWidth))

	return separator + "\n" + title + "\n" + content
}

// ── Graph transformation ──────────────────────────────

// graphFieldSep is the byte GetUnifiedGraph puts between the subject and the
// ref decorations (git's %x1f). git strips control characters out of header
// lines, so it cannot appear inside a subject — which is the whole point: the
// old " (" heuristic could not tell "fix: handle (edge case)" apart from a
// real branch decoration and painted the subject's own parenthetical cyan.
const graphFieldSep = '\x1f'

// minDecoWidth is the narrowest decoration worth drawing: "(a…)" and a spare
// cell. Anything tighter is punctuation with no information in it.
const minDecoWidth = 6

type decoKind int

const (
	decoBranch decoKind = iota
	decoHead
	decoTag
)

type decoRef struct {
	name string
	kind decoKind
}

type graphLine struct {
	chars     []byte
	message   string
	refs      []decoRef
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

// isGraphChar reports whether line[i] belongs to the graph column rather than
// the commit subject.
//
// '-' and '.' only ever occur in octopus merge rows ("*-----.   Merge …"),
// where git writes them flush against the column they extend — never after a
// space. A subject, by contrast, is always preceded by one. That single rule
// lets the connectors be consumed as graph without eating the leading '-' of
// a subject like "-fix the thing" or a ".gitignore" mention.
func isGraphChar(line string, i int) bool {
	switch line[i] {
	case '*', '|', '/', '\\', ' ', '_':
		return true
	case '-', '.':
		return i > 0 && line[i-1] != ' '
	}
	return false
}

func parseLine(line string) graphLine {
	gl := graphLine{starCol: -1}

	graphEnd := 0
	for i := 0; i < len(line); i++ {
		if !isGraphChar(line, i) {
			break
		}
		ch := line[i]
		gl.chars = append(gl.chars, ch)
		if ch == '*' {
			gl.starCol = i
		}
		graphEnd = i + 1
	}

	if graphEnd >= len(line) {
		return gl
	}

	rest := line[graphEnd:]
	sep := strings.IndexByte(rest, graphFieldSep)
	if sep < 0 {
		// No separator — output from something other than GetUnifiedGraph.
		// Treat every byte as subject rather than guessing where a decoration
		// might start; a guess here is what invented phantom branches.
		gl.message = strings.TrimSpace(rest)
		return gl
	}
	gl.message = strings.TrimSpace(rest[:sep])
	gl.refs = cleanDecoration(rest[sep+1:])
	return gl
}

// cleanDecoration turns git's %d field — " (HEAD -> main, tag: v1.2.0,
// origin/main, origin/HEAD)" — into the refs worth showing, ordered:
// checked-out branch first, then other branches, then tags.
//
// Dropped: bare "HEAD" (the detached-head marker for a row the graph already
// highlights) and any "*/HEAD" symref — origin/HEAD exists in every clone,
// points at whatever origin/main points at, and reading it as a branch put a
// branch on the dashboard that nobody can check out.
//
// Kept, unlike before: tags. A release commit that renders undecorated is a
// release you cannot find in the graph.
func cleanDecoration(raw string) []decoRef {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "(") || !strings.HasSuffix(raw, ")") {
		return nil
	}
	return parseDecoRefs(strings.Split(raw[1:len(raw)-1], ", "))
}

// parseDecoRefs classifies and orders already-split ref names. Split out from
// cleanDecoration because the history browser reads %D, which git prints
// without the parentheses %d wraps around it — the two screens must colour the
// same ref the same way, and that only holds while they share this function.
func parseDecoRefs(parts []string) []decoRef {
	var head, branches, tags []decoRef
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case p == "", p == "HEAD", strings.HasSuffix(p, "/HEAD"):
			continue
		case strings.HasPrefix(p, "HEAD -> "):
			if name := strings.TrimSpace(strings.TrimPrefix(p, "HEAD -> ")); name != "" {
				head = append(head, decoRef{name: name, kind: decoHead})
			}
		case strings.HasPrefix(p, "tag: "):
			if name := strings.TrimSpace(strings.TrimPrefix(p, "tag: ")); name != "" {
				tags = append(tags, decoRef{name: name, kind: decoTag})
			}
		default:
			branches = append(branches, decoRef{name: p, kind: decoBranch})
		}
	}

	refs := make([]decoRef, 0, len(head)+len(branches)+len(tags))
	refs = append(refs, head...)
	refs = append(refs, branches...)
	refs = append(refs, tags...)
	if len(refs) == 0 {
		return nil
	}
	return refs
}

// insertConnectors used to inject a cosmetic `│` line between every
// pair of adjacent linear commits. That doubled the vertical footprint
// for no informational gain — `git log --graph` already emits its own
// connector rows (`|\`, `|/`, `|`) at branch divergence points, and
// those pass through as starCol < 0 lines. Keeping the function as a
// pass-through preserves the call site while letting the compact
// layout stand on its own.
func insertConnectors(lines []graphLine) []graphLine {
	return lines
}

// truncateWidth shortens s to at most width display cells, marking the cut
// with the ellipsis symbol.
//
// Cells, not bytes: byte slicing cuts multi-byte runes in half (a Turkish
// subject ends in a replacement glyph) and counts them as wider than they
// render, so it also truncates far earlier than it needs to.
func truncateWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	ellipsis := symEllipsis
	budget := width - lipgloss.Width(ellipsis)
	if budget < 1 {
		// No room to say "there is more" — just fill the cells.
		ellipsis = ""
		budget = width
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := lipgloss.Width(string(r))
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + ellipsis
}

// refStyle colours a ref by what it is: purple for the branch you are on,
// cyan for other branches, amber for tags.
func refStyle(r decoRef) lipgloss.Style {
	switch r.kind {
	case decoTag:
		return tagStyle
	case decoHead:
		return activeStyle
	}
	return branchStyle
}

// renderDecoration draws "(main, origin/main, v1.2.0)" within a hard cell
// budget, returning the styled text and the cells it occupies.
//
// The budget is the point: decorations used to be appended at full length, so
// a commit carrying four long ref names wrapped its own row inside the box and
// knocked every graph column below it out of alignment.
func renderDecoration(refs []decoRef, budget int) (string, int) {
	if len(refs) == 0 || budget < minDecoWidth {
		return "", 0
	}

	var b strings.Builder
	b.WriteString(dimStyle.Render("("))
	used := 2 // the parens

	for i, ref := range refs {
		sepWidth := 0
		if i > 0 {
			sepWidth = 2 // ", "
		}
		w := lipgloss.Width(ref.name)
		if used+sepWidth+w <= budget {
			if sepWidth > 0 {
				b.WriteString(dimStyle.Render(", "))
			}
			b.WriteString(refStyle(ref).Render(ref.name))
			used += sepWidth + w
			continue
		}
		if i == 0 {
			// One ref that does not fit still beats no ref at all.
			name := truncateWidth(ref.name, budget-used)
			if name == "" {
				return "", 0
			}
			b.WriteString(refStyle(ref).Render(name))
			used += lipgloss.Width(name)
		} else if used+sepWidth+lipgloss.Width(symEllipsis) <= budget {
			b.WriteString(dimStyle.Render(", " + symEllipsis))
			used += sepWidth + lipgloss.Width(symEllipsis)
		}
		break
	}

	b.WriteString(dimStyle.Render(")"))
	return b.String(), used
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
					visual.WriteString(activeStyle.Render(symGraphVert))
				} else {
					visual.WriteString(modifiedStyle.Render(symGraphVert))
				}
			} else {
				visual.WriteString(dimStyle.Render(symGraphVert))
			}
		case '/':
			visual.WriteString(dimStyle.Render("/"))
		case '\\':
			visual.WriteString(dimStyle.Render("\\"))
		case ' ':
			visual.WriteString(" ")
		case '_':
			visual.WriteString(dimStyle.Render("_"))
		case '-':
			visual.WriteString(dimStyle.Render(symGraphDash))
		case '.':
			visual.WriteString(dimStyle.Render(symGraphCorner))
		}
	}

	prefix := visual.String()
	if gl.connector || (gl.message == "" && len(gl.refs) == 0) {
		return prefix
	}

	available := maxWidth - lipgloss.Width(prefix) - 1
	if available < 4 {
		// Deep graph on a narrow terminal: anything else on this row would
		// wrap and drag the columns below it out of line.
		return prefix
	}

	msg := gl.message
	deco, decoWidth := "", 0
	if len(gl.refs) > 0 {
		budget := available - lipgloss.Width(msg) - 1
		if budget < minDecoWidth {
			// The subject alone would crowd the refs out. Refs say *where*
			// this commit sits, so give them up to half the row and truncate
			// the subject instead of dropping them.
			budget = available / 2
		}
		deco, decoWidth = renderDecoration(gl.refs, budget)
		if decoWidth > 0 {
			decoWidth++ // the space in front of it
		}
	}

	msg = truncateWidth(msg, available-decoWidth)

	parts := make([]string, 0, 3)
	parts = append(parts, prefix)
	if msg != "" {
		parts = append(parts, msg)
	}
	if deco != "" {
		parts = append(parts, deco)
	}
	return strings.Join(parts, " ")
}

// formatAheadBehind renders the sync state of a branch. hasUpstream is
// distinct from 0/0: a branch that tracks nothing has never been compared to
// anything, and calling that "up to date" hides that it exists only locally.
func formatAheadBehind(ahead, behind int, hasUpstream bool) string {
	if !hasUpstream {
		return "no upstream"
	}
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
	return strings.Join(parts, " "+symMidDot+" ")
}
