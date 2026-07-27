package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/git"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── The commit history browser ──────────────────────────
//
// Until this screen existed the only history in the app was the dashboard's
// ten-line graph of bare subjects: no SHA, no author, no date, and nothing that
// said what any of those commits changed. A beginner asking "what did I do
// yesterday?" or "what exactly did that commit touch?" had to leave for a
// terminal — which is the one thing this tool exists to avoid.
//
// Two decisions shape everything below.
//
//   - READ-ONLY. No checkout, no revert, no cherry-pick, no reset. Every key on
//     these screens moves the cursor or fetches something to look at. History is
//     the record of work already done, and a mis-key on a browsing screen must
//     not be able to rewrite it.
//   - ONE BRANCH. `git log <branch>`, never --all: the menu graph already shows
//     the cross-branch picture, and this screen answers the different question
//     "what is the story of the branch I am on". The header names the branch so
//     the two are never confused.

const (
	// historyPageSize is how many commits one fetch reads. Paged because a
	// repository with fifty thousand commits is ordinary and reading all of
	// them to draw twenty rows would freeze the UI at the exact moment the user
	// asked to look at something.
	historyPageSize = 200

	// historyPrefetchMargin is how close to the end of the loaded list the
	// cursor gets before the next page is requested. Wide enough that the page
	// lands before a held-down arrow key reaches the bottom, so scrolling never
	// visibly stops.
	historyPrefetchMargin = 20
)

// ── Async results ───────────────────────────────────────

// historyPageMsg carries one page. ref and skip are echoed back because both
// are how the handler decides what to do with it: a page for a ref we have
// since left describes another branch, and a page whose skip no longer matches
// the loaded length would splice a gap into the list.
type historyPageMsg struct {
	ref     string
	skip    int
	entries []git.CommitEntry
	err     error
}

// historyDetailMsg carries one commit's full record. sha identifies which
// commit asked for it — the user can walk on before it lands.
type historyDetailMsg struct {
	sha    string
	detail git.CommitDetail
	err    error
}

// historyPatchMsg carries one commit's patch, capped (see git.CommitPatch).
type historyPatchMsg struct {
	sha  string
	info git.CommitPatchInfo
	err  error
}

// ── Async commands ──────────────────────────────────────
//
// Every read on this screen is a command. `git log` on a large repository takes
// long enough to be felt, and a View func runs on every keypress, every resize
// and every spinner tick — the hard rule of this codebase is that no git
// process is ever started from one.

func doHistoryPage(ref string, skip, limit int) tea.Cmd {
	return func() tea.Msg {
		entries, err := git.HistoryPage(ref, skip, limit)
		return historyPageMsg{ref: ref, skip: skip, entries: entries, err: err}
	}
}

func doCommitDetail(sha string) tea.Cmd {
	return func() tea.Msg {
		detail, err := git.GetCommitDetail(sha)
		return historyDetailMsg{sha: sha, detail: detail, err: err}
	}
}

func doCommitPatch(sha string) tea.Cmd {
	return func() tea.Msg {
		info, err := git.CommitPatch(sha)
		return historyPatchMsg{sha: sha, info: info, err: err}
	}
}

// ── Entry ───────────────────────────────────────────────

// enterHistory opens the browser on the current branch and asks for the first
// page. Nothing is read synchronously: the screen draws its spinner on the next
// frame and fills in a moment later.
func (m *Model) enterHistory() tea.Cmd {
	m.step = stepHistory
	m.historyRef = m.historyRev()
	m.historyEntries = nil
	m.historyCursor = 0
	m.historyScroll = 0
	m.historyExhausted = false
	m.historyPaging = false
	m.historyLoading = true
	m.closeHistoryDetail()
	return tea.Batch(doHistoryPage(m.historyRef, 0, historyPageSize), m.spinner.Tick)
}

// historyRev is the revision the pages are read from. Detached HEAD keeps the
// browser — "where am I?" is precisely the question in that state, and HEAD
// answers it — but m.branch is git.DetachedLabel there, a display string git
// would reject outright.
func (m Model) historyRev() string {
	if m.detached {
		return "HEAD"
	}
	return m.branch
}

// historyAvailable gates the dashboard entry. A repository with no commits has
// no history to browse, and offering an empty screen to somebody who has not
// committed yet is one more thing to wonder about.
func (m Model) historyAvailable() bool { return m.historyTotal > 0 }

// historyBusy reports whether something is being read right now — which is the
// only thing the spinner on these screens ever means.
func (m Model) historyBusy() bool {
	return m.historyLoading || m.historyPaging || m.historyDetailLoading || m.historyPatchLoading
}

// historyLabel names what is being browsed, for the step header. Detached HEAD
// is labelled with the commit itself: there is no branch name to print, and the
// SHA is what the user needs in order to find their way back.
func (m Model) historyLabel() string {
	if !m.detached {
		return m.branch
	}
	if len(m.historyEntries) > 0 {
		return m.historyEntries[0].SHA + " (detached HEAD)"
	}
	return "detached HEAD"
}

// historyListRows is how many commit rows the list draws — listRows minus the
// footer rows past the first, exactly as the file selector and the stash
// manager budget. Read off the footer rather than hardcoded, so adding a key
// cannot silently delete the footer instead of one list row.
func (m Model) historyListRows() int {
	rows := m.listRows() - (m.footerHeight() - 1)
	if rows < 3 {
		rows = 3
	}
	return rows
}

// historySelected returns the commit under the cursor.
func (m Model) historySelected() (git.CommitEntry, bool) {
	if m.historyCursor < 0 || m.historyCursor >= len(m.historyEntries) {
		return git.CommitEntry{}, false
	}
	return m.historyEntries[m.historyCursor], true
}

// closeHistoryDetail drops everything the detail and patch panes hold. Called
// on the way in and on the way out, so a second visit can never open on the
// previous commit's message while its own is still loading.
func (m *Model) closeHistoryDetail() {
	m.historyShowDetail = false
	m.historyDetail = git.CommitDetail{}
	m.historyDetailSHA = ""
	m.historyDetailLoading = false
	m.historyDetailScroll = 0
	m.historyShowPatch = false
	m.historyPatch = git.CommitPatchInfo{}
	m.historyPatchLoaded = false
	m.historyPatchLoading = false
	m.historyPatchScroll = 0
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateHistory(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Input is deliberately NOT blocked while a read is in flight. Nothing here
	// mutates the repository, pages are appended rather than replaced, and
	// freezing the cursor for the duration of a `git log` is exactly the stall
	// this screen was paged to avoid. The spinner keeps ticking meanwhile.
	if _, ok := msg.(spinner.TickMsg); ok {
		if m.historyBusy() {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	if m.historyShowDetail {
		return m.updateHistoryDetail(keyMsg)
	}

	// ── The list ───────────────────────────────────────
	switch keyMsg.String() {
	case "up", "k":
		if m.historyCursor > 0 {
			m.historyCursor--
		}
	case "down", "j":
		if m.historyCursor < len(m.historyEntries)-1 {
			m.historyCursor++
		}
	case "enter", "d":
		return m.openHistoryDetail()
	case "esc":
		// The browser changes nothing, but it is entered from the dashboard and
		// left to it — returnToMenu is the single door back everywhere in this
		// app, and it re-reads a tree that may have moved while the user read.
		//
		// The list is dropped on the way out rather than kept for a faster
		// second visit: someone who scrolled through a large history is holding
		// thousands of entries, enterHistory re-reads from scratch anyway (the
		// history may have moved), and a stale list must never be what the next
		// visit draws its first frame from.
		m.historyEntries = nil
		m.historyExhausted = false
		return m, m.returnToMenu()
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	m.followHistoryCursor()
	return m, m.maybeLoadOlderCommits()
}

// updateHistoryDetail handles the detail pane and the patch pane inside it.
func (m Model) updateHistoryDetail(keyMsg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.historyShowPatch {
		switch keyMsg.String() {
		case "up", "k":
			if m.historyPatchScroll > 0 {
				m.historyPatchScroll--
			}
		case "down", "j":
			if m.historyPatchScroll < m.historyPatchMaxScroll() {
				m.historyPatchScroll++
			}
		case "p", "esc":
			// Back to the message and the stat block. The patch stays loaded, so
			// toggling it again costs nothing.
			m.historyShowPatch = false
		case "q":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}

	switch keyMsg.String() {
	case "up", "k":
		if m.historyDetailScroll > 0 {
			m.historyDetailScroll--
		}
	case "down", "j":
		if m.historyDetailScroll < m.historyDetailMaxScroll() {
			m.historyDetailScroll++
		}
	case "p":
		if m.historyDetailLoading || m.historyPatchLoading || m.historyDetailSHA == "" {
			return m, nil
		}
		if m.historyPatchLoaded {
			m.historyShowPatch = true
			return m, nil
		}
		m.historyPatchLoading = true
		return m, tea.Batch(doCommitPatch(m.historyDetailSHA), m.spinner.Tick)
	case "esc":
		m.closeHistoryDetail()
	case "q":
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// openHistoryDetail moves to the detail pane and asks for the commit. The pane
// opens immediately with a spinner rather than after the read: `git show` on a
// large commit is slow enough to look like a dropped keypress.
func (m Model) openHistoryDetail() (tea.Model, tea.Cmd) {
	e, ok := m.historySelected()
	if !ok {
		return m, nil
	}
	m.closeHistoryDetail()
	m.historyShowDetail = true
	m.historyDetailSHA = e.SHA
	m.historyDetailLoading = true
	return m, tea.Batch(doCommitDetail(e.SHA), m.spinner.Tick)
}

// maybeLoadOlderCommits asks for the next page when the cursor approaches the
// end of the loaded list. One request at a time, and none at all once a short
// page has proved there is nothing older — otherwise every keypress at the
// bottom of a fully-loaded history would fork another `git log`.
func (m *Model) maybeLoadOlderCommits() tea.Cmd {
	if m.historyExhausted || m.historyPaging || m.historyLoading {
		return nil
	}
	if len(m.historyEntries) == 0 {
		return nil
	}
	if m.historyCursor < len(m.historyEntries)-historyPrefetchMargin {
		return nil
	}
	m.historyPaging = true
	return tea.Batch(
		doHistoryPage(m.historyRef, len(m.historyEntries), historyPageSize),
		m.spinner.Tick)
}

// followHistoryCursor keeps the cursor inside the window the view will draw.
func (m *Model) followHistoryCursor() {
	rows := m.historyListRows()
	if m.historyCursor < m.historyScroll {
		m.historyScroll = m.historyCursor
	}
	if m.historyCursor >= m.historyScroll+rows {
		m.historyScroll = m.historyCursor - rows + 1
	}
	if m.historyScroll > len(m.historyEntries)-rows {
		m.historyScroll = len(m.historyEntries) - rows
	}
	if m.historyScroll < 0 {
		m.historyScroll = 0
	}
}

// historyDetailMaxScroll / historyPatchMaxScroll are the largest offsets that
// still end on the last line — the same bound the file diff uses, so a counter
// can never promise a line the keys cannot reach.
func (m Model) historyDetailMaxScroll() int {
	return maxScrollFor(len(m.historyDetailLines()), m.listRows())
}

func (m Model) historyPatchMaxScroll() int {
	return maxScrollFor(len(strings.Split(m.historyPatch.Patch, "\n")), m.listRows())
}

func maxScrollFor(lines, rows int) int {
	if n := lines - rows; n > 0 {
		return n
	}
	return 0
}

// ── Result handling ─────────────────────────────────────

// handleHistoryPage installs one page. Pages are APPENDED, never merged or
// re-sorted: the cursor is an index into this slice and the user may well be
// holding an arrow key while the page lands, so anything that reorders what is
// already on screen moves the row under their cursor.
func (m Model) handleHistoryPage(msg historyPageMsg) (tea.Model, tea.Cmd) {
	if m.step != stepHistory || msg.ref != m.historyRef {
		// A page for a ref we have since left, or for a browser that has been
		// closed: nothing renders it, and its failure would surface as an error
		// banner on whatever screen the user walked to instead.
		return m, nil
	}
	if msg.skip == 0 {
		m.historyLoading = false
	} else {
		m.historyPaging = false
	}
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	if msg.skip == 0 {
		m.historyEntries = msg.entries
	} else {
		if msg.skip != len(m.historyEntries) {
			// The list has changed length since this page was requested.
			// Appending it now would leave a hole or a repeat in the middle of
			// the history; dropping it costs one page-load the next keypress
			// asks for again.
			return m, nil
		}
		m.historyEntries = append(m.historyEntries, msg.entries...)
	}
	// A short page is the only signal git gives that the history ended.
	if len(msg.entries) < historyPageSize {
		m.historyExhausted = true
	}
	m.clampHistoryCursor()
	return m, nil
}

func (m Model) handleHistoryDetail(msg historyDetailMsg) (tea.Model, tea.Cmd) {
	if msg.sha != m.historyDetailSHA {
		return m, nil // superseded: the user opened another commit meanwhile
	}
	m.historyDetailLoading = false
	if msg.err != nil {
		m.err = msg.err
		// Back to the list rather than an empty pane with nothing in it.
		m.closeHistoryDetail()
		return m, nil
	}
	m.historyDetail = msg.detail
	return m, nil
}

func (m Model) handleHistoryPatch(msg historyPatchMsg) (tea.Model, tea.Cmd) {
	// Both halves of the guard matter. The SHA says this patch is about the
	// commit on screen; historyPatchLoading says the patch is still WANTED.
	// Input is never blocked during a read, so `p` then esc then enter on the
	// same commit used to let the abandoned result through — and it opens the
	// patch pane, over the detail pane the user had just asked for and while
	// that detail was still loading. closeHistoryDetail clears the flag, so
	// this is exactly "the request that opened this pane is still out".
	if msg.sha != m.historyDetailSHA || !m.historyPatchLoading {
		return m, nil
	}
	m.historyPatchLoading = false
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	m.historyPatch = msg.info
	// Loaded, not "non-empty": a clean merge's patch is legitimately empty, and
	// without this flag `p` would re-read it on every press.
	m.historyPatchLoaded = true
	m.historyShowPatch = true
	m.historyPatchScroll = 0
	return m, nil
}

// clampHistoryCursor keeps the cursor and the scroll offset on the list as it
// stands. Pages only ever grow the list, but a first page can arrive smaller
// than the one it replaced.
func (m *Model) clampHistoryCursor() {
	if m.historyCursor >= len(m.historyEntries) {
		m.historyCursor = max(0, len(m.historyEntries)-1)
	}
	if m.historyScroll > m.historyCursor {
		m.historyScroll = m.historyCursor
	}
	if m.historyScroll < 0 {
		m.historyScroll = 0
	}
}

// ── View ────────────────────────────────────────────────

func (m Model) viewHistory() string {
	if m.historyShowDetail {
		return m.viewHistoryDetail()
	}

	var b strings.Builder
	b.WriteString(m.historyHeader("History " + symEmDash + " " + m.historyLabel()))

	switch {
	case m.historyLoading && len(m.historyEntries) == 0:
		b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render("Reading history...") + "\n")
	case len(m.historyEntries) == 0:
		// Reachable only in the seconds between the first commit and the next
		// dashboard snapshot — the menu entry is hidden at zero commits.
		b.WriteString("  " + dimStyle.Render("No commits on this branch yet.") + "\n")
	default:
		rows := m.historyListRows()
		start, end := listWindow(len(m.historyEntries), m.historyScroll, rows)
		if start > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp, start)) + "\n")
		}
		for i := start; i < end; i++ {
			cursor := "  "
			if i == m.historyCursor {
				cursor = cursorStyle.Render(symCursor + " ")
			}
			b.WriteString(cursor + m.renderHistoryRow(m.historyEntries[i], i == m.historyCursor) + "\n")
		}
		if end < len(m.historyEntries) {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown, len(m.historyEntries)-end)) + "\n")
		}
		b.WriteString("\n  " + dimStyle.Render(m.historyCountLine()) + "\n")
	}

	// Said quietly, and only while it is true: an older page arriving is not an
	// operation the user started, and the list stays usable throughout.
	if m.historyPaging {
		b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render("loading older commits...") + "\n")
	}

	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return m.styledBox(b.String())
}

// historyHeader is the two-line header every screen of the browser shares.
// Both rows are cell-budgeted: the subtitle carries a commit subject, the badge
// row a branch name, and either can be long enough to wrap (see paneTextWidth).
func (m Model) historyHeader(subtitle string) string {
	const badge = " git-assist " // 12 cells, plus the two spaces after it
	var b strings.Builder
	b.WriteString(titleStyle.Render(badge))
	b.WriteString("  ")
	b.WriteString(fitSpans(m.paneTextWidth(len(badge)+2),
		textSpan{symBranch + " " + m.branch, branchStyle}))
	b.WriteString("\n")
	b.WriteString("  " + fitSpans(m.paneTextWidth(2), textSpan{subtitle, stepStyle}))
	b.WriteString("\n\n")
	return b.String()
}

// historyCountLine says how much of the history is loaded. Honest about the
// difference: "200 commits" on a branch with 4000 of them would read as a
// complete list, and the browser would look like it had lost the rest.
func (m Model) historyCountLine() string {
	loaded := len(m.historyEntries)
	if m.historyTotal > loaded && !m.historyExhausted {
		return fmt.Sprintf("%d of %s loaded", loaded,
			plural(m.historyTotal, "commit", "commits"))
	}
	return plural(loaded, "commit", "commits")
}

// renderHistoryRow draws one commit: "abc1234  2h ago  subject (main)".
//
// The SHA leads because it is how every other screen — and git itself — names a
// commit. Merges carry the word "merge" rather than a glyph: a beginner reading
// this list has no reason to know what a fork symbol means, and the app spells
// out "apply (keep)" and "pop (apply + delete)" for the same reason.
func (m Model) renderHistoryRow(e git.CommitEntry, active bool) string {
	// The box's inner text width, less the cursor column.
	width := m.width - 12
	if width < 20 {
		width = 20
	}

	sha := helpKeyStyle.Render(e.SHA)
	if active {
		sha = highlightStyle.Render(e.SHA)
	}
	used := lipgloss.Width(e.SHA)

	// A padded age column so the subjects line up. Ages are compacted ("2h
	// ago"), so the column is narrow; anything longer just pushes its own row.
	age := padRight(e.Age, historyAgeColumn)
	out := sha + "  " + dimStyle.Render(age)
	used += 2 + lipgloss.Width(age)

	subject := e.Subject
	if e.IsMerge {
		subject = "merge " + subject
	}
	available := width - used - 2
	if available < 8 {
		return out
	}

	deco, decoWidth := "", 0
	if refs := parseDecoRefs(e.Refs); len(refs) > 0 {
		budget := available - lipgloss.Width(subject) - 1
		if budget < minDecoWidth {
			// Refs say WHERE this commit sits — where a branch points, which
			// commit carries the tag. Give them up to half the row and truncate
			// the subject instead of dropping them.
			budget = available / 2
		}
		deco, decoWidth = renderDecoration(refs, budget)
		if decoWidth > 0 {
			decoWidth++ // the space in front of it
		}
	}

	style := dimStyle
	if active {
		style = inactiveStyle
	}
	out += "  " + style.Render(truncateWidth(subject, available-decoWidth))
	if deco != "" {
		out += " " + deco
	}
	return out
}

// historyAgeColumn is the width the age column is padded to. "2h ago",
// "3d ago", "12mo ago" all fit; git's own unrecognised spellings pass through
// compactAge verbatim and push their row, which is better than a lie.
const historyAgeColumn = 9

// paneTextWidth is how many cells a row may spend, after `indent` leading
// cells, before styledBox has to wrap it.
//
// Nothing in these panes may exceed it. clampLines can only ESTIMATE how many
// rows a long line will cost (it divides by the width; lipgloss also breaks on
// word boundaries, which can cost a row more), and when the estimate is low the
// box overflows and MaxHeight takes the bottom border off — the one thing the
// layout must never do. A commit message and a patch line are both arbitrary
// user text, so both are truncated to this before they are styled.
func (m Model) paneTextWidth(indent int) int {
	// The same arithmetic styledBox does: box width, less its own padding.
	box := m.width - 4
	if box < 30 {
		box = 30
	}
	if w := box - 4 - indent; w > 8 {
		return w
	}
	return 8
}

// textSpan is one styled piece of a composed row.
type textSpan struct {
	text  string
	style lipgloss.Style
}

// fitSpans renders spans into at most width cells.
//
// The truncation happens on each span's RAW text, never on rendered output:
// cutting a styled string by cells would slice an escape sequence in half and
// leave the rest of the terminal painted in that colour.
func fitSpans(width int, spans ...textSpan) string {
	var b strings.Builder
	used := 0
	for _, s := range spans {
		if s.text == "" {
			continue
		}
		if used >= width {
			break
		}
		text := truncateWidth(s.text, width-used)
		if text == "" {
			break
		}
		b.WriteString(s.style.Render(text))
		used += lipgloss.Width(text)
	}
	return b.String()
}

// padRight pads to a display-cell width (not a byte count — lipgloss measures
// the cells the terminal actually spends).
func padRight(s string, width int) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// ── Detail pane ─────────────────────────────────────────

func (m Model) viewHistoryDetail() string {
	if m.historyShowPatch {
		return m.viewHistoryPatch()
	}

	var b strings.Builder
	label := "Commit"
	if m.historyDetailSHA != "" {
		label = "Commit " + m.historyDetailSHA
	}
	b.WriteString(m.historyHeader(label))

	if m.historyDetailLoading {
		b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render("Reading commit...") + "\n")
	} else {
		lines := m.historyDetailLines()
		start, end := listWindow(len(lines), m.historyDetailScroll, m.listRows())
		for _, line := range lines[start:end] {
			b.WriteString(line + "\n")
		}
		if len(lines) > m.listRows() {
			b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(
				fmt.Sprintf("Lines %d-%d of %d", start+1, end, len(lines)))))
		}
	}

	if m.historyPatchLoading {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Reading the patch...") + "\n")
	}
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return m.styledBox(b.String())
}

// historyDetailLines builds the whole detail pane as rows, so one scroll offset
// covers the header block, a long commit message and a hundred-file stat block
// alike. Built here rather than in the view because the scroll bound has to
// count exactly the lines the view will draw.
func (m Model) historyDetailLines() []string {
	d := m.historyDetail
	// Two leading spaces on every row; the header block spends another
	// helpKeyColumn cells on its label.
	textWidth := m.paneTextWidth(2)
	fieldWidth := m.paneTextWidth(2 + helpKeyColumn)

	var lines []string
	field := func(label string, spans ...textSpan) {
		if value := fitSpans(fieldWidth, spans...); value != "" {
			lines = append(lines, "  "+dimStyle.Render(padKey(label))+value)
		}
	}
	text := func(s string, style lipgloss.Style) string {
		return "  " + fitSpans(textWidth, textSpan{s, style})
	}

	full := ""
	if d.FullSHA != "" && d.FullSHA != d.SHA {
		full = "  " + d.FullSHA
	}
	field("commit", textSpan{d.SHA, helpKeyStyle}, textSpan{full, dimStyle})
	email := ""
	if d.Email != "" {
		email = " <" + d.Email + ">"
	}
	field("author", textSpan{d.Author, filePathStyle}, textSpan{email, dimStyle})
	age := ""
	if d.Age != "" {
		age = "  (" + d.Age + ")"
	}
	field("date", textSpan{d.Date, filePathStyle}, textSpan{age, dimStyle})
	if refs := parseDecoRefs(d.Refs); len(refs) > 0 {
		// renderDecoration owns its own budget, and it truncates rather than
		// wrapping — so it is handed the width directly.
		if deco, _ := renderDecoration(refs, fieldWidth); deco != "" {
			lines = append(lines, "  "+dimStyle.Render(padKey("refs"))+deco)
		}
	}
	if d.IsMerge {
		// Spelled out: this is the commit whose patch is usually empty, and
		// "nothing changed here" needs a reason attached to it.
		field("merge", textSpan{"joins two lines of history", dimStyle})
	}

	if d.Subject != "" {
		lines = append(lines, "", text(d.Subject, highlightStyle))
	}
	if d.Body != "" {
		lines = append(lines, "")
		for _, line := range strings.Split(d.Body, "\n") {
			lines = append(lines, text(line, filePathStyle))
		}
	}

	lines = append(lines, "")
	if d.Stat == "" {
		lines = append(lines, text(m.historyNoChangesLine(), dimStyle))
	} else {
		for _, line := range strings.Split(d.Stat, "\n") {
			lines = append(lines, text(line, dimStyle))
		}
	}
	return lines
}

// historyNoChangesLine explains an empty stat or an empty patch. The two cases
// look identical on screen and mean completely different things — a merge that
// git resolved by itself records no changes of its own, which is normal and not
// worth alarming anybody about.
func (m Model) historyNoChangesLine() string {
	if m.historyDetail.IsMerge {
		return "This merge changed nothing by itself — git combined the two branches cleanly."
	}
	return "This commit changes no files."
}

// ── Patch pane ──────────────────────────────────────────

// viewHistoryPatch is the same rendering the file diff viewer and the stash
// preview use, against the same window helper, so all three scroll identically.
func (m Model) viewHistoryPatch() string {
	var b strings.Builder
	label := "Patch"
	if m.historyDetailSHA != "" {
		label = "Patch " + m.historyDetailSHA
		if s := m.historyDetail.Subject; s != "" {
			label += "  " + symEmDash + " " + s
		}
	}
	b.WriteString(m.historyHeader(label))

	// The line-number gutter is 5 cells wide, after the two-space indent.
	const gutter = 5
	if strings.TrimSpace(m.historyPatch.Patch) == "" {
		b.WriteString("  " + fitSpans(m.paneTextWidth(2),
			textSpan{m.historyNoChangesLine(), dimStyle}) + "\n")
	} else {
		lines := strings.Split(m.historyPatch.Patch, "\n")
		start, end := listWindow(len(lines), m.historyPatchScroll, m.listRows())
		width := m.paneTextWidth(2 + gutter)
		for i, line := range lines[start:end] {
			lineNum := dimStyle.Render(fmt.Sprintf("%4d ", start+i+1))
			// Truncated BEFORE styling: a diff line is arbitrary user text, and
			// one long enough to wrap costs the box its bottom border.
			b.WriteString("  " + lineNum + styleDiffLine(truncateWidth(line, width)) + "\n")
		}
		b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(
			fmt.Sprintf("Lines %d-%d of %d", start+1, end, len(lines)))))
		if m.historyPatch.Truncated {
			// Honest about the cut, and specific about how much is missing: a
			// silently halved patch is one a reader would trust.
			b.WriteString("  " + fitSpans(m.paneTextWidth(2), textSpan{fmt.Sprintf(
				"%s patch truncated — %d KB in full; showing the first %d KB",
				symWarn, m.historyPatch.FullKB, len(m.historyPatch.Patch)/1024),
				modifiedStyle}) + "\n")
		}
	}

	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return m.styledBox(b.String())
}
