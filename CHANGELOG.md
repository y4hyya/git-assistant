# Changelog

All notable changes to **git-assist** are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.3.0] — 2026-07-27

A full-codebase audit on 2026-07-26 confirmed around a hundred distinct defects, from
filenames the file selector could not see to a force push that could delete a
colleague's commit. This release is the answer to it: roughly ninety fixes, and ten
features that close the loops the tool had been leaving open — every place where
git-assist could *start* something it could not *finish*, and then told the user to
open a terminal. Merges that conflict, stashes it created itself, commits it had no
way to show, and pushes it could only describe are all handled in-app now.

### Added

- **Conflict resolver** — a conflicting merge, pull or sync stops on a resolution
  screen instead of being aborted. Per-file `o` (keep yours) / `t` (take theirs),
  routed by git's unmerged index code (`UU / AA / UD / DU / DD / AU / UA`) so that
  keeping the side that *deleted* a file runs `git rm` and says so; `e` edits the file with the markers and a legend; `m`
  marks it resolved, warning once if markers remain; `c` finishes with
  `git commit --no-edit` (never an editor on top of the TUI); `a` aborts. The
  pending auto-stash is parked for the whole resolution — `git stash pop` fails
  against an unmerged index — and restored at the end. An unfinished merge, whether
  left by a quit or started in another terminal, is picked up at the next launch.
- **Stash manager** — list, patch preview (untracked files included), apply, pop and
  drop, on the menu and on `S` whenever the stack is non-empty. Entries are addressed
  by their stable SHA, re-resolved before every operation, because `stash@{N}` is
  positional and a drop renumbers everything below it.
- **History browser** — the current branch's commits, paged 200 at a time and
  extended as you scroll, with a detail pane (SHAs, author, dates, refs, full
  message, `--stat`) and a capped patch view. Read-only by design.
- **Push from anywhere** — a `Push` menu entry whenever origin is missing something,
  and a push screen that shows the outgoing commits before sending them. Previously
  the push step was reachable only in the seconds after a commit.
- **Force-with-lease push** — offered as its own key (`f`) after an amend or undo of a
  commit origin already had, with the lease pinned to the pre-rewrite SHA.
- **Discard a file's changes** (`x`) — routed by status, with a confirmation that
  states exactly what is lost. On a deleted file the key relabels itself `restore`.
- **Revert** — `r` on the undo prompt when the commit is already pushed: a new commit
  rather than a rewrite. A conflicting revert aborts itself and says nothing changed.
- **Branch rename** (`r`) — validated with `git check-ref-format`, and honest that
  `git branch -m` is local only when the branch has an upstream.
- **`?` key overlay and `git-assist --help`** — the overlay lists exactly the keys the
  current screen responds to, from the same table its footer renders.
- **Detached-HEAD guard** — a warning banner, and every branch-assuming action hidden
  until you switch to a branch. The branch manager, stash manager and history browser
  stay reachable, being the ways out.

### Fixed

**Crashes and data safety**

- Filenames containing spaces, quotes or non-ASCII characters were invisible to the
  file selector. Status is now read as `--porcelain=v1 -z --untracked-files=all`, and
  untracked directories expand to their files.
- Renames were committed as half a rename: only the new path was staged, so the old
  path's deletion stayed behind. Both halves now go in the same commit.
- A commit whose staging partly failed committed the rest in silence. Staging is
  all-or-nothing and names the path that failed.
- Amend integrity: abandoning an amend latched `amendMode` and leaked the old message
  into the next ordinary commit; non-conventional subjects were truncated at the
  wizard's 200/500-character limits. Raw subjects now round-trip verbatim, and one
  `resetWizard()` clears every field on every exit.
- Panics on empty lists (file selector, gitignore mode, and the branch, push and
  default-branch pickers) are guarded; leaving gitignore mode clamps the cursor.
- The editor refuses binary files.
- Undo and gitignore-apply are single-shot, so a held key cannot dispatch them twice.
  `ctrl+c` during a mutating operation warns before the second press quits.
- Every merge path auto-stashes a dirty tree and restores it afterwards, conflict
  aborts included; a failed pop surfaces its SHA.
- Launched from a subdirectory, the app operated on paths git reports relative to the
  worktree root — it now moves to the root first.

**Correctness**

- `GetAheadBehind` reported "up to date" for a branch with no upstream at all.
- "Behind main" measured against a local `main` that a fetch had left behind.
- A failed background fetch still stamped `lastFetch`, so the 30-second debounce
  suppressed the retry.
- `.gitignore` entries are anchored to the repository root, and dedup normalises
  `./x`, `/x` and `x/` to the same entry.
- `UndoLastCommit` on a repository's first commit answered with git's raw
  `fatal: ambiguous argument 'HEAD~1'`; it now says there is nothing to undo.
- Merge targets are local branches only — selecting a remote-only branch used to
  create a tracking branch as a side effect. New branch names are validated with
  `git check-ref-format`.
- Branch listings parse worktree-marked entries and ignore non-`origin` remotes.
- Config: clearing a field unsets the key instead of writing an empty string over the
  global value; `commit.gpgsign` recognises git's boolean synonyms; `tab` during an
  edit cancels it instead of writing the typed value into the other scope.
- A subject like `fix: things (see #12)` was parsed as a scope; a conventional type
  must now be a single word, and anything else takes the raw path.
- A conflict listing that failed to run read as "all files resolved".
- `gh auth login --web` exits 0 when the browser flow is abandoned; the init flow
  re-checks authentication before continuing.

**UX**

- The last screenful of a long diff was unreachable; scroll position is now remembered
  per file, and a filter jump keeps the cursor on screen.
- The confirm screen's hard cap of five files became a height-aware scrolling window.
- The commit body is cursor-navigable; custom types are validated as a single
  lowercase word with a visible counter.
- Merges, pulls, syncs, branch deletes, `.gitignore` edits and stashed switches used
  to finish in complete silence. They all leave a one-line status note now, including
  what the auto-stash did.
- The Done screen reported failed pushes as successes and drew steps that never ran.
- A push rejected after an amend was answered with "run git pull first" — which merges
  the pre-rewrite commit straight back in. The hint is amend-aware.
- A diverged branch is warned about rather than offered a naive pull, and
  "Already up to date" is reported as such instead of as a merge that happened.
- Errors carrying recovery instructions (a stash SHA, a command) survive keypresses
  until dismissed.
- Merging into the current branch was impossible from the branch manager; `m` on the
  current branch now opens the target picker.
- Deleting a branch with unmerged commits offers a force-delete confirmation instead
  of printing git's refusal.
- Network operations time out after 60 seconds and cancel on force-quit.

**Rendering**

- A commit subject containing brackets could masquerade as a branch decoration. The
  graph is parsed structurally now, on a unit separator.
- Tags survived to the graph (in amber) instead of being stripped; `origin/HEAD` is
  dropped. Octopus merges are drawn rather than passed through raw.
- All truncation is width-aware, so multi-byte text is never cut mid-character.
- The box's bottom border is guaranteed at every terminal height — the graph yields
  its rows first — and word-wrapped text can no longer overflow the height budget.
- The palette moved to adaptive colours, readable on light terminals.
- An unset locale now defaults to Unicode symbols rather than the ASCII fallback;
  an explicitly non-UTF-8 locale and `TERM=dumb` still get ASCII.
- Resizing the terminal resizes the file editor and the setup inputs.

**Performance**

- Dashboard reads moved off the UI thread into one coalesced snapshot message.
  Menu redraw on a slow repository: 2.90s → 0.02s, measured through a pty.
- The confirm screen forked `git` about ten times a second from inside `View`; no
  view in the wizard forks git any more.
- History is paged, so opening the browser on a large repository does not read it.

### Changed

- **Push semantics.** Push always pushes the current branch to `origin/<branch>`. The
  branch picker and the `HEAD:<target>` refspec it produced are gone — that refspec
  could put the current commits onto an unrelated branch's remote ref with nothing on
  screen saying so. `-u` is passed only on the first publish.
- **Conflicts are no longer auto-aborted.** v1.1.0 aborted a conflicting merge and
  asked the user to resolve it in a terminal. The abort is now one key on the resolver.
- **"Behind main" is measured against `origin/<main>`** when it exists, for the badge,
  the `s` shortcut and the ref that gets merged alike.
- **Unicode is the default** when the locale is unset, rather than the ASCII fallback.
- Every "run `git stash apply <ref>` in your terminal" message was replaced by "press
  `S` to open the stash manager". A test asserts the command does not come back.

### Infrastructure

- Tests, from none to over three hundred, across `internal/git` and `internal/ui`. Every test
  that touches git builds its own scratch repository. Includes a parity test binding
  the `?` overlay to the footers on every screen state, and a test that pins
  `GIT_EDITOR=false` to prove the merge is finished without spawning an editor.
- GitHub Actions workflow: build, vet and test on push and pull request.
- `.goreleaser.yaml` for Linux / macOS / Windows binaries on tag.
- `gofmt` clean baseline across the repository.

## [1.2.0] — 2026-05-13

First UX-gap feature from the roadmap: editing the last commit without losing its message.

### Added
- **Amend last commit** — new entry on the main menu. Pre-loads the commit wizard with the previous commit's scope, subject, body, and type so the user only edits what's actually changing. Optionally stages additional files in the Files step (empty selection is fine for message-only amends). The final step runs `git commit --amend`. Hidden when the repo has no commits yet.
- Parser for conventional-commit subjects (`type(scope)!: rest`) so the type / scope / breaking-change flag survive the round-trip. Non-conventional commits fall through to the "custom" type with the original subject preserved verbatim.
- Confirm step shows "Amend `<short-sha>`" and warns when the commit is already on a remote that the next push will need `git push --force-with-lease`.
- `git.Amend()`, `git.IsLastCommitPushed()`, `git.GetLastCommitFull()` in the git package.

### Changed
- After an amend, the wizard routes directly to Done instead of through the push step. Auto-pushing after an amend would either fail (non-FF on already-pushed commits) or surprise the user; the Confirm step warning sets up the manual `--force-with-lease` push expectation.
- `hasAnyCommit` is now cached in `RefreshGraphs` alongside `branchCount`, so menu rendering doesn't fork `git rev-parse` per keypress.

## [1.1.0] — 2026-05-13

Tier-2 hardening bundle from the post-v1.0 audit.

### Changed
- Menu reads a cached branch count refreshed alongside the commit graph. `git branch` no longer forks on every keypress.
- Branch manager fully blocks keyboard input while a switch or merge is running. Spinner animation continues; `ctrl+c` still quits.
- Merge conflicts encountered through the branch manager auto-abort with a clear error showing the conflict count. Previously the repo could be left in a half-merged state.
- Done step accepts `esc` (return to menu) and `q` (quit) in addition to `enter`.

### Fixed
- `HasUncommittedChanges()` propagates git errors instead of swallowing them. Branch switches and pulls abort on unknown working-tree state instead of proceeding as if clean.
- `GetConfigValue()` distinguishes "key not set" from "value is empty string" from "git command failed". The config editor now shows "not set" only when a key is actually unset.
- `GetBehindMain()` falls back to `origin/main` and `origin/master` when local main/master don't exist. Fresh clones now see the "behind main" indicator.
- When a stash-pop conflict cancels a pending target-picker merge, the error message names the cancelled merge so the user knows to re-initiate after resolving.

## [1.0.1] — 2026-05-13

Post-release hardening from a full-codebase audit that identified 4 Critical bugs, 31 Major findings, and 27 Minor paper cuts. v1.0.1 ships all 4 Criticals and the highest-impact Majors.

### Fixed
- **Critical:** `git stash` now passes `--include-untracked`. Untracked files were being left behind on branch switches and could be overwritten by `git checkout`.
- **Critical:** `Commit()` only runs `git reset` when the repo has at least one commit, and propagates the error otherwise. A previously-silent reset failure could let pre-existing staged content slip into the commit alongside the user's selection.
- **Critical:** `main` and `master` are now guarded from deletion in the branch manager.
- **Critical:** `StashChanges()` returns the new stash's short SHA. Every caller surfaces it in the error message when the subsequent pop fails — users get `git stash apply <sha>` instead of guessing which `stash@{N}` is theirs.
- Remote-only branches used as merge sources are passed as `origin/<name>` instead of bare `<name>`, preventing accidental merges of unrelated local branches sharing the name.
- Empty commit subjects now show an inline "subject cannot be empty" error instead of silently swallowing the keypress.
- Gitignore mode warns when the add-set includes tracked files (which trigger `git rm --cached`).
- Init flow validates remote URLs and GitHub repo names upfront with clear inline errors, instead of failing several seconds later inside `git remote add` or `gh repo create`.

## [1.0.0] — 2026-05-13

First public release.

### Added
- Always-on TUI git dashboard built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).
- Commit workflow — file selector → conventional-commit type picker → message with optional inline scope and body → confirmation → push → done → menu.
- File selector features — fuzzy filter (`/`), inline diff preview (`d`), in-place file editor (`e` from diff), gitignore mode (`g`), undo last commit (`u`).
- Branch manager — switch, create, delete, and merge with `--no-ff` so the fork/merge diamond is always visible in the graph. Merge target picker with direction arrow ("merge X into Y").
- Auto-stash + restore around branch switches.
- Remote sync — `p` to pull current, `s` to merge `origin/main` into current. Sync dialog auto-shows on startup when the current branch is behind origin or main.
- First-run init flow — launching in a non-git directory offers four paths (local init, connect existing remote, `gh repo create`, cancel). Language-aware `.gitignore` templates (Go / Node / Python / Rust / Generic). `gh auth login --web` integration.
- "Connect to GitHub" recovery menu entry when an existing repo has no origin.
- Config editor — view and edit `user.name`, `user.email`, `init.defaultBranch`, the origin URL, `commit.gpgsign`, and `core.editor`. Local / Global scope toggle. Branch picker for `init.defaultBranch`.
- Unified commit graph on the main menu showing all branches with decorations.
- "N behind main" warning on the dashboard.
- `--version` / `-v` flag with build-time version injection via `-ldflags`.
- `--no-color` flag (also respects the `NO_COLOR` env var).
- `git-assist branch` subcommand for jumping straight into the branch manager.
- macOS-conditional codesigning on `make install`.
- Unicode symbols with automatic ASCII fallback for terminals that don't support them.

[Unreleased]: https://github.com/y4hyya/Git-Assistant/compare/v1.3.0...HEAD
[1.3.0]: https://github.com/y4hyya/Git-Assistant/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/y4hyya/Git-Assistant/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/y4hyya/Git-Assistant/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/y4hyya/Git-Assistant/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/y4hyya/Git-Assistant/releases/tag/v1.0.0
