package git

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ── Parsing ────────────────────────────────────────────

// rec builds one historyListFormat record the way git would emit it.
func rec(sha, age, author, parents, refs, subject string) string {
	return strings.Join([]string{sha, age, author, parents, refs, subject},
		historyFieldSep)
}

// The subject is the one field a user controls completely. It is LAST in the
// format for exactly this reason: everything after the fifth separator belongs
// to it, whatever it contains.
//
// want is what the row shows, which is the subject verbatim for every shape of
// TEXT. A control character is the one exception: it is dropped, because a row
// is one line of printable characters and 0x1f/ESC in a cloned repository's
// subject is a rendering attack surface, not content. It no longer has anything
// to do with the parse — see historyFieldSep.
func TestParseHistoryPageKeepsExoticSubjects(t *testing.T) {
	cases := []struct {
		name    string
		subject string
		want    string
	}{
		{"a unit separator", "fix: split on \x1f and survive", "fix: split on  and survive"},
		{"braces and parens", "feat: handle {a} (b) [c]", "feat: handle {a} (b) [c]"},
		{"non-ASCII", "fix: düzeltme — çalışıyor ✓", "fix: düzeltme — çalışıyor ✓"},
		{"a colon-heavy prose subject", "Update the README: add badges: really", "Update the README: add badges: really"},
		{"leading dash", "-fix: not a flag", "-fix: not a flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := rec("abc1234", "2 hours ago", "Yahya", "def5678", "", tc.subject) + "\x00"
			got := parseHistoryPage(data)
			if len(got) != 1 {
				t.Fatalf("parsed %d entries, want 1", len(got))
			}
			if got[0].Subject != tc.want {
				t.Errorf("subject = %q, want %q", got[0].Subject, tc.want)
			}
			if got[0].SHA != "abc1234" {
				t.Errorf("sha = %q, want abc1234", got[0].SHA)
			}
			if got[0].Age != "2h ago" {
				t.Errorf("age = %q, want %q", got[0].Age, "2h ago")
			}
			if got[0].IsMerge {
				t.Error("a one-parent commit was reported as a merge")
			}
		})
	}
}

func TestParseHistoryPageDetectsMergesAndDecorations(t *testing.T) {
	data := rec("aaa1111", "3 days ago", "Yahya", "bbb2222 ccc3333",
		"HEAD -> main, tag: v1.2.0, origin/main", "Merge branch 'feature'") + "\x00" +
		rec("bbb2222", "5 minutes ago", "Mert", "ddd4444", "", "fix: ordinary commit") + "\x00"

	got := parseHistoryPage(data)
	if len(got) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(got))
	}
	if !got[0].IsMerge {
		t.Error("a two-parent commit was not reported as a merge")
	}
	wantRefs := []string{"HEAD -> main", "tag: v1.2.0", "origin/main"}
	if len(got[0].Refs) != len(wantRefs) {
		t.Fatalf("refs = %v, want %v", got[0].Refs, wantRefs)
	}
	for i, want := range wantRefs {
		if got[0].Refs[i] != want {
			t.Errorf("refs[%d] = %q, want %q", i, got[0].Refs[i], want)
		}
	}
	if got[1].Refs != nil {
		t.Errorf("an undecorated commit came back with refs %v", got[1].Refs)
	}
	if got[1].Age != "5m ago" {
		t.Errorf("age = %q, want %q", got[1].Age, "5m ago")
	}
	if got[1].Author != "Mert" {
		t.Errorf("author = %q, want Mert", got[1].Author)
	}
}

// A truncated or otherwise malformed record is dropped rather than rendered as
// a row with the fields shifted one place to the left.
func TestParseHistoryPageDropsMalformedRecords(t *testing.T) {
	data := "abc1234\x1fonly two fields\x00" +
		rec("", "2 hours ago", "Yahya", "d", "", "no sha") + "\x00" +
		rec("bbb2222", "2 hours ago", "Yahya", "d", "", "good") + "\x00"
	got := parseHistoryPage(data)
	if len(got) != 1 || got[0].Subject != "good" {
		t.Fatalf("parsed %v, want just the well-formed record", got)
	}
}

// An ident is NOT a safe field, whatever the old comment claimed. git strips
// control characters only from the ENDS of a configured name, so
// `git -c user.name=$'Evil\x1fName' commit` is accepted and %an emits the byte
// raw. Against a 0x1f-separated record that shifted every field after it: the
// parent SHA arrived as a ref badge and the real decorations were glued onto
// the subject. The separator is a NEWLINE for exactly this reason — git cannot
// put one inside an ident, a ref name or a folded subject.
func TestParseHistoryPageSurvivesAHostileAuthorName(t *testing.T) {
	data := rec("abc1234", "2 hours ago", "Evil\x1fName", "def5678",
		"HEAD -> main", "ident test") + "\x00"
	got := parseHistoryPage(data)
	if len(got) != 1 {
		t.Fatalf("parsed %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Subject != "ident test" {
		t.Errorf("subject = %q — the decorations shifted into it", e.Subject)
	}
	if len(e.Refs) != 1 || e.Refs[0] != "HEAD -> main" {
		t.Errorf("refs = %v, want [HEAD -> main] (a parent SHA rendered as a badge?)", e.Refs)
	}
	if e.IsMerge {
		t.Error("IsMerge was read out of a shifted field")
	}
	// The name survives whole, minus the control byte itself: a raw 0x1f in a
	// list row costs the column its alignment and has no meaning to show.
	if e.Author != "EvilName" {
		t.Errorf("author = %q, want %q", e.Author, "EvilName")
	}
}

// The same thing end to end, because the claim being tested is about what git
// really emits, not about what this parser is fed.
func TestHistoryPageSurvivesAHostileAuthorNameFromGit(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "chore: base")
	runGit(t, "-c", "user.name=Evil\x1fName", "commit", "-q", "--allow-empty", "-m", "ident test")

	page, err := HistoryPage("main", 0, 10)
	if err != nil {
		t.Fatalf("HistoryPage: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("HistoryPage returned %d commits, want 2", len(page))
	}
	e := page[0]
	if e.Subject != "ident test" {
		t.Errorf("subject = %q, want %q", e.Subject, "ident test")
	}
	if e.Author != "EvilName" {
		t.Errorf("author = %q, want %q", e.Author, "EvilName")
	}
	for _, r := range e.Refs {
		if !strings.Contains(r, "main") {
			t.Errorf("refs = %v — something that is not a ref became a badge", e.Refs)
		}
	}
	if e.IsMerge {
		t.Error("a single-parent commit was reported as a merge")
	}

	detail, err := GetCommitDetail(e.SHA)
	if err != nil {
		t.Fatalf("GetCommitDetail: %v", err)
	}
	if detail.Author != "EvilName" || detail.Subject != "ident test" {
		t.Errorf("detail author/subject = %q / %q, want %q / %q",
			detail.Author, detail.Subject, "EvilName", "ident test")
	}
	if detail.Email != "test@example.invalid" {
		t.Errorf("detail email = %q — the fields shifted", detail.Email)
	}
}

func TestFlattenSubjectIsOneRow(t *testing.T) {
	if got := flattenSubject("first\nsecond\r\nthird"); got != "first second third" {
		t.Errorf("flattenSubject = %q, want a single line", got)
	}
}

// ── Pagination against a real repository ───────────────

// historyRepo builds n commits on the current branch, oldest first.
func historyRepo(t *testing.T, n int) {
	t.Helper()
	scratchRepo(t)
	for i := 0; i < n; i++ {
		write(t, "log.txt", fmt.Sprintf("line %d\n", i))
		commitAll(t, fmt.Sprintf("chore: commit %d", i))
	}
}

func subjects(entries []CommitEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Subject)
	}
	return out
}

func TestHistoryPagePaginatesAndTerminates(t *testing.T) {
	historyRepo(t, 25)

	first, err := HistoryPage("main", 0, 10)
	if err != nil {
		t.Fatalf("HistoryPage: %v", err)
	}
	if len(first) != 10 {
		t.Fatalf("first page has %d entries, want 10", len(first))
	}
	// Newest first: commit 24 is the tip.
	if first[0].Subject != "chore: commit 24" {
		t.Errorf("first row = %q, want the newest commit", first[0].Subject)
	}

	second, err := HistoryPage("main", 10, 10)
	if err != nil {
		t.Fatalf("HistoryPage(skip=10): %v", err)
	}
	if len(second) != 10 {
		t.Fatalf("second page has %d entries, want 10", len(second))
	}
	// The page boundary is exact: no commit is shown twice, none is skipped.
	if second[0].Subject != "chore: commit 14" {
		t.Errorf("second page starts at %q, want chore: commit 14", second[0].Subject)
	}
	seen := map[string]bool{}
	for _, s := range append(subjects(first), subjects(second)...) {
		if seen[s] {
			t.Errorf("%q appears on both pages", s)
		}
		seen[s] = true
	}

	// The last page is short — that, and nothing else, is how the browser
	// learns the history has ended.
	last, err := HistoryPage("main", 20, 10)
	if err != nil {
		t.Fatalf("HistoryPage(skip=20): %v", err)
	}
	if len(last) != 5 {
		t.Fatalf("last page has %d entries, want 5", len(last))
	}
	past, err := HistoryPage("main", 25, 10)
	if err != nil {
		t.Fatalf("HistoryPage(skip=25): %v", err)
	}
	if len(past) != 0 {
		t.Errorf("reading past the end returned %d entries", len(past))
	}
}

// The browser answers "what is the story of MY branch" — the dashboard graph
// already shows the cross-branch picture. A commit that only exists on another
// branch must not appear.
func TestHistoryPageFollowsOneBranchOnly(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "chore: base")
	runGit(t, "checkout", "-q", "-b", "feature")
	write(t, "b.txt", "b\n")
	commitAll(t, "feat: only on feature")
	runGit(t, "checkout", "-q", "main")
	write(t, "c.txt", "c\n")
	commitAll(t, "chore: only on main")

	got, err := HistoryPage("main", 0, 50)
	if err != nil {
		t.Fatalf("HistoryPage: %v", err)
	}
	for _, s := range subjects(got) {
		if s == "feat: only on feature" {
			t.Fatalf("main's history includes another branch's commit: %v", subjects(got))
		}
	}
	if len(got) != 2 {
		t.Fatalf("main has %d commits, want 2: %v", len(got), subjects(got))
	}
}

// Detached HEAD is where "where am I?" matters most, and DetachedLabel is a
// display string git would reject outright.
func TestHistoryPageOnDetachedHeadReadsHead(t *testing.T) {
	historyRepo(t, 3)
	runGit(t, "checkout", "-q", "--detach", "HEAD~1")

	got, err := HistoryPage(DetachedLabel, 0, 10)
	if err != nil {
		t.Fatalf("HistoryPage(DetachedLabel): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("detached HEAD~1 has %d commits, want 2", len(got))
	}
	if got[0].Subject != "chore: commit 1" {
		t.Errorf("first row = %q, want the detached tip", got[0].Subject)
	}
	empty, err := HistoryPage("", 0, 10)
	if err != nil || len(empty) != 2 {
		t.Errorf("an empty ref did not fall back to HEAD: %d entries, %v", len(empty), err)
	}
}

func TestHistoryPageRefusesAFlagShapedRef(t *testing.T) {
	historyRepo(t, 2)
	for _, ref := range []string{"--all", "-n1"} {
		if _, err := HistoryPage(ref, 0, 10); !errors.Is(err, ErrBadRevision) {
			t.Errorf("HistoryPage(%q) error = %v, want ErrBadRevision", ref, err)
		}
	}
}

// A branch whose name is also a file on disk is ambiguous to git without `--`.
func TestHistoryPageHandlesABranchNamedLikeAFile(t *testing.T) {
	scratchRepo(t)
	write(t, "release", "not a ref\n")
	commitAll(t, "chore: add a file called release")
	runGit(t, "branch", "release")

	got, err := HistoryPage("release", 0, 10)
	if err != nil {
		t.Fatalf("HistoryPage: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
}

// ── Detail ─────────────────────────────────────────────

func TestGetCommitDetailReadsTheWholeCommit(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "one\ntwo\n")
	runGit(t, "add", "-A")
	runGit(t, "commit", "-q", "-m", "feat: the subject", "-m", "The body.\n\nA second paragraph.")
	runGit(t, "tag", "v9.9.9")

	sha := strings.TrimSpace(runGit(t, "rev-parse", "--short", "HEAD"))
	d, err := GetCommitDetail(sha)
	if err != nil {
		t.Fatalf("GetCommitDetail: %v", err)
	}
	if d.Subject != "feat: the subject" {
		t.Errorf("subject = %q", d.Subject)
	}
	for _, want := range []string{"The body.", "A second paragraph."} {
		if !strings.Contains(d.Body, want) {
			t.Errorf("body is missing %q:\n%s", want, d.Body)
		}
	}
	if strings.HasPrefix(d.Body, "\n") {
		t.Errorf("body keeps the blank separator line: %q", d.Body)
	}
	if d.Author != "git-assist test" || d.Email != "test@example.invalid" {
		t.Errorf("author = %q <%q>", d.Author, d.Email)
	}
	if len(d.Date) != len("2026-07-26 14:03") {
		t.Errorf("date = %q, want an absolute YYYY-MM-DD HH:MM", d.Date)
	}
	if d.Age == "" {
		t.Error("no relative age")
	}
	if !strings.HasPrefix(d.FullSHA, d.SHA) || len(d.FullSHA) != 40 {
		t.Errorf("full sha %q does not extend short sha %q", d.FullSHA, d.SHA)
	}
	if !strings.Contains(d.Stat, "a.txt") || !strings.Contains(d.Stat, "changed") {
		t.Errorf("stat block does not describe the change:\n%s", d.Stat)
	}
	var tagged bool
	for _, r := range d.Refs {
		if r == "tag: v9.9.9" {
			tagged = true
		}
	}
	if !tagged {
		t.Errorf("refs = %v, want the tag", d.Refs)
	}
	if d.IsMerge {
		t.Error("an ordinary commit was reported as a merge")
	}
}

func TestGetCommitDetailFlagsAMerge(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "chore: base")
	runGit(t, "checkout", "-q", "-b", "feature")
	write(t, "b.txt", "b\n")
	commitAll(t, "feat: on feature")
	runGit(t, "checkout", "-q", "main")
	runGit(t, "merge", "-q", "--no-ff", "feature", "-m", "Merge branch 'feature'")

	sha := strings.TrimSpace(runGit(t, "rev-parse", "--short", "HEAD"))
	d, err := GetCommitDetail(sha)
	if err != nil {
		t.Fatalf("GetCommitDetail: %v", err)
	}
	if !d.IsMerge {
		t.Error("the merge commit was not flagged")
	}
}

func TestGetCommitDetailRejectsAnythingButASHA(t *testing.T) {
	historyRepo(t, 1)
	for _, bad := range []string{"", "HEAD", "HEAD~1", "--all", "main", "zzz1234"} {
		if _, err := GetCommitDetail(bad); !errors.Is(err, ErrBadRevision) {
			t.Errorf("GetCommitDetail(%q) error = %v, want ErrBadRevision", bad, err)
		}
	}
}

// ── Patch ──────────────────────────────────────────────

func TestCommitPatchReadsTheDiff(t *testing.T) {
	historyRepo(t, 2)
	sha := strings.TrimSpace(runGit(t, "rev-parse", "--short", "HEAD"))
	info, err := CommitPatch(sha)
	if err != nil {
		t.Fatalf("CommitPatch: %v", err)
	}
	if !strings.Contains(info.Patch, "diff --git") || !strings.Contains(info.Patch, "+line 1") {
		t.Errorf("patch does not contain the change:\n%s", info.Patch)
	}
	if info.Truncated {
		t.Error("a two-line patch was reported as truncated")
	}
}

// git show never emits raw binary — it says so in words, which is exactly what
// the pane should print. There is no binary refusal on this path.
func TestCommitPatchDescribesBinaryFilesInsteadOfFailing(t *testing.T) {
	scratchRepo(t)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "chore: seed")
	write(t, "bin.dat", "\x00\x01\x02binary")
	commitAll(t, "chore: add a binary")

	sha := strings.TrimSpace(runGit(t, "rev-parse", "--short", "HEAD"))
	info, err := CommitPatch(sha)
	if err != nil {
		t.Fatalf("CommitPatch: %v", err)
	}
	if !strings.Contains(info.Patch, "Binary files") {
		t.Errorf("patch does not name the binary file:\n%s", info.Patch)
	}
	if strings.ContainsRune(info.Patch, '\x00') {
		t.Error("raw binary reached the patch")
	}
}

func TestCommitPatchCapsHugeDiffsAndSaysSo(t *testing.T) {
	scratchRepo(t)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "chore: seed")

	var b strings.Builder
	for i := 0; b.Len() < 3*maxPreviewBytes/2; i++ {
		fmt.Fprintf(&b, "line %d of a generated file\n", i)
	}
	write(t, "big.txt", b.String())
	commitAll(t, "chore: add a generated file")

	sha := strings.TrimSpace(runGit(t, "rev-parse", "--short", "HEAD"))
	info, err := CommitPatch(sha)
	if err != nil {
		t.Fatalf("CommitPatch: %v", err)
	}
	if !info.Truncated {
		t.Fatal("an oversized patch was not reported as truncated")
	}
	if len(info.Patch) > maxPreviewBytes {
		t.Errorf("patch is %d bytes, past the %d cap", len(info.Patch), maxPreviewBytes)
	}
	if info.FullKB < maxPreviewBytes/1024 {
		t.Errorf("FullKB = %d, want the true size of the patch", info.FullKB)
	}
	// Cut on a line boundary: a byte-exact cut lands mid-rune and renders as
	// corruption rather than as a limit.
	if last := info.Patch[len(info.Patch)-1]; last == '\x00' {
		t.Error("the cut left a NUL at the end of the patch")
	}
	if !strings.HasSuffix(info.Patch, "generated file") {
		t.Errorf("the patch does not end on a whole line: %q", info.Patch[len(info.Patch)-40:])
	}
}

// A clean merge's default combined diff is empty. That is not an error, and it
// is not "no changes" either — the caller says which, because only it knows the
// commit was a merge.
func TestCommitPatchOnACleanMergeIsEmpty(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "a\n")
	commitAll(t, "chore: base")
	runGit(t, "checkout", "-q", "-b", "feature")
	write(t, "b.txt", "b\n")
	commitAll(t, "feat: on feature")
	runGit(t, "checkout", "-q", "main")
	runGit(t, "merge", "-q", "--no-ff", "feature", "-m", "Merge branch 'feature'")

	sha := strings.TrimSpace(runGit(t, "rev-parse", "--short", "HEAD"))
	info, err := CommitPatch(sha)
	if err != nil {
		t.Fatalf("CommitPatch on a merge: %v", err)
	}
	if strings.TrimSpace(info.Patch) != "" {
		t.Errorf("a clean merge produced a patch:\n%s", info.Patch)
	}
}

func TestCommitPatchRejectsAnythingButASHA(t *testing.T) {
	historyRepo(t, 1)
	for _, bad := range []string{"", "HEAD", "--stat", "main"} {
		if _, err := CommitPatch(bad); !errors.Is(err, ErrBadRevision) {
			t.Errorf("CommitPatch(%q) error = %v, want ErrBadRevision", bad, err)
		}
	}
}

// ── Counting ───────────────────────────────────────────

func TestTotalCommits(t *testing.T) {
	scratchRepo(t)
	if n := TotalCommits(); n != 0 {
		t.Errorf("a repository with no commits counted %d", n)
	}
	for i := 0; i < 3; i++ {
		write(t, "log.txt", fmt.Sprintf("%d\n", i))
		commitAll(t, fmt.Sprintf("chore: %d", i))
	}
	if n := TotalCommits(); n != 3 {
		t.Errorf("TotalCommits = %d, want 3", n)
	}
}
