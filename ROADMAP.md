# Roadmap

What's open, what's ruled out, and what's known-but-not-yet-fixed in git-assist.

For *shipped* work, see [CHANGELOG.md](CHANGELOG.md).

## Status

Currently at **v1.3.0**.

Two audits so far. The first (2026-05-13) found 4 Critical, 31 Major and 27 Minor issues, and v1.0.1 / v1.1.0 answered the top of that list. The second, a full-codebase audit on 2026-07-26, confirmed around a hundred distinct defects; v1.3.0 shipped roughly ninety fixes and ten features across thirteen commits.

Of the Majors that were still open at the start of the second audit, these are closed: `M2`, `M5`, `M6`, `M7`, `M8`, `M10`, `M11`, `M12`, `M14`, `M15`, `M18`, `M19`, `M20`, `M28`, `M29`, `M30` — along with most of the minor paper cuts. Four of those needed no code, only a re-read: `M8` (the diff pane runs `git diff HEAD`, which is the working-tree state the editor loads and saves — they cannot disagree), `M10` (gitignore mode has no filter to desynchronise), `M12` (nothing blurs the editor's textarea) and `M19` (detection walks a sorted `os.ReadDir` and looks names up in the map, rather than ranging over it).

What follows is what is *not* closed.

---

## Open

### Behaviour

- **M21** — the `init.defaultBranch` picker offers existing local branches only. Git accepts any name; a brand-new one can't be typed here.
- The history-rewrite pin (`rewriteBaseByBranch`) is memory-only. Quit and relaunch after undoing a pushed commit and the new session doesn't know the branch diverged deliberately — its sync dialog offers the plain pull that restores the undone commit. The conflict-stash association survives a relaunch via `.git/git-assist-conflict-stash`; the rewrite pin deserves the same treatment.
- **M22** — `core.editor` isn't checked against `PATH`. Setting `nonexistent-editor` succeeds, and the break surfaces the next time git wants an editor.
- **M23** — the GPG signing toggle doesn't check that a signing key is configured. Enabling it without one makes every subsequent commit fail.
- **M31** — the help bar still wraps below roughly 80 columns. `footerHeight()` now *counts* the wrapped rows, so nothing else is pushed off the screen, but a two-line bar on a narrow terminal is still worse than a shorter list would be.
- **The dashboard doesn't scroll.** Content is trimmed from the bottom (`clampLines`), so on a terminal too short for the whole menu the last entries and the help bar are cut with no way to reach them. The file selector, stash manager, history browser and conflict resolver all have real scroll windows; the menu never got one because its list was three items long when it was written.
- **No horizontal scroll in the diff viewer.** Lines wider than the box are truncated.
- **The 200-character subject limit is silent** — the input simply stops accepting characters. Custom commit types got a counter; the subject didn't.
- **The message step's live preview omits the body.** It shows `type(scope): subject` only, so a body typo isn't visible until the confirm screen.
- **No pattern-syntax hint in gitignore mode** — nothing on screen explains `*`, `!` or a trailing `/`.
- **`RemoveFromGitignore` silently no-ops** on entries that aren't in the file.
- **The editor doesn't normalise line endings.** A file with mixed `\r\n` / `\n` round-trips through the textarea and back to disk verbatim, which can produce a surprise diff.
- **Template auto-detection picks the alphabetically first marker file**, not a considered priority — `Cargo.toml` beats `go.mod` in a mixed repo. Deterministic, and the template picker lets you override it, so this is a nit rather than a bug.

### Not supported

- **Multi-remote repositories.** Every remote path in the codebase is `origin`: push, fetch, ahead/behind, the config editor's Remote URL, the init flow. A repo with `upstream` alongside `origin` is read as if only `origin` existed. Supporting it properly means a remote picker on the push and sync paths, not a rename.
- **Worktree awareness.** Multiple worktrees of one repository share `.git/config` but have independent indexes and HEADs; nothing detects that. `GetBranches` parses the `+` prefix git puts on a branch checked out elsewhere, which is as far as it goes.
- **Windows.** The code shells out to `git` and uses forward-slash paths as git reports them, which should be portable, but nothing has been run there and no test covers it. Treat the Windows binary as unverified.

---

## Ruled out

Decisions, not backlog. Listed so they don't get re-proposed.

- **Rebase — permanently out of scope.** An interactive rebase is a multi-commit, editor-driven operation that stops on a conflict per commit, and its safety net is the reflog — precisely what a beginner doesn't know to reach for. Wrapping it means either building a second, worse todo-list editor inside the TUI, or shelling out to the user's editor, which is the trip to the terminal this tool exists to remove. Everything else here is shaped so a wrong keypress costs one file or one commit; rebase is the operation where it costs the branch. The same ground is covered from the other side: `--no-ff` merges for integrating, revert for undoing pushed work, and amend plus a force-with-lease that is leased against the exact commit it means to replace.
- **Cherry-pick.** Less categorical — it's a single, revertible operation — but it needs a commit picker and its own conflict path, and the history browser is deliberately read-only. Revisit if it's ever actually asked for.
- **Write actions in the history browser.** No checkout, no revert-from-here, no reset. The screen's value is that every key on it is safe, and a destructive key on a browsing screen would need the destructive-prompt treatment that the rest of the app reserves for screens the user went to *in order* to do something.
- **Auto-aborting conflicts.** Removed in v1.3.0 and not coming back. Aborting a merge the user asked for, on their behalf, to keep the app's state simple was the app's problem being solved with the user's work.
- **Horizontal commit graph** — explored and rejected for v1. The vertical layout matches every comparable TUI tool (lazygit, gitui, tig) for good reasons rooted in terminal constraints (character-cell rendering, width as the scarce axis, monospace text, no leader lines). The v1.0 compaction (`feat: compact commit graph for menu`) addressed the underlying "too sparse" complaint without fighting the medium.

---

## Infrastructure

Built in v1.3.0:

- **Tests** — over three hundred, across `internal/git` and `internal/ui`, all hermetic: anything touching git builds its own scratch repository. Includes a parity test binding the `?` overlay to every screen's footer, and a test that pins `GIT_EDITOR=false` to prove finishing a merge never spawns one.
- **CI** — GitHub Actions: build, vet and test on push and pull request.
- **Multi-platform builds** — `.goreleaser.yaml` producing Linux / macOS / Windows binaries on tag.

Still open:

- **Issue templates** — bug report + feature request, if the repo grows beyond solo work.
- **A race-detector job.** The suite passes under `-race` locally; CI doesn't run it yet.
- **No end-to-end pty suite in CI.** The pty runs that verified the v1.3.0 features (stash round-trips, amend→force, external merges, the slow-git benchmark) live in scratch directories and were run by hand.

---

## Recently shipped (for context)

| Date | Release | Headline |
|------|---------|----------|
| 2026-07-27 | v1.3.0 | Second audit answered: conflict resolver, stash manager, history browser, push loop closed, force-with-lease, discard / revert / rename, `?` overlay, detached-HEAD guard, async dashboard, 300+ tests |
| 2026-05-13 | v1.2.0 | Amend the last commit through the wizard |
| 2026-05-13 | v1.1.0 | Tier-2 hardening: cached branch count, async input lock, unified conflict recovery, git package error handling, `Done` step keys |
| 2026-05-13 | v1.0.1 | All 4 Critical bug fixes from the first audit + 4 high-impact Majors (stash hardening, commit reset guard, main protection, init validation) |
| 2026-05-13 | v1.0.0 | First public release |
