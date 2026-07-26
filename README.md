# git-assist

An interactive TUI git dashboard built with Go and [Bubble Tea](https://github.com/charmbracelet/bubbletea). Designed to make everyday git workflows obvious — branching, syncing, committing, and connecting a repo to GitHub — without trading away the underlying primitives.

```
╭──────────────────────────────────────────────────╮
│  git-assist   ⎇ main   clean                     │
│                                                  │
│  ▸ Commit    no changes                          │
│    Branch    3 branches                          │
│    History   214 commits                         │
│    Config    git settings                        │
│                                                  │
│   ↑↓ navigate    enter select    q quit          │
│                                                  │
│  ──────────────────────────────────────────────  │
│  Commit Graph                       main: clean  │
│  ●  feat: tier-2 hardening                       │
│  ●  fix: validate remote URL and repo name       │
│  ●  fix: harden stash flow                       │
│  ●  feat: compact commit graph                   │
╰──────────────────────────────────────────────────╯
```

## Features

### Commit workflow
- File selector with status indicators (`M / A / D / R / ?`), fuzzy filter (`/`), inline diff preview (`d`), in-place file editor (`e` from diff).
- Conventional commit types — `feat / fix / docs / refactor / style / test / chore / ci / perf / build`, plus custom types.
- Optional `scope` inline with the subject; optional body (toggle with `tab`); `!` toggle for breaking-change commits.
- Empty subjects are rejected with a clear inline error.
- Discard a file's changes (`x`) with a confirmation that states exactly what is lost — and that adapts to the file's status: modified files are restored from the last commit, untracked files are deleted, renames are undone, and a **deleted** file is brought back (the key relabels itself `restore`).
- Undo last commit (`u`) with a confirmation prompt. If the commit is already on origin the prompt offers both exits: `u` undoes anyway (rewrites history, force-push after) or `r` **reverts** instead — a new commit that undoes it, safe for pushed work. A conflicting revert aborts itself and reports that nothing changed.

### Branch manager
Reachable from the menu, from the file selector (`b`), or directly via `git-assist branch`.
- Switch, create, rename (`r`), and delete branches. `main` / `master` and the current branch are protected from deletion.
- Renaming discloses that `git branch -m` is local only: if the branch has an upstream, both the prompt and the result say origin still has the old name.
- Auto-stash and restore around branch switches and pulls — **includes untracked files**. If a stash-pop conflicts, the short SHA of your stash is surfaced and `S` opens the stash manager right there — no trip to another terminal.
- Merge with `--no-ff` so the fork/merge diamond is always visible in the graph.
- Merge target picker — choose which branch to merge **into** with a direction arrow; auto-switches to the target first if needed.
- Conflicting merges auto-abort with a clear file count, never leaving the repo half-merged.

### Stash manager
Appears on the menu (and answers `S`) only once there is something stashed — from the menu or from the branch manager, including on top of the "your changes are safe in stash …" banner an auto-stash failure raises.
- Every entry as `abc1234  2h ago  on feat — message`, newest first, scrollable.
- `enter` / `d` previews the full patch, **untracked files included**, with the same colouring and scrolling as the file diff viewer.
- `a` applies and keeps the entry, `p` pops (applies and removes it), `x` deletes after a confirmation that states the cost — stashed changes are in no commit, and git cannot bring them back.
- Results are reported in plain words: *"Applied stash abc1234 — 3 files restored to the working tree; the entry is still in the stash list."*
- A conflicted apply or pop is described truthfully: which files now hold conflict markers, that the stash itself was kept, and how to either resolve or cancel. Nothing is auto-aborted behind your back.
- Entries are always addressed by their stable SHA, re-resolved just before every operation — a `stash@{N}` read a moment ago can name a different entry after a drop.

### History browser
Appears on the menu as `History  N commits` once the repo has any. Answers the two questions the dashboard graph cannot: *what did I do yesterday*, and *what exactly did that commit touch*.
- Scrollable list of the **current branch's** commits — `abc1234  2h ago  subject`, with branch / tag decorations coloured exactly as they are in the graph, and merges marked in a word rather than a glyph.
- Loaded in pages of 200 and extended automatically as you scroll, so opening the browser on a 50,000-commit repository is as fast as on a fresh one. The counter is honest about it (`200 of 3,412 commits loaded`).
- `enter` / `d` opens the commit: short and full SHA, author and email, absolute date *and* relative age, refs, the full message (subject **and** body), and the `--stat` block.
- `p` shows the whole patch, diff-coloured and scrollable. Oversized patches are capped and say so, with the true size. A clean merge's empty patch is explained rather than shown as a blank pane.
- Read-only by design: no checkout, no revert-from-here, no cherry-pick. Nothing on this screen can change your repository.
- Works on a detached HEAD too — where the header names the commit you are sitting on.

### Remote sync
- Sync dialog auto-shows on startup when the current branch is behind origin or behind main.
- `p` to pull the current branch (fast-forward when possible).
- `s` to merge `origin/main` into the current branch (`--no-ff` for an explicit integration commit).
- Background fetch on startup and on return-to-menu, debounced to 30 seconds.

### First-run init flow
When launched in a non-git directory, git-assist offers four paths instead of erroring:
1. **Initialize local repo** — `git init` only.
2. **Connect to GitHub repo** — `git init` + add an existing remote URL.
3. **Create new GitHub repo** — `git init` + `gh repo create` (wires `origin` via `--source=.`).
4. **Cancel** — quit without changes.

Includes:
- Language-aware `.gitignore` templates (Go / Node / Python / Rust / Generic) auto-detected from marker files.
- `gh auth login --web` integration when the GitHub CLI isn't authenticated.
- Upfront URL and repo-name validation — bad inputs are caught before shelling out.

A "Connect to GitHub" recovery entry appears in the menu when an existing repo has no `origin` and the `gh` CLI is available.

### Config editor
- View and edit `user.name`, `user.email`, `init.defaultBranch`, `commit.gpgsign`, `core.editor`, and the origin URL.
- Toggle between local and global scope with `tab`.
- Branch picker for `init.defaultBranch` (pick from existing local branches, no free-text typos).
- "not set" hint distinguishes unconfigured keys from intentionally-empty values.

### Visual feedback
- Unified commit graph on the main menu — compact one-line-per-commit on linear history, with branch / fork / merge connectors for divergence points.
- Branch decorations colored in the graph.
- "N behind main" warning on the dashboard.
- Press `?` on any non-typing screen for an overlay listing exactly the keys that screen responds to — sourced from the same data the footers render, so it can never drift.
- Detached-HEAD guard: a warning banner on the dashboard, and everything that assumes a branch (commit, amend, push, sync) is hidden until you switch to one.
- Spinner animations during async ops; input is blocked during destructive operations.
- Unicode symbols with automatic ASCII fallback when the terminal doesn't support them.
- Respects `NO_COLOR=1` and the `--no-color` flag.

## Installation

Requires **Go 1.26+** and **Git**.

```bash
git clone https://github.com/y4hyya/Git-Assistant.git
cd Git-Assistant
make install
```

Installs to `~/.local/bin/git-assist`. On macOS the binary is codesigned so Gatekeeper accepts it. Make sure `~/.local/bin` is on your `PATH`:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## Usage

```bash
git-assist               # interactive dashboard
git-assist branch        # jump straight into the branch manager
git-assist --help        # usage (also -h, or `git-assist help`)
git-assist --version     # show installed version
git-assist --no-color    # disable color (also respects NO_COLOR=1)
```

Running `git-assist` outside of a git repository launches the first-run init flow instead of erroring.

## Keybindings

### Global
| Key | Action |
|-----|--------|
| `q` | Quit (from any top-level screen) |
| `?` | Show this screen's keys (any screen without a focused text field) |
| `ctrl+c` | Force quit |
| `esc` | Back / cancel (context-dependent) |

### Menu
| Key | Action |
|-----|--------|
| `↑↓` or `j/k` | Navigate |
| `enter` | Open selected screen |
| `p` | Pull current branch (visible when behind origin) |
| `s` | Sync with `origin/main` (visible when behind main) |
| `S` | Open the stash manager (visible when something is stashed) |

### Files
| Key | Action |
|-----|--------|
| `↑↓` or `j/k` | Navigate |
| `space` | Toggle file selection |
| `a` | Select / deselect all |
| `/` | Fuzzy filter |
| `d` | Diff preview |
| `x` | Discard this file's changes (delete if untracked, restore if deleted) |
| `b` | Open branch manager |
| `g` | Gitignore mode |
| `u` | Undo last commit (`r` reverts instead when the commit is pushed) |
| `enter` | Continue to commit type |

### Diff preview
| Key | Action |
|-----|--------|
| `↑↓` or `j/k` | Scroll |
| `e` | Edit file (for modifiable files) |
| `esc` | Back to file list |

### Edit mode
| Key | Action |
|-----|--------|
| `ctrl+s` | Save |
| `esc` | Back (prompts if unsaved) |

### Commit wizard
| Key | Action |
|-----|--------|
| `↑↓` | Navigate / move between fields |
| `enter` | Confirm / next |
| `tab` | Toggle body (message step) |
| `!` | Toggle breaking-change marker (type step) |
| `esc` | Back |

### Branch manager
| Key | Action |
|-----|--------|
| `↑↓` | Navigate |
| `enter` | Switch to selected branch |
| `c` | Create branch |
| `r` | Rename branch (local branches only) |
| `d` | Delete branch |
| `m` | Merge (opens target picker) |
| `S` | Open the stash manager (visible when something is stashed) |
| `esc` | Back to menu |

### Stash manager
| Key | Action |
|-----|--------|
| `↑↓` or `j/k` | Navigate |
| `enter` or `d` | Toggle the patch preview (`↑↓` scrolls it, `esc` closes it) |
| `a` | Apply — restores the changes, keeps the entry |
| `p` | Pop — restores the changes and removes the entry |
| `x` | Delete the entry (asks first; there is no undo) |
| `esc` | Back to menu |

### History browser
| Key | Action |
|-----|--------|
| `↑↓` or `j/k` | Navigate (older pages load as you approach the end) |
| `enter` or `d` | Open the commit — message, author, date, refs, stat |
| `p` | Toggle the full patch (in the detail pane) |
| `esc` | Patch → detail → list → menu |

### Config editor
| Key | Action |
|-----|--------|
| `↑↓` | Navigate |
| `enter` | Edit value / toggle / open picker |
| `tab` | Toggle Local / Global scope |
| `esc` | Back to menu |

### Done
| Key | Action |
|-----|--------|
| `enter` or `esc` | Return to menu |
| `q` | Quit |

## Development

```bash
make build      # compile to ./git-assist
make run        # build + run
make install    # build + install to ~/.local/bin/ (+ macOS codesign)
make clean      # remove the binary
```

Builds embed the current `git describe --tags --always --dirty` output as the version string. Tagged commits show the tag (e.g. `v1.1.0`); untagged commits show the short hash, suffixed `-dirty` if the working tree has uncommitted changes.

### Layout

```
main.go                    Entry, flag and subcommand parsing
internal/
  git/                     Pure git operations — no TUI dependencies
                           (git.go, stash.go, history.go, gitignore_templates.go)
  types/                   Shared types (FileEntry, BranchEntry, …)
  ui/
    model.go               Bubble Tea Model, step enum, async msg handlers
    layout.go, styles.go   Rendering helpers + color/symbol system
    help.go                Per-screen key lists — footers and the ? overlay
    step_menu.go           Main menu hub with commit graph
    step_files.go          File selector, diff, edit, gitignore, discard, undo/revert, filter
    step_branch.go         Branch manager (switch, create, rename, delete, merge)
    step_stash.go          Stash manager (list, preview, apply, pop, delete)
    step_history.go        History browser (paged commit list, detail, patch)
    step_config.go         Config editor
    step_init.go           First-run init flow
    step_sync.go           Pull / sync-with-main dialog
    step_type.go           Commit type picker
    step_message.go        Commit message input
    step_confirm.go        Confirmation
    step_push.go           Push confirm (outgoing commits, publish, force-with-lease)
    step_done.go           Done screen
```

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for the full release history.

## License

[MIT](LICENSE)
