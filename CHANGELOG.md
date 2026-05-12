# Changelog

All notable changes to **git-assist** are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

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

[Unreleased]: https://github.com/y4hyya/Git-Assistant/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/y4hyya/Git-Assistant/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/y4hyya/Git-Assistant/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/y4hyya/Git-Assistant/releases/tag/v1.0.0
