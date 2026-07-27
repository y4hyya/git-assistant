package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Helpers ────────────────────────────────────────────

// historyEntries builds n synthetic rows, newest first.
func historyEntries(n int) []git.CommitEntry {
	entries := make([]git.CommitEntry, 0, n)
	for i := 0; i < n; i++ {
		entries = append(entries, git.CommitEntry{
			SHA:     fmt.Sprintf("c0mm1%02d", i),
			Subject: fmt.Sprintf("chore: commit number %d", i),
			Author:  "Yahya",
			Age:     "2h ago",
		})
	}
	return entries
}

// historyModel parks a model on the browser with n synthetic commits already
// loaded. exhausted by default: pagination has its own tests, and every other
// test would otherwise fire a background page load on its first keypress.
func historyModel(t *testing.T, n int) Model {
	t.Helper()
	m := wizardModel(t, stepHistory)
	m.historyRef = "main"
	m.historyEntries = historyEntries(n)
	m.historyTotal = n
	m.historyExhausted = true
	return m
}

// pump feeds one async result back through Update.
//
// The browser dispatches its reads inside a tea.Batch with the spinner tick,
// and running that batch would sleep a frame for nothing — so tests call the
// command that matters directly and hand its message here.
func pump(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	out, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want ui.Model", next)
	}
	return out
}

// ── Menu visibility ────────────────────────────────────

func TestMenuHistoryEntryCountsCommits(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := wizardModel(t, stepMenu)

	m.historyTotal = 0
	for _, item := range m.menuItems() {
		if item.name == "History" {
			t.Fatal("the menu offers History in a repository with no commits")
		}
	}

	m.historyTotal = 1
	if desc := menuDesc(t, m, "History"); desc != "1 commit" {
		t.Errorf("History desc = %q, want %q", desc, "1 commit")
	}
	m.historyTotal = 312
	if desc := menuDesc(t, m, "History"); desc != "312 commits" {
		t.Errorf("History desc = %q, want %q", desc, "312 commits")
	}
}

func menuDesc(t *testing.T, m Model, name string) string {
	t.Helper()
	for _, item := range m.menuItems() {
		if item.name == name {
			return item.desc
		}
	}
	t.Fatalf("%q entry missing from the menu", name)
	return ""
}

// The browser reads HEAD and writes nothing, so it survives the detached-HEAD
// menu — and "which commit am I actually on" is the question that state raises.
func TestDetachedMenuKeepsTheHistoryBrowser(t *testing.T) {
	m := wizardModel(t, stepMenu)
	m.detached = true
	m.branch = git.DetachedLabel
	m.historyTotal = 4

	var found bool
	for _, item := range m.menuItems() {
		if item.name == "History" {
			found = true
		}
	}
	if !found {
		t.Fatal("the detached menu hides History")
	}
	// git.DetachedLabel is a display string; `git log "HEAD (detached)"` is a
	// fatal. HEAD is both safe and the right answer.
	if rev := m.historyRev(); rev != "HEAD" {
		t.Errorf("historyRev = %q while detached, want HEAD", rev)
	}
}

func TestDetachedHistoryHeaderNamesTheCommit(t *testing.T) {
	m := historyModel(t, 3)
	m.detached = true
	m.branch = git.DetachedLabel
	m.historyEntries[0].SHA = "abc1234"

	if label := m.historyLabel(); label != "abc1234 (detached HEAD)" {
		t.Errorf("header label = %q, want the detached commit", label)
	}
	if !strings.Contains(m.View(), "abc1234 (detached HEAD)") {
		t.Errorf("the header does not label the detached state:\n%s", m.View())
	}
	// Before the first page lands there is no SHA to name.
	m.historyEntries = nil
	if label := m.historyLabel(); label != "detached HEAD" {
		t.Errorf("label with nothing loaded = %q", label)
	}
}

func TestHistoryHeaderNamesTheBranch(t *testing.T) {
	m := historyModel(t, 2)
	m.branch = "feature/x"
	if !strings.Contains(m.View(), "History "+symEmDash+" feature/x") {
		t.Errorf("the header does not name the branch being browsed:\n%s", m.View())
	}
}

func TestDashboardSnapshotCarriesTheCommitCount(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	snap := readDashboard("main", false)
	if snap.totalCommits != 1 {
		t.Fatalf("snapshot counted %d commits, want 1", snap.totalCommits)
	}
	m := wizardModel(t, stepMenu)
	m.historyTotal = 0
	m.applyDashboard(snap)
	if m.historyTotal != 1 {
		t.Errorf("applyDashboard did not carry the count onto the model (%d)", m.historyTotal)
	}
}

// ── Entering ───────────────────────────────────────────

func TestEnterHistoryDefersTheReadAndLoadsTheBranch(t *testing.T) {
	tempRepo(t, "feat: the first commit", "")
	m := wizardModel(t, stepMenu)
	m.historyTotal = 1
	m.menuCursor = menuIndex(t, m, "History")

	m, cmd := key(t, m, "enter")
	if m.step != stepHistory {
		t.Fatalf("enter on History went to step %v", m.step)
	}
	if cmd == nil {
		t.Fatal("entering the browser read nothing")
	}
	// Nothing is read synchronously: the screen shows its spinner first.
	if !m.historyLoading || len(m.historyEntries) != 0 {
		t.Errorf("the first page was read inside Update (loading=%v, %d entries)",
			m.historyLoading, len(m.historyEntries))
	}
	if !strings.Contains(m.View(), "Reading history") {
		t.Errorf("the loading frame says nothing:\n%s", m.View())
	}

	m = pump(t, m, doHistoryPage(m.historyRef, 0, historyPageSize)())
	if m.historyLoading {
		t.Error("the page landed but the screen is still loading")
	}
	if len(m.historyEntries) != 1 {
		t.Fatalf("loaded %d commits, want 1", len(m.historyEntries))
	}
	if !m.historyExhausted {
		t.Error("a short first page did not end the pagination")
	}
	view := m.View()
	for _, want := range []string{m.historyEntries[0].SHA, "feat: the first commit", "1 commit"} {
		if !strings.Contains(view, want) {
			t.Errorf("the list never says %q:\n%s", want, view)
		}
	}
}

func TestHistoryPageErrorSurfaces(t *testing.T) {
	m := historyModel(t, 0)
	m.historyLoading = true
	m = pump(t, m, historyPageMsg{ref: "main", skip: 0, err: errors.New("git log: boom")})
	if m.historyLoading {
		t.Error("a failed read left the screen loading forever")
	}
	if m.err == nil || !strings.Contains(m.View(), "boom") {
		t.Errorf("the failure is not on screen:\n%s", m.View())
	}
}

// ── The list ───────────────────────────────────────────

func TestHistoryListWindowsAndMarksWhatIsOffScreen(t *testing.T) {
	m := historyModel(t, 60)
	m.height = 24 // a small window, so most of the list is off screen
	rows := m.historyListRows()
	if rows >= 60 {
		t.Fatalf("the window (%d rows) is not smaller than the list", rows)
	}
	if !strings.Contains(m.View(), "more") {
		t.Errorf("nothing tells the user the list continues:\n%s", m.View())
	}

	for i := 0; i < 59; i++ {
		m, _ = key(t, m, "down")
	}
	if m.historyCursor != 59 {
		t.Fatalf("cursor stopped at %d", m.historyCursor)
	}
	if m.historyScroll+rows <= m.historyCursor {
		t.Errorf("the cursor (%d) is outside the window [%d,%d)",
			m.historyCursor, m.historyScroll, m.historyScroll+rows)
	}
	if !strings.Contains(m.View(), "commit number 59") {
		t.Errorf("the oldest commit is unreachable:\n%s", m.View())
	}
	// Down at the end is a no-op rather than an index past the slice.
	m, _ = key(t, m, "down")
	if m.historyCursor != 59 {
		t.Errorf("down past the end moved the cursor to %d", m.historyCursor)
	}
}

func TestHistoryRowShowsShaAgeSubjectAndRefs(t *testing.T) {
	m := historyModel(t, 1)
	m.historyEntries[0] = git.CommitEntry{
		SHA: "abc1234", Age: "3d ago", Author: "Yahya",
		Subject: "feat: add the thing",
		Refs:    []string{"HEAD -> main", "tag: v1.3.0"},
	}
	view := m.View()
	for _, want := range []string{"abc1234", "3d ago", "feat: add the thing", "main", "v1.3.0"} {
		if !strings.Contains(view, want) {
			t.Errorf("the row never says %q:\n%s", want, view)
		}
	}
}

// A merge is the one row whose patch is usually empty, so it says so in a word
// a beginner already knows rather than in a glyph they would have to look up.
func TestMergeRowsAreMarked(t *testing.T) {
	m := historyModel(t, 2)
	m.historyEntries[0].IsMerge = true
	m.historyEntries[0].Subject = "Merge branch 'feature'"
	if !strings.Contains(m.View(), "merge Merge branch 'feature'") {
		t.Errorf("the merge row carries no marker:\n%s", m.View())
	}
}

func TestHistoryKeysOnAnEmptyListDoNotPanic(t *testing.T) {
	m := historyModel(t, 0)
	for _, k := range []string{"up", "down", "enter", "d", "j", "k", "p"} {
		m, _ = key(t, m, k)
	}
	if m.step != stepHistory {
		t.Errorf("a key on the empty list navigated away (step %v)", m.step)
	}
	if m.historyShowDetail {
		t.Error("a detail pane opened with no commit under the cursor")
	}
}

// ── Pagination ─────────────────────────────────────────

func TestCursorNearTheEndFetchesTheNextPage(t *testing.T) {
	m := historyModel(t, 30)
	m.historyExhausted = false
	m.historyCursor = 30 - historyPrefetchMargin - 1

	// One row short of the margin: nothing is requested yet.
	if cmd := m.maybeLoadOlderCommits(); cmd != nil {
		t.Fatal("a page was requested before the cursor reached the margin")
	}

	m, cmd := key(t, m, "down")
	if cmd == nil {
		t.Fatal("reaching the margin did not request the next page")
	}
	if !m.historyPaging {
		t.Error("the in-flight flag was not set")
	}
	if !strings.Contains(m.View(), "loading older commits") {
		t.Errorf("the screen does not say a page is on its way:\n%s", m.View())
	}

	// One request at a time: every further keypress must not fork another
	// `git log` while this one is out.
	m2, cmd2 := key(t, m, "down")
	if cmd2 != nil {
		t.Error("a second page was requested while the first was in flight")
	}
	_ = m2
}

func TestExhaustedHistoryStopsAsking(t *testing.T) {
	m := historyModel(t, 30) // exhausted by default
	m.historyCursor = 29
	if cmd := m.maybeLoadOlderCommits(); cmd != nil {
		t.Error("a page was requested after the history had ended")
	}
}

// Pages are appended, never merged or re-sorted: the cursor is an index into
// this slice and the user may be holding an arrow key when one lands.
func TestPagesAppendAndKeepTheCursorStill(t *testing.T) {
	m := historyModel(t, 30)
	m.historyExhausted = false
	m.historyPaging = true
	m.historyCursor = 25
	m.historyScroll = 12
	firstSHA := m.historyEntries[0].SHA

	older := []git.CommitEntry{
		{SHA: "old00001", Subject: "chore: older 1", Age: "3d ago"},
		{SHA: "old00002", Subject: "chore: older 2", Age: "3d ago"},
	}
	m = pump(t, m, historyPageMsg{ref: "main", skip: 30, entries: older})

	if len(m.historyEntries) != 32 {
		t.Fatalf("list has %d entries, want 32 (appended, not replaced)", len(m.historyEntries))
	}
	if m.historyEntries[0].SHA != firstSHA {
		t.Error("the append rewrote the top of the list")
	}
	if m.historyEntries[30].SHA != "old00001" {
		t.Errorf("the page did not land at the end: %v", m.historyEntries[30])
	}
	if m.historyCursor != 25 || m.historyScroll != 12 {
		t.Errorf("the append moved the cursor/window to %d/%d", m.historyCursor, m.historyScroll)
	}
	if m.historyPaging {
		t.Error("the in-flight flag survived the page")
	}
	// Short page: there is nothing older.
	if !m.historyExhausted {
		t.Error("a short page did not end the pagination")
	}
}

func TestAFullPageKeepsThePaginationOpen(t *testing.T) {
	m := historyModel(t, historyPageSize)
	m.historyExhausted = false
	m.historyPaging = true
	m = pump(t, m, historyPageMsg{
		ref: "main", skip: historyPageSize, entries: historyEntries(historyPageSize),
	})
	if m.historyExhausted {
		t.Error("a full page was mistaken for the end of the history")
	}
	if len(m.historyEntries) != 2*historyPageSize {
		t.Errorf("list has %d entries, want %d", len(m.historyEntries), 2*historyPageSize)
	}
}

// A page for a branch we have left describes another repository state, and a
// page whose skip no longer matches the loaded length would splice a gap into
// the middle of the history.
func TestStalePagesAreDropped(t *testing.T) {
	t.Run("another ref", func(t *testing.T) {
		m := historyModel(t, 30)
		m.historyPaging = true
		m = pump(t, m, historyPageMsg{ref: "some-other-branch", skip: 30, entries: historyEntries(5)})
		if len(m.historyEntries) != 30 {
			t.Errorf("a page for another ref was appended (%d entries)", len(m.historyEntries))
		}
		if !m.historyPaging {
			t.Error("a page for another ref cleared this screen's in-flight flag")
		}
	})
	t.Run("wrong offset", func(t *testing.T) {
		m := historyModel(t, 30)
		m.historyExhausted = false
		m.historyPaging = true
		m = pump(t, m, historyPageMsg{ref: "main", skip: 99, entries: historyEntries(5)})
		if len(m.historyEntries) != 30 {
			t.Errorf("an out-of-order page was spliced in (%d entries)", len(m.historyEntries))
		}
		if m.historyPaging {
			t.Error("the in-flight flag was not cleared, so no page can ever be requested again")
		}
	})
}

// ── Detail and patch ───────────────────────────────────

func TestDetailOpensAndThePatchToggles(t *testing.T) {
	tempRepo(t, "feat: the subject", "The body explains why.")
	m := wizardModel(t, stepHistory)
	m.historyRef = "main"
	m = pump(t, m, doHistoryPage("main", 0, historyPageSize)())
	if len(m.historyEntries) != 1 {
		t.Fatalf("loaded %d commits, want 1", len(m.historyEntries))
	}

	m, cmd := key(t, m, "enter")
	if !m.historyShowDetail || !m.historyDetailLoading {
		t.Fatalf("enter did not open a loading detail pane (%v/%v)",
			m.historyShowDetail, m.historyDetailLoading)
	}
	if cmd == nil {
		t.Fatal("the detail was not read")
	}
	if !strings.Contains(m.View(), "Reading commit") {
		t.Errorf("the loading frame says nothing:\n%s", m.View())
	}

	m = pump(t, m, doCommitDetail(m.historyDetailSHA)())
	if m.historyDetailLoading {
		t.Error("the detail landed but the pane is still loading")
	}
	view := m.View()
	for _, want := range []string{
		"commit", "author", "date",
		"feat: the subject", "The body explains why.",
		"t@t.invalid", "seed.txt", "1 file changed",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the detail pane never says %q:\n%s", want, view)
		}
	}

	// p reads the patch, once. A second toggle reuses it.
	m, cmd = key(t, m, "p")
	if cmd == nil || !m.historyPatchLoading {
		t.Fatal("p did not read the patch")
	}
	m = pump(t, m, doCommitPatch(m.historyDetailSHA)())
	if !m.historyShowPatch || !m.historyPatchLoaded {
		t.Fatal("the patch landed but is not on screen")
	}
	patchView := m.View()
	for _, want := range []string{"diff --git", "+seed", "Lines 1-"} {
		if !strings.Contains(patchView, want) {
			t.Errorf("the patch pane never says %q:\n%s", want, patchView)
		}
	}

	m, cmd = key(t, m, "p")
	if m.historyShowPatch {
		t.Error("p did not hide the patch")
	}
	m, cmd = key(t, m, "p")
	if cmd != nil {
		t.Error("re-opening the patch read it a second time")
	}
	if !m.historyShowPatch {
		t.Error("p did not bring the cached patch back")
	}
}

func TestPatchScrollsAndStopsAtTheLastLine(t *testing.T) {
	m := historyModel(t, 1)
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyPatchLoaded = true
	m.historyShowPatch = true
	m.historyPatch = git.CommitPatchInfo{Patch: strings.TrimRight(strings.Repeat("+line\n", 200), "\n")}

	m, _ = key(t, m, "down")
	if m.historyPatchScroll != 1 {
		t.Errorf("down did not scroll the patch (%d)", m.historyPatchScroll)
	}
	m, _ = key(t, m, "up")
	m, _ = key(t, m, "up")
	if m.historyPatchScroll != 0 {
		t.Errorf("up scrolled past the top (%d)", m.historyPatchScroll)
	}
	for i := 0; i < 400; i++ {
		m, _ = key(t, m, "down")
	}
	if want := m.historyPatchMaxScroll(); m.historyPatchScroll != want {
		t.Errorf("scroll stopped at %d, want the bound %d", m.historyPatchScroll, want)
	}
	if !strings.Contains(m.View(), fmt.Sprintf("of %d", 200)) {
		t.Errorf("the counter does not name the whole patch:\n%s", m.View())
	}
}

// A footer that offers ↑↓ on a one-line patch is describing a key that does
// nothing — the confirm screen sets the same precedent.
func TestScrollKeysAppearOnlyWhereThereIsScrolling(t *testing.T) {
	m := historyModel(t, 1)
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyPatchLoaded = true
	m.historyShowPatch = true
	m.historyPatch = git.CommitPatchInfo{Patch: "@@ -1 +1 @@\n-a\n+b"}
	if strings.Contains(renderHelpRows(m.helpRows()), "scroll") {
		t.Errorf("the footer offers scroll on a three-line patch: %s", renderHelpRows(m.helpRows()))
	}

	m.height = 24
	m.historyPatch = git.CommitPatchInfo{Patch: strings.TrimRight(strings.Repeat("+x\n", 200), "\n")}
	if !strings.Contains(renderHelpRows(m.helpRows()), "scroll") {
		t.Errorf("a 200-line patch does not advertise scrolling: %s", renderHelpRows(m.helpRows()))
	}
}

// A silently halved patch is one a reader would trust.
func TestTruncatedPatchSaysSoWithASize(t *testing.T) {
	m := historyModel(t, 1)
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyPatchLoaded = true
	m.historyShowPatch = true
	m.historyPatch = git.CommitPatchInfo{
		Patch:     strings.Repeat("+x\n", 40),
		Truncated: true,
		FullKB:    3096,
	}
	view := m.View()
	for _, want := range []string{"patch truncated", "3096 KB"} {
		if !strings.Contains(view, want) {
			t.Errorf("the pane never says %q:\n%s", want, view)
		}
	}
}

// An empty patch has two meanings and only one of them is worth alarming
// anybody about.
func TestEmptyMergePatchIsExplained(t *testing.T) {
	m := historyModel(t, 1)
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyDetail = git.CommitDetail{SHA: "abc1234", Subject: "Merge branch 'x'", IsMerge: true}
	m.historyPatchLoaded = true
	m.historyShowPatch = true

	if !strings.Contains(m.View(), "git combined the two branches cleanly") {
		t.Errorf("an empty merge patch is not explained:\n%s", m.View())
	}
	m.historyDetail.IsMerge = false
	if !strings.Contains(m.View(), "changes no files") {
		t.Errorf("an empty ordinary patch is not explained:\n%s", m.View())
	}
}

func TestDetailFailureFallsBackToTheList(t *testing.T) {
	m := historyModel(t, 3)
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyDetailLoading = true
	m = pump(t, m, historyDetailMsg{sha: "abc1234", err: errors.New("could not read commit")})
	if m.historyShowDetail {
		t.Error("a failed read left an empty detail pane on screen")
	}
	if !strings.Contains(m.View(), "could not read commit") {
		t.Errorf("the failure is not on screen:\n%s", m.View())
	}
}

// The user can walk on before a read lands.
func TestASupersededDetailIsIgnored(t *testing.T) {
	m := historyModel(t, 3)
	m.historyShowDetail = true
	m.historyDetailSHA = "c0mm101"
	m.historyDetailLoading = true
	m = pump(t, m, historyDetailMsg{
		sha:    "c0mm100",
		detail: git.CommitDetail{SHA: "c0mm100", Subject: "the wrong commit"},
	})
	if m.historyDetail.SHA != "" {
		t.Errorf("another commit's detail was installed: %q", m.historyDetail.SHA)
	}
	if !m.historyDetailLoading {
		t.Error("a superseded result cleared the loading flag")
	}
}

func TestDetailScrollsThroughALongMessage(t *testing.T) {
	m := historyModel(t, 1)
	m.height = 24
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyDetail = git.CommitDetail{
		SHA: "abc1234", Subject: "feat: x", Author: "Yahya",
		Body: strings.TrimRight(strings.Repeat("a body line\n", 80), "\n"),
	}
	if m.historyDetailMaxScroll() == 0 {
		t.Fatal("a long detail pane reports nothing to scroll")
	}
	m, _ = key(t, m, "down")
	if m.historyDetailScroll != 1 {
		t.Errorf("down did not scroll the detail pane (%d)", m.historyDetailScroll)
	}
	for i := 0; i < 300; i++ {
		m, _ = key(t, m, "down")
	}
	if want := m.historyDetailMaxScroll(); m.historyDetailScroll != want {
		t.Errorf("scroll stopped at %d, want the bound %d", m.historyDetailScroll, want)
	}
}

// ── Navigation ─────────────────────────────────────────

func TestEscChainsPatchToDetailToListToMenu(t *testing.T) {
	tempRepo(t, "chore: seed", "")
	m := historyModel(t, 3)
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyPatchLoaded = true
	m.historyShowPatch = true

	m, _ = key(t, m, "esc")
	if m.historyShowPatch || !m.historyShowDetail {
		t.Fatalf("esc from the patch went to detail=%v patch=%v", m.historyShowDetail, m.historyShowPatch)
	}
	m, _ = key(t, m, "esc")
	if m.historyShowDetail || m.step != stepHistory {
		t.Fatalf("esc from the detail left step=%v detail=%v", m.step, m.historyShowDetail)
	}
	if m.historyPatchLoaded {
		t.Error("closing the detail kept the previous commit's patch")
	}
	next, cmd := key(t, m, "esc")
	if next.step != stepMenu {
		t.Fatalf("esc from the list went to step %v", next.step)
	}
	if cmd == nil {
		t.Error("returning to the menu did not ask for a dashboard refresh")
	}
	if len(next.historyEntries) != 0 {
		t.Error("the loaded history is still held after leaving the browser")
	}
	// A page that was in flight when the user left must not repopulate it, and
	// its failure must not surface as a banner on the dashboard.
	late := pump(t, next, historyPageMsg{ref: next.historyRef, skip: 0, err: errors.New("git log: boom")})
	if late.err != nil || len(late.historyEntries) != 0 {
		t.Errorf("a late page reached the dashboard (err=%v, %d entries)",
			late.err, len(late.historyEntries))
	}
}

// The whole surface is read-only in v1.3: no checkout, no revert-from-here, no
// cherry-pick. Nothing on these screens may write, and the footer must not
// advertise anything that does.
func TestTheBrowserHasNoMutationKeys(t *testing.T) {
	m := historyModel(t, 5)
	before := len(m.historyEntries)
	for _, k := range []string{"a", "c", "m", "r", "u", "s", "S", "g", "b", "f", "x", "y", "!"} {
		var cmd tea.Cmd
		m, cmd = key(t, m, k)
		if cmd != nil {
			t.Errorf("%q dispatched a command on the history list", k)
		}
		if m.step != stepHistory {
			t.Fatalf("%q navigated away from the browser (step %v)", k, m.step)
		}
	}
	if len(m.historyEntries) != before {
		t.Error("a key changed the list")
	}

	m.showHelp = true
	overlay := m.View()
	for _, forbidden := range []string{"checkout", "revert", "cherry", "reset", "delete", "drop"} {
		if strings.Contains(strings.ToLower(overlay), forbidden) {
			t.Errorf("the key list offers %q on a read-only screen:\n%s", forbidden, overlay)
		}
	}
}

// ── Geometry ───────────────────────────────────────────

// Same invariant the file selector and the menu are held to: the box keeps its
// floor and no row is wider than the terminal, at every size — with the long
// subjects, long ref names and long patch lines that break naive layouts.
func TestHistoryScreensKeepTheirBorderAtEverySize(t *testing.T) {
	longRefs := []string{"HEAD -> feature/a-very-long-branch-name-indeed",
		"origin/feature/a-very-long-branch-name-indeed", "tag: v1.3.0-release-candidate.1"}
	screens := map[string]func(m *Model){
		"list": func(m *Model) {
			m.historyEntries = historyEntries(40)
			m.historyEntries[0].Subject = strings.Repeat("a very long commit subject ", 12)
			m.historyEntries[0].Refs = longRefs
			m.historyEntries[1].IsMerge = true
			m.historyCursor = 20
		},
		"detail": func(m *Model) {
			m.historyEntries = historyEntries(3)
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			m.historyDetail = git.CommitDetail{
				SHA: "abc1234", FullSHA: strings.Repeat("ab", 20),
				Subject: strings.Repeat("long subject ", 20),
				Body:    strings.TrimRight(strings.Repeat("a body line that runs on and on and on\n", 40), "\n"),
				Author:  "Yahya", Email: "y@example.invalid",
				Date: "2026-07-26 14:03", Age: "2h ago", Refs: longRefs,
				Stat: " internal/ui/step_history.go | 420 ++++++++++++++++++++++\n 1 file changed",
			}
		},
		"patch": func(m *Model) {
			m.historyEntries = historyEntries(3)
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			// The subject rides in the pane's subtitle, so it is part of the
			// geometry too.
			m.historyDetail = git.CommitDetail{SHA: "abc1234", Subject: strings.Repeat("long subject ", 20)}
			m.historyPatchLoaded, m.historyShowPatch = true, true
			m.historyPatch = git.CommitPatchInfo{
				Patch:     "+" + strings.Repeat("x", 400) + "\n" + strings.TrimRight(strings.Repeat("-line\n", 200), "\n"),
				Truncated: true, FullKB: 4096,
			}
		},
		"empty merge patch": func(m *Model) {
			m.historyEntries = historyEntries(3)
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			m.historyDetail = git.CommitDetail{SHA: "abc1234", IsMerge: true}
			m.historyPatchLoaded, m.historyShowPatch = true, true
		},
		"loading": func(m *Model) {
			m.branch = strings.Repeat("a-long-branch-name/", 8)
			m.historyLoading = true
		},
	}
	for name, setup := range screens {
		for _, w := range []int{40, 60, 80, 120} {
			for _, h := range []int{12, 20, 30, 44} {
				m := wizardModel(t, stepHistory)
				m.width, m.height = w, h
				m.historyRef, m.historyExhausted = "main", true
				setup(&m)

				lines := strings.Split(m.viewHistory(), "\n")
				last := lines[len(lines)-1]
				if !strings.Contains(last, "╰") || !strings.Contains(last, "╯") {
					t.Errorf("%s at %dx%d: bottom border missing, last line = %q", name, w, h, last)
				}
				if got := len(lines); got > h {
					t.Errorf("%s at %dx%d: box is %d rows tall", name, w, h, got)
				}
				for i, line := range lines {
					if got := lipgloss.Width(line); got > w {
						t.Errorf("%s at %dx%d: line %d is %d cells wide: %q", name, w, h, i, got, line)
					}
				}
			}
		}
	}
}

func TestHistoryQuits(t *testing.T) {
	for _, screen := range []func() Model{
		func() Model { return historyModel(t, 3) },
		func() Model {
			m := historyModel(t, 3)
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			return m
		},
		func() Model {
			m := historyModel(t, 3)
			m.historyShowDetail = true
			m.historyDetailSHA = "abc1234"
			m.historyPatchLoaded, m.historyShowPatch = true, true
			return m
		},
	} {
		m, _ := key(t, screen(), "q")
		if !m.quitting {
			t.Error("q did not quit")
		}
	}
}

// The spinner on this screen means one thing — something is being read — so
// it has to tick for exactly as long as that is true. Forwarding ticks
// unconditionally would leave the browser re-rendering ten times a second
// forever; not forwarding them during a read freezes the only indication that
// anything is happening.
func TestHistorySpinnerTicksOnlyWhileSomethingIsBeingRead(t *testing.T) {
	tempRepo(t, "chore: seed", "")

	idle := historyModel(t, 3)
	if idle.historyBusy() {
		t.Fatal("an idle browser reports itself busy")
	}
	if _, cmd := idle.Update(idle.spinner.Tick()); cmd != nil {
		t.Fatal("the spinner keeps ticking with nothing in flight")
	}

	// Each of the four reads this screen can have outstanding counts.
	reads := []struct {
		name string
		set  func(*Model)
	}{
		{"first page", func(m *Model) { m.historyLoading = true }},
		{"next page", func(m *Model) { m.historyPaging = true }},
		{"commit detail", func(m *Model) { m.historyDetailLoading = true }},
		{"patch", func(m *Model) { m.historyPatchLoading = true }},
	}
	for _, r := range reads {
		t.Run(r.name, func(t *testing.T) {
			m := historyModel(t, 3)
			r.set(&m)
			if !m.historyBusy() {
				t.Fatal("a read is in flight but the screen reports itself idle")
			}
			if _, cmd := m.Update(m.spinner.Tick()); cmd == nil {
				t.Fatal("the spinner stopped while a read was in flight")
			}
		})
	}
}

// ── An abandoned patch request ─────────────────────────

// Input is deliberately not blocked while a read is out, so `p` on a large
// commit, esc back to the list and enter on the SAME commit used to let the
// abandoned patch land: the guard only asked which commit it was about, not
// whether a patch was still wanted. The patch pane then opened itself over the
// detail pane the user had just asked for, while that detail was still loading.
func TestAnAbandonedPatchDoesNotOpenItselfLater(t *testing.T) {
	m := historyModel(t, 3)
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyPatchLoading = true // p was pressed on abc1234

	// esc: the request is abandoned, everything about the pane is dropped.
	m, _ = key(t, m, "esc")
	if m.historyPatchLoading || m.historyShowDetail {
		t.Fatal("esc did not close the detail pane")
	}

	// The user opens the same commit again — detail only, still loading.
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyDetailLoading = true

	next, _ := m.Update(historyPatchMsg{
		sha:  "abc1234",
		info: git.CommitPatchInfo{Patch: "@@ -1 +1 @@\n-a\n+b\n"},
	})
	after := next.(Model)
	if after.historyShowPatch {
		t.Error("the abandoned patch opened its pane over the detail the user asked for")
	}
	if after.historyPatchLoaded {
		t.Error("the abandoned patch was cached as this pane's content")
	}
	if !after.historyDetailLoading {
		t.Error("the detail request was disturbed by the stale result")
	}
}

// A patch that IS still wanted opens, as it always did.
func TestAPatchThatIsStillWantedOpens(t *testing.T) {
	m := historyModel(t, 3)
	m.historyShowDetail = true
	m.historyDetailSHA = "abc1234"
	m.historyPatchLoading = true

	next, _ := m.Update(historyPatchMsg{
		sha:  "abc1234",
		info: git.CommitPatchInfo{Patch: "@@ -1 +1 @@\n-a\n+b\n"},
	})
	after := next.(Model)
	if !after.historyShowPatch || !after.historyPatchLoaded {
		t.Error("the patch the user is waiting for did not open")
	}
}
