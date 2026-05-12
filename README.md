# git-assist

An interactive TUI git dashboard built with Go and [Bubble Tea](https://github.com/charmbracelet/bubbletea). Designed to make everyday git workflows obvious — branching, syncing, committing, and connecting a repo to GitHub — without trading away the underlying primitives.

```
╭──────────────────────────────────────────────────╮
│  git-assist   ⎇ main   clean                     │
│                                                  │
│  ▸ Commit    no changes                          │
│    Branch    3 branches                          │
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
- Undo last commit (`u`) with a confirmation prompt.

### Branch manager
Reachable from the menu, from the file selector (`b`), or directly via `git-assist branch`.
- Switch, create, and delete branches. `main` / `master` and the current branch are protected from deletion.
- Auto-stash and restore around branch switches and pulls — **includes untracked files**. If a stash-pop conflicts, the short SHA of your stash is surfaced so you can recover with `git stash apply <sha>`.
- Merge with `--no-ff` so the fork/merge diamond is always visible in the graph.
- Merge target picker — choose which branch to merge **into** with a direction arrow; auto-switches to the target first if needed.
- Conflicting merges auto-abort with a clear file count, never leaving the repo half-merged.

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
git-assist --version     # show installed version
git-assist --no-color    # disable color (also respects NO_COLOR=1)
```

Running `git-assist` outside of a git repository launches the first-run init flow instead of erroring.

## Keybindings

### Global
| Key | Action |
|-----|--------|
| `q` | Quit (from any top-level screen) |
| `ctrl+c` | Force quit |
| `esc` | Back / cancel (context-dependent) |

### Menu
| Key | Action |
|-----|--------|
| `↑↓` or `j/k` | Navigate |
| `enter` | Open selected screen |
| `p` | Pull current branch (visible when behind origin) |
| `s` | Sync with `origin/main` (visible when behind main) |

### Files
| Key | Action |
|-----|--------|
| `↑↓` or `j/k` | Navigate |
| `space` | Toggle file selection |
| `a` | Select / deselect all |
| `/` | Fuzzy filter |
| `d` | Diff preview |
| `b` | Open branch manager |
| `g` | Gitignore mode |
| `u` | Undo last commit |
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
| `d` | Delete branch |
| `m` | Merge (opens target picker) |
| `esc` | Back to menu |

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
  types/                   Shared types (FileEntry, BranchEntry, …)
  ui/
    model.go               Bubble Tea Model, step enum, async msg handlers
    layout.go, styles.go   Rendering helpers + color/symbol system
    step_menu.go           Main menu hub with commit graph
    step_files.go          File selector, diff, edit, gitignore, undo, filter
    step_branch.go         Branch manager
    step_config.go         Config editor
    step_init.go           First-run init flow
    step_sync.go           Pull / sync-with-main dialog
    step_type.go           Commit type picker
    step_message.go        Commit message input
    step_confirm.go        Confirmation
    step_push.go           Push picker
    step_done.go           Done screen
```

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for the full release history.

## License

[MIT](LICENSE)
