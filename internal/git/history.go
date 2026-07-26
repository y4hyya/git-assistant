package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ── Commit history ──────────────────────────────────────
//
// The only history this app used to show was the dashboard's ten-line graph of
// bare subjects: no SHA, no author, no date, and no way to see what any of
// those commits actually changed. "What did I do yesterday?" and "what exactly
// did that commit touch?" both ended in another terminal. This file is the read
// side that answers them.
//
// Everything here is READ-ONLY by design. There is no checkout, no revert, no
// cherry-pick: the browser is a place to look, and a wrong key on a history
// screen rewrites work that is already safe.

// CommitEntry is one row of the history list. Deliberately flat and
// pre-formatted — the row is rendered on every frame and must not need a second
// git process to say when a commit happened or whether it was a merge.
type CommitEntry struct {
	SHA     string   // abbreviated hash (%h) — what every other screen names a commit by
	Subject string   // first line of the message, flattened to one row
	Author  string   // author NAME, not the committer: who wrote it
	Age     string   // "2h ago", compacted from %cr
	Refs    []string // decorations as git spells them: "HEAD -> main", "tag: v1.2.0"
	IsMerge bool     // more than one parent
}

// CommitDetail is one commit in full: everything the detail pane shows.
type CommitDetail struct {
	SHA     string // abbreviated, for headings
	FullSHA string // the whole object name, which is what you paste into git
	Subject string // first line of %B
	Body    string // the rest of %B, verbatim (blank line separator dropped)
	Author  string
	Email   string
	Date    string   // absolute, "2026-07-26 14:03"
	Age     string   // relative, "2h ago"
	Refs    []string // as CommitEntry.Refs
	IsMerge bool
	Stat    string // `git show --stat` block: one line per file plus the summary
}

// CommitPatchInfo is one commit's patch plus what had to be left out of it.
// The cap is not optional: a generated-file commit can carry tens of megabytes
// of diff, and the rendered copy costs several times that again.
type CommitPatchInfo struct {
	Patch string
	// Truncated says the cap bit, so the pane can admit it rather than
	// silently presenting half a patch as the whole one. FullKB is the true
	// size, which is the only number that makes the admission useful.
	Truncated bool
	FullKB    int
}

// ErrBadRevision marks an argument that must not be handed to git: a display
// string like DetachedLabel, or anything that would arrive as a flag.
var ErrBadRevision = errors.New("not a usable revision")

// historyFieldSep separates the fields of one record — the same discipline the
// stash list uses (see stashListFormat). A control character cannot occur in a
// ref name or in an ident line, git strips them from the subject, and the one
// genuinely free-text field is LAST so that even a message carrying a separator
// cannot shift the parse.
const historyFieldSep = "\x1f"

const (
	historyListFormat = "%h" + historyFieldSep + // abbreviated hash
		"%cr" + historyFieldSep + // relative date
		"%an" + historyFieldSep + // author name
		"%p" + historyFieldSep + // parents — two or more means a merge
		"%D" + historyFieldSep + // decorations, bare (no parens, unlike %d)
		"%s" // subject, LAST
	historyListFields = 6

	historyDetailFormat = "%h" + historyFieldSep +
		"%H" + historyFieldSep +
		"%an" + historyFieldSep +
		"%ae" + historyFieldSep +
		"%ad" + historyFieldSep +
		"%cr" + historyFieldSep +
		"%p" + historyFieldSep +
		"%D" + historyFieldSep +
		"%B" // the whole message, LAST — the one field that may contain anything
	historyDetailFields = 9

	// historyDateFormat is the absolute date the detail pane shows. Explicit
	// rather than --date=default, which is locale- and config-dependent: the
	// pane puts it next to the relative age, and the two have to agree.
	historyDateFormat = "format:%Y-%m-%d %H:%M"
)

// HistoryPage returns one page of `ref`'s history, newest first.
//
// Paged on purpose. A repository with fifty thousand commits is not unusual and
// reading all of them to draw twenty rows would freeze the UI for seconds at the
// exact moment the user asked to look at something. The browser fetches a page,
// draws it, and asks for the next one only when the cursor approaches the end.
//
// skip is the number of commits already held, limit the page size. A page
// shorter than limit means the history ended there — callers stop asking.
func HistoryPage(ref string, skip, limit int) ([]CommitEntry, error) {
	rev, err := revArg(ref)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, nil
	}
	args := []string{"log", "-z", "--format=" + historyListFormat,
		fmt.Sprintf("-%d", limit)}
	if skip > 0 {
		args = append(args, fmt.Sprintf("--skip=%d", skip))
	}
	// `--` closes the revision list: without it a branch that shares its name
	// with a file on disk is ambiguous and git refuses outright.
	args = append(args, rev, "--")
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s: %w", rev, err)
	}
	return parseHistoryPage(string(out)), nil
}

// parseHistoryPage turns the NUL-terminated records of historyListFormat into
// entries. Split out from HistoryPage so the parse can be exercised against the
// messages git actually allows — separators, braces, non-ASCII, empty subjects.
func parseHistoryPage(data string) []CommitEntry {
	var entries []CommitEntry
	for _, rec := range strings.Split(data, "\x00") {
		if rec == "" {
			continue
		}
		// The free-text field is last, so SplitN's final element absorbs
		// anything the subject contains — including another separator.
		fields := strings.SplitN(rec, historyFieldSep, historyListFields)
		if len(fields) < historyListFields {
			continue
		}
		sha := strings.TrimSpace(fields[0])
		if sha == "" {
			continue
		}
		entries = append(entries, CommitEntry{
			SHA:     sha,
			Age:     compactAge(strings.TrimSpace(fields[1])),
			Author:  strings.TrimSpace(fields[2]),
			IsMerge: len(strings.Fields(fields[3])) > 1,
			Refs:    parseRefNames(fields[4]),
			Subject: flattenSubject(fields[5]),
		})
	}
	return entries
}

// GetCommitDetail reads one commit in full. Two git processes — the header and
// the stat block cannot come out of the same invocation — both run on a command
// goroutine, never in a View.
func GetCommitDetail(sha string) (CommitDetail, error) {
	if !isCommitSHA(sha) {
		return CommitDetail{}, fmt.Errorf("%w: %q", ErrBadRevision, sha)
	}
	out, err := exec.Command("git", "log", "-1", "-z",
		"--format="+historyDetailFormat, "--date="+historyDateFormat, sha, "--").Output()
	if err != nil {
		return CommitDetail{}, fmt.Errorf("git log %s: %w", sha, err)
	}
	d, ok := parseCommitDetail(string(out))
	if !ok {
		return CommitDetail{}, fmt.Errorf("could not read commit %s", sha)
	}
	d.Stat = commitStat(sha)
	return d, nil
}

// parseCommitDetail turns one historyDetailFormat record into a CommitDetail.
func parseCommitDetail(data string) (CommitDetail, bool) {
	rec := strings.TrimSuffix(data, "\x00")
	fields := strings.SplitN(rec, historyFieldSep, historyDetailFields)
	if len(fields) < historyDetailFields {
		return CommitDetail{}, false
	}
	sha := strings.TrimSpace(fields[0])
	if sha == "" {
		return CommitDetail{}, false
	}
	// %B is the raw message: subject, blank line, body. Cut at the first
	// newline rather than reformatting — this pane is the one place the commit
	// message is shown exactly as it was written.
	msg := strings.TrimRight(stripCR(fields[8]), "\n")
	subject, body, _ := strings.Cut(msg, "\n")
	return CommitDetail{
		SHA:     sha,
		FullSHA: strings.TrimSpace(fields[1]),
		Author:  strings.TrimSpace(fields[2]),
		Email:   strings.TrimSpace(fields[3]),
		Date:    strings.TrimSpace(fields[4]),
		Age:     compactAge(strings.TrimSpace(fields[5])),
		IsMerge: len(strings.Fields(fields[6])) > 1,
		Refs:    parseRefNames(fields[7]),
		Subject: strings.TrimSpace(subject),
		Body:    strings.Trim(body, "\n"),
	}, true
}

// commitStat is the `git show --stat` block: one line per file plus git's own
// summary. Errors are swallowed to "" — a commit whose stat cannot be read is
// still worth showing the message and the author of.
func commitStat(sha string) string {
	out, err := exec.Command("git", "show", "--stat", "--format=", sha, "--").Output()
	if err != nil {
		return ""
	}
	return strings.Trim(stripCR(string(out)), "\n")
}

// CommitPatch returns the full patch for one commit.
//
// `git show -p` never emits raw binary: a changed image comes through as
// "Binary files a/x and b/x differ", which is exactly what the pane should say,
// so unlike the single-file diff viewer there is no binary refusal here.
//
// An empty patch is returned as an empty patch rather than a message. It has
// two meanings — an ordinary merge that resolved nothing by hand (git's default
// combined diff is empty there) and a genuinely empty commit — and only the
// caller, which holds the CommitDetail, knows which one to say.
func CommitPatch(sha string) (CommitPatchInfo, error) {
	if !isCommitSHA(sha) {
		return CommitPatchInfo{}, fmt.Errorf("%w: %q", ErrBadRevision, sha)
	}
	out, err := exec.Command("git", "show", "--format=", "-p", sha, "--").CombinedOutput()
	if err != nil {
		return CommitPatchInfo{}, fmt.Errorf("git show %s: %s", sha, strings.TrimSpace(string(out)))
	}
	info := CommitPatchInfo{FullKB: (len(out) + 1023) / 1024}
	raw := out
	if len(raw) > maxPreviewBytes {
		cut := raw[:maxPreviewBytes]
		// Back up to a line boundary: a byte-exact cut lands mid-rune as often
		// as not, and a replacement glyph at the end of a truncated patch reads
		// as corruption rather than as a limit.
		if i := bytes.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i]
		}
		raw = cut
		info.Truncated = true
	}
	// Same treatment the file diff gets: \r returns the terminal cursor to
	// column 0 and the box border overwrites the line.
	info.Patch = strings.TrimRight(stripCR(string(raw)), "\n")
	return info, nil
}

// TotalCommits is how many commits HEAD can reach — the number the dashboard's
// History entry shows, and the gate that hides it in a repository with none.
// Read with the rest of the snapshot so the menu never forks git per keypress.
func TotalCommits() int {
	out, err := exec.Command("git", "rev-list", "--count", "HEAD").Output()
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// ── Argument hygiene ────────────────────────────────────

// revArg turns a branch name from the UI into something safe to hand git.
//
// DetachedLabel is the case this exists for: it is a DISPLAY string, `git log
// "HEAD (detached)"` is a fatal, and in that state HEAD is exactly the right
// answer anyway — "where am I?" is the question a detached user has. An empty
// branch means the same thing. Anything that would arrive as a flag is refused
// rather than rewritten: a silent substitution would show one branch's history
// under another's name.
func revArg(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == DetachedLabel {
		return "HEAD", nil
	}
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("%w: %q", ErrBadRevision, ref)
	}
	return ref, nil
}

// isCommitSHA reports whether s is an object name and nothing else. The browser
// only ever addresses commits by the SHA it read out of a CommitEntry, so this
// is deliberately narrower than "a revision git would accept": no HEAD~3, no
// branch names, nothing that could be a flag or a pathspec.
func isCommitSHA(s string) bool {
	if len(s) < 4 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// ── Field cleanup ───────────────────────────────────────

// parseRefNames splits %D into the refs git listed. %D, unlike %d, comes
// without the surrounding parentheses and is empty when a commit carries no
// decoration at all. Ref names cannot contain a space, so ", " is an
// unambiguous separator.
func parseRefNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var refs []string
	for _, p := range strings.Split(raw, ", ") {
		if p = strings.TrimSpace(p); p != "" {
			refs = append(refs, p)
		}
	}
	return refs
}

// flattenSubject makes one list row out of a subject. git collapses the first
// paragraph into a single line for %s, but a message written with a control
// character in it would still cost the list its alignment — one row, one line,
// no exceptions.
func flattenSubject(s string) string {
	s = stripCR(s)
	if strings.ContainsRune(s, '\n') {
		s = strings.ReplaceAll(s, "\n", " ")
	}
	return strings.TrimSpace(s)
}
