# Roadmap

What's open, what's planned, and what's known-but-not-yet-fixed in git-assist.

For *shipped* work, see [CHANGELOG.md](CHANGELOG.md).

## Status

Currently at **v1.1.0**.

A full-codebase audit on 2026-05-13 surfaced 4 Critical bugs, 31 Major issues, and 27 Minor paper cuts. v1.0.1 shipped all 4 Critical fixes plus 4 high-impact Majors; v1.1.0 shipped 8 more Majors as a Tier-2 hardening pass. Everything below is what remains.

---

## Open Major issues (from audit)

These are real behavioral gaps, not just polish. Roughly grouped by area.

### Branch & merge
- **M5** — `git branch -d` on a branch with unmerged commits surfaces git's raw error, but no in-TUI option to force-delete with `-D`. User has to drop to terminal.
- **M6** — Remote-only branches are selectable as a merge **target**. Selecting one creates a local tracking branch as a side-effect, which is confusing relative to the user's mental model ("I'm merging into the remote").
- **M7** — `CreateBranch` accepts spaces / invalid characters with no client-side validation; git rejects with a technical error like `fatal: 'my branch' is not a valid pathspec`.

### Sync
- **M2** — Sync dialog (`step_sync.go`) doesn't fully block input during pull / sync-with-main. Same problem v1.1.0 fixed for the branch manager, just hasn't been applied here yet.
- **M28** — Fetch errors are intentionally swallowed (background op) but no "stale" indicator is shown when fetch fails. `m.aheadBehind` silently goes stale and the user can't tell.

### Files / commit wizard
- **M8** — Editor saves the **working-tree** version of a file while the diff viewer shows the **index/HEAD** diff. If a user has a partially-staged file, opening the editor and saving overwrites their unstaged work. Real design question: align them, warn, or document.
- **M10** — In gitignore mode, when a filter is active, `space` toggles the file at the unfiltered cursor index instead of the filtered match. Wrong file gets toggled.
- **M11** — Cursor is not reset when exiting gitignore mode. If the user navigated into the existing-ignored zone (high index) and then cancelled, subsequent commit-mode operations could OOB on `m.files[cursor]`.
- **M12** — `confirmExit` prompt in edit mode leaves the textarea unfocused after the user declines ("n"). Subsequent keystrokes go nowhere until they trigger another action.
- **M14** — Diff scroll math leaves the last visible-window of lines unreachable in long diffs (`maxScroll = len-1` then `< maxScroll - visible` cutoff).
- **M15** — Confirm screen hard-caps file list at 5 entries with "... and N more". No way to review a large commit before pressing enter.

### Config editor
- **M18** — `tab` scope-toggle while in inline edit mode commits the typed value to the **other** scope (Local → Global or vice versa).
- **M21** — `init.defaultBranch` picker only shows existing local branches. Can't pick a branch name that doesn't yet exist (git accepts arbitrary names).
- **M22** — `core.editor` isn't validated against `PATH`. Setting "nonexistent-editor" succeeds; user discovers the break only when they next try an interactive operation.
- **M23** — GPG signing toggle doesn't pre-check that a signing key is configured. Enabling it without a key makes every subsequent commit fail.

### Init flow
- **M19** — Gitignore template auto-detection is non-deterministic for multi-language repos (iterates a Go map). Repo with both `go.mod` and `package.json` picks a different template on different runs.
- **M20** — `gh auth login --web` can return success when the user cancelled in the browser. Next step (repo create) then fails with an auth error.

### General / git package
- **M29** — Push refspec accepts any branch string. Not shell-injection (no shell), but defensive validation would prevent weird inputs from reaching git.
- **M30** — Force push not offered with `--force-with-lease` safety. A rebased branch's plain push fails; the formatError hint says "Run `git pull` first" which is **wrong** for a rebase.
- **M31** — Help bar wraps on terminals narrower than ~80 chars.

---

## Minor paper cuts (from audit)

Lower-impact polish. Roughly: each is a one-function fix, none individually moves the needle, but bundled they raise the quality floor.

**State / cursor:**
- `menuCursor` resets to 0 on return-from-Done (inconsistent with other cursor preservation)
- `m.err` cleared on **any** keypress — rapid typists / held keys may miss critical errors
- `initSuccessMsg` often invisible because the same keypress that triggers a step transition also clears it
- `branchCreatedHint` cleared without distinguishing navigation from action
- `mergeAbort` called unconditionally (silent error if no merge in progress)
- `lastFetch` updated even on **failed** fetch — 30-second debounce then suppresses the retry

**Validation / edge cases:**
- `UndoLastCommit` on first commit returns raw `fatal: ambiguous argument 'HEAD~1'` — should say "nothing to undo"
- `RemoveFromGitignore` silently no-ops on entries that don't exist
- `.gitignore` dedup ignores trailing-whitespace differences but doesn't normalize
- Gitignore paths not normalized (`./file` vs `file` stored differently)
- `AddToGitignore`'s `0644` overwrites stricter perms on existing `.gitignore`
- `parseLine` doesn't handle octopus merges (3+ parents)
- `WriteFileContent` doesn't normalize line endings — mixed `\r\n`/`\n` files round-trip with surprise diffs
- Binary files can be opened in edit mode (no pre-check)

**UX clarity:**
- Long file paths truncated without ellipsis
- Long diff lines hard-truncated, no horizontal scroll
- Diff scroll position not preserved across re-opens of the same file
- Subject 200-char limit is silent (no counter)
- Body field only appears after pressing `tab` (discoverability)
- Preview line in message step doesn't include body preview
- Breaking-change `!` not persistently visualized on the type-picker
- "Connect to GitHub" menu entry stale if a remote is added externally mid-session
- `cleanDecoration` strips tag refs entirely — tagged release commits look undecorated in the graph

---

## UX gaps (features not yet built)

These would substantially improve the tool but are not bugs.

- **`--amend`** flow for fixing the last commit without losing the message
- **Conflict resolution UI** — currently a conflict kicks the user to terminal; an in-TUI conflict-file picker + "mark resolved" + "abort" would close the loop
- **Branch rename** — common operation, not exposed
- **`--force-with-lease` push** — for rebased branches, with safety
- **Stash management** — list / apply / drop entries; right now stashes created by auto-stash become orphans the user manages from the terminal
- **Revert / cherry-pick** — useful for the target audience (git beginners)
- **`git-assist --help`** — there's no help output for the CLI itself
- **Detached-HEAD UI** — `GetCurrentBranch` returns `"HEAD (detached)"` but no special-case handling; most operations fail in confusing ways
- **Worktree awareness** — multiple worktrees of the same repo share `.git/config` but have independent indexes; no detection
- **Pattern syntax help in gitignore mode** — no hint about `*`, `!`, trailing `/`

---

## Infrastructure

- **Tests** — currently zero `_test.go` files. Start with `internal/git/` (pure functions, no TUI dependency).
- **CI** — GitHub Actions workflow for `build + vet + test` on push and PR.
- **Multi-platform builds** — `.goreleaser.yaml` for Linux / Windows / macOS binaries on tag.
- **Issue templates** — bug report + feature request templates if the repo grows beyond solo work.

---

## Deferred / rejected

- **Horizontal commit graph** — explored and rejected for v1. The vertical layout matches every comparable TUI tool (lazygit, gitui, tig) for good reasons rooted in terminal constraints (character-cell rendering, width as the scarce axis, monospace text, no leader lines). The v1.0 compaction (`feat: compact commit graph for menu`) addressed the underlying "too sparse" complaint without fighting the medium.

---

## Recently shipped (for context)

| Date | Release | Headline |
|------|---------|----------|
| 2026-05-13 | v1.1.0 | Tier-2 hardening: cached branch count, async input lock, unified conflict recovery, git package error handling, `Done` step keys |
| 2026-05-13 | v1.0.1 | All 4 Critical bug fixes from the audit + 4 high-impact Majors (stash hardening, commit reset guard, main protection, init validation) |
| 2026-05-13 | v1.0.0 | First public release |
| 2026-05-13 | (post-release) | README rewrite + CHANGELOG.md added |
