# git-assist

An interactive TUI git dashboard built with Go and [Bubble Tea](https://github.com/charmbracelet/bubbletea). Designed to make everyday git workflows obvious — branching, syncing, committing, resolving conflicts, and connecting a repo to GitHub — without trading away the underlying primitives.

```
╭────────────────────────────────────────────────────────────╮
│  git-assist   ⎇ main  3 changes  ↓2 behind main            │
│                                                            │
│  ▸ Commit       3 changes                                  │
│    Amend        edit last commit                           │
│    Push         ↑2 ahead                                   │
│    Branch       4 branches                                 │
│    Stash        1 stashed                                  │
│    History      214 commits                                │
│    Config       git settings                               │
│                                                            │
│    ↑↓ navigate  enter select  p pull  s sync  S stash      │
│    ? help  q quit                                          │
│                                                            │
│  ────────────────────────────────────────────────────────  │
│  Commit Graph                              main: 2 ahead   │
│  ●  feat(conflicts): in-app conflict resolution            │
│  ●  feat(history): commit history browser                  │
│  ●  feat(stash): in-app stash manager                      │
│  ●  perf(ui): async dashboard refresh                      │
╰────────────────────────────────────────────────────────────╯
```

Entries appear when they apply. `Amend` and `History` need at least one commit; `Push` shows up when origin is missing something (commits to send, a branch it has never seen, or a rewrite it still contradicts); `Stash` only once something is stashed. An unfinished merge puts a `⚠ Resolve conflicts` entry above all of them, and a detached HEAD reduces the list to the ways out.

## Features

### Commit workflow

- File selector with status indicators (`M / A / D / R / ?`), fuzzy filter (`/`), inline diff preview (`d`), in-place file editor (`e` from diff).
- Filenames with spaces, quotes or non-ASCII characters are read the same way git writes them (`--porcelain=v1 -z`), so they show up in the list rather than silently going missing. Renames are staged as both halves, so the old path's deletion lands in the same commit.
- Staging is all-or-nothing: if any selected path fails to stage, the commit aborts and names it instead of quietly committing the rest.
- Conventional commit types — `feat / fix / docs / refactor / style / test / chore / ci / perf / build`, plus custom types (validated as a single lowercase word, with a visible counter).
- Optional `scope` inline with the subject; optional body (`tab`, cursor-navigable); `!` toggles a breaking-change commit and says so on the type picker.
- Empty subjects are rejected with a clear inline error.
- Discard a file's changes (`x`) with a confirmation that states exactly what is lost — and that adapts to the file's status: modified files are restored from the last commit, untracked files are deleted, renames are undone on both paths, and a **deleted** file is brought back (the key relabels itself `restore`).
- Undo last commit (`u`) with a confirmation prompt. If the commit is already on origin the prompt offers both exits: `u` undoes anyway (rewrites history — the next push is then offered as a force-with-lease) or `r` **reverts** instead — a new commit that undoes it, safe for pushed work. A conflicting revert aborts itself and reports that nothing changed.

### Amend the last commit

`Amend` on the menu re-opens the wizard on the commit that is already there.

- The previous commit's type, scope, breaking flag, subject and body are pre-loaded, so only what is actually changing gets typed.
- Subjects that are not conventional commits — merges, `Initial commit`, plain prose — take the raw path: no type picker, no prefix, and the subject goes back to git byte for byte rather than being reshaped into `chore: …`.
- Additional files can be staged in the Files step; an empty selection is a message-only amend, and the confirm screen says so, listing anything already staged under "Also included".
- When the commit is already on a remote, the confirm screen says the next push will need a force — and the Push screen then offers exactly that (below), instead of printing a command to run somewhere else.
- Abandoning an amend clears the whole wizard, so the old message cannot leak into the next ordinary commit.

### Push

Push always pushes the **current** branch to `origin/<branch>`. There is no branch picker: the old one built a `HEAD:<target>` refspec that could put the current commits onto some other branch's remote ref, with nothing on screen saying so.

- Reachable from the menu (whenever origin is missing something), from the wizard after a commit, and from the Done screen with `p` for a push that was skipped or that failed.
- The screen shows what will happen before it happens: the outgoing commits and their count, or an explanation that this branch has no upstream yet and the push will publish it (`-u`, only on the first push).
- After an amend or an undo of a commit origin already had, `f` offers a **force-with-lease** with a plain-words explanation of what "force" means here. It is offered only while origin's tip is still exactly the commit that was rewritten — if someone else pushed on top, the force is withheld rather than leased against their work.
- The lease is pinned to that pre-rewrite SHA. Git's default lease is taken against the remote-tracking ref, which this app's own background fetch moves; pinning is what keeps "replace the commit I showed you" from becoming "replace whatever is there now".
- A branch that is merely *behind* never reaches the force key. That is the pull case.

### Branch manager

Reachable from the menu, from the file selector (`b`), or directly via `git-assist branch`.

- Switch, create, rename (`r`), and delete (`x`, the same destructive key the file selector and the stash manager use) branches. `main` / `master` and the current branch are protected from deletion.
- New branch names are validated with `git check-ref-format` and rejected with a readable message, rather than by git's `fatal: … is not a valid pathspec`.
- Deleting a branch with unmerged commits opens a second confirmation and then force-deletes (`-D`) — no trip to a terminal to repeat the command.
- Renaming discloses that `git branch -m` is local only: if the branch has an upstream, both the prompt and the result say origin still has the old name.
- Auto-stash and restore around branch switches, merges and pulls — **includes untracked files** — and the result says what happened ("Switched to feat — 3 changed files stashed and restored"). If a stash-pop conflicts, the short SHA of your stash is surfaced and `S` opens the stash manager right there.
- Merge with `--no-ff` so the fork/merge diamond is always visible in the graph.
- Merge target picker — choose which branch to merge **into** with a direction arrow; auto-switches to the target first if needed. Targets are local branches only, so picking one can no longer create a tracking branch as a side effect.
- Conflicting merges open the conflict resolver (below) instead of being abandoned.

### Conflict resolver

A conflicting merge, pull or sync used to be aborted on the spot with "resolve it in your terminal" — git-assist could start a merge it could not finish. Now it stops here, and the abort is one keypress away instead of the only outcome.

- Explains what happened in plain words: *"Merging feat into main stopped — 2 files have conflicting changes. git needs you to pick which version to keep in each."*
- Every conflicting file is listed with what actually happened to it: **both changed it**, **you changed it, feat deleted it**, **you deleted it, feat changed it**, **you and feat each added a file with this name** — parsed from git's unmerged index entries (`UU / AA / UD / DU / DD / AU / UA`), not guessed.
- `o` keeps **your** version, `t` takes the incoming branch's. On a file the other side deleted, the key that "keeps that side" **deletes the file**, and both the footer and the line above it say so rather than pretending it is a content choice.
- `e` opens the conflicted file in the built-in editor with the markers visible, above a legend: `<<<<<<< yours … ======= … >>>>>>> theirs — delete the markers, keep what you want, save`. Saving does **not** mark it resolved (half-finished saves are normal); `m` does, and warns once if the markers are still in there.
- Resolved files get a ✓, move to their own section, and the header counts down (`2 of 3 resolved`).
- `c` finishes the merge (`git commit --no-edit`, so no editor is ever launched on top of the TUI); `a` aborts the whole thing — after a `y/N` that says how many resolutions go back to conflicted, since `a` means select-all one screen over.
- Uncommitted work auto-stashed before the merge stays parked until the merge is committed or aborted — git refuses to restore a stash into an unmerged index — and the screen says so instead of leaving you to wonder where your edits went. The parked entry is addressed by its SHA, and the stash manager's apply / pop / delete are locked while the merge is open, so nothing can move it out from under the resolver.
- Quit halfway and the merge is still there: the next launch opens straight on the resolver with an "unfinished merge from earlier" banner, and it picks the parked stash back up — the association is recorded in `.git`, next to git's own merge state. A `git merge` you started in a terminal is picked up the same way, with the branch names read from git's own merge message.
- While a merge is open the dashboard carries a permanent `⚠ Resolve conflicts` entry, and commit / amend / push / branch switching answer "finish or abort the merge first".

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
- Loaded in pages of 200 and extended automatically as you scroll, so opening the browser on a 50,000-commit repository is as fast as on a fresh one. The counter is honest about it (`200 of 3412 commits loaded`).
- `enter` / `d` opens the commit: short and full SHA, author and email, absolute date *and* relative age, refs, the full message (subject **and** body), and the `--stat` block.
- `p` shows the whole patch, diff-coloured and scrollable. Patches over 2 MiB are capped and say so, with the true size. A clean merge's empty patch is explained rather than shown as a blank pane.
- Read-only by design: no checkout, no revert-from-here, no cherry-pick. Nothing on this screen can change your repository, and a test asserts that every mutating key is a no-op here.
- Works on a detached HEAD too — where the header names the commit you are sitting on.

### Remote sync

- Sync dialog auto-shows on startup when the current branch is behind origin or behind main.
- `p` to pull the current branch (fast-forward when possible). A branch that is both ahead and behind — the usual state after amending a pushed commit — gets an explicit warning instead of a naive pull offer.
- `s` to merge `origin/main` into the current branch (`--no-ff` for an explicit integration commit).
- "Behind main" is measured against `origin/<main>` when it exists, so a stale local `main` can no longer wedge the badge or the shortcut.
- Background fetch on startup and on return-to-menu, debounced to 30 seconds. A failed fetch does not reset the debounce, and the header says so quietly: `(offline — sync info may be stale)`.
- Network operations (fetch, pushes, `gh repo create`) time out after 60 seconds and cancel on force-quit; local operations — including merges of already-fetched refs — are untouched.
- A pull or sync that conflicts opens the conflict resolver, named after the operation you asked for; neither offer appears while a merge is still open, since both would start a second one.

### First-run init flow

When launched in a non-git directory, git-assist offers four paths instead of erroring:

1. **Initialize local repo** — `git init` only.
2. **Connect to GitHub repo** — `git init` + add an existing remote URL.
3. **Create new GitHub repo** — `git init` + `gh repo create` (wires `origin` via `--source=.`).
4. **Cancel** — quit without changes.

Includes:

- Language-aware `.gitignore` templates (Go / Node / Python / Rust / Generic) auto-detected from marker files.
- `gh auth login --web` integration when the GitHub CLI isn't authenticated — and a re-check afterwards, because `gh` exits 0 even when the browser flow was abandoned.
- Upfront URL and repo-name validation — bad inputs are caught before shelling out.
- An unrelated-histories guard when connecting to a remote that already has commits, since git would refuse every push and pull between the two.

A "Connect to GitHub" recovery entry appears in the menu when an existing repo has no `origin` and the `gh` CLI is available.

### Config editor

- View and edit `user.name`, `user.email`, `init.defaultBranch`, `commit.gpgsign`, `core.editor`, and the origin URL.
- Toggle between local and global scope with `tab`. In an open edit, `tab` cancels the edit rather than writing the typed value into the other scope.
- Clearing a field **unsets** the key instead of writing an empty string over the global value. Clearing the origin URL asks before removing the remote.
- Branch picker for `init.defaultBranch` (pick from existing local branches, no free-text typos).
- "not set" hint distinguishes unconfigured keys from intentionally-empty values.

### Visual feedback

- Unified commit graph on the main menu — compact one-line-per-commit on linear history, with branch / fork / merge connectors for divergence points, octopus merges included. Branch decorations are purple, tags amber; subjects containing brackets can no longer masquerade as decorations.
- Every remaining silent success now says what it did — merges, pulls, syncs, branch deletes, `.gitignore` edits, stashed switches all leave a one-line note on the dashboard.
- Dashboard reads run off the UI thread and land as a single snapshot; redraw on a large repository went from ~2.9s to ~0.02s.
- "N behind main" warning on the dashboard.
- Press `?` on any non-typing screen for an overlay listing exactly the keys that screen responds to — sourced from the same data the footers render, so it can never drift.
- Detached-HEAD guard: a warning banner on the dashboard, and everything that assumes a branch (commit, amend, push, sync) is hidden until you switch to one.
- Spinner animations during async ops; input is blocked during them, and `ctrl+c` in the middle of one warns before the second press quits.
- Adaptive colours (readable on light and dark terminals), Unicode symbols with automatic ASCII fallback, and width-aware truncation so multi-byte text is never cut mid-character.
- Respects `NO_COLOR=1` and the `--no-color` flag.

## Installation

Requires **Go 1.26.2+** (as pinned in `go.mod`) and **Git 2.32+**. The floor is `git stash show --include-untracked`, which the stash preview uses; `git branch --show-current`, `git stash push` and `git restore` set lower ones. The `gh` CLI is optional and only needed for creating a GitHub repo from the init flow.

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

```
git-assist — an interactive TUI git dashboard

USAGE
  git-assist [command] [flags]

COMMANDS
  (none)      open the dashboard: commit, amend, push, branches, config
  branch      jump straight into the branch manager
  help        show this help

FLAGS
  -h, --help       show this help
  -v, --version    print the version and exit
      --no-color   disable colours (same as NO_COLOR=1 in the environment)

NOTES
  Run it anywhere inside a repository — git-assist moves to the repository root
  itself, so paths and .gitignore resolve the way git reports them.

  Run it outside a repository and it offers first-run setup (git init, connect
  an existing GitHub repo, or create one with the gh CLI) instead of failing.

  Press ? on any screen for the keys that screen responds to.
```

Running `git-assist` outside of a git repository launches the first-run init flow instead of erroring. Running it in a subdirectory is fine — it moves to the repository root first, so paths resolve the way git reports them.

## Keybindings

Every list below is what the app's own `?` overlay shows, because both come from the same table in `internal/ui/help.go`. Where a screen lists `↑↓`, `j` / `k` work too.

### Global
| Key | Action |
|-----|--------|
| `q` | Quit (from any top-level screen) |
| `?` | Show this screen's keys (any screen without a focused text field) |
| `ctrl+c` | Force quit (warns first if an operation is running) |
| `esc` | Back / cancel (context-dependent) |
| `S` | Open the stash manager — from **any** screen showing a stash-recovery banner (the banner ends in "press S", and it lands wherever the failed operation finished) |

### Menu
| Key | Action |
|-----|--------|
| `↑↓` | Navigate |
| `enter` | Open selected screen |
| `p` | Pull current branch (visible when behind origin) |
| `s` | Sync with `origin/main` (visible when behind main) |
| `S` | Open the stash manager (visible when something is stashed) |

### Files
| Key | Action |
|-----|--------|
| `↑↓` | Navigate |
| `space` | Toggle file selection |
| `a` | Select / deselect all |
| `enter` | Continue to commit type |
| `/` | Fuzzy filter |
| `d` | Diff preview |
| `x` | Discard this file's changes — labelled `discard` / `delete` / `restore` / `undo rename` per status |
| `u` | Undo last commit (`r` reverts instead when the commit is pushed) |
| `b` | Open branch manager |
| `g` | Gitignore mode |
| `esc` | Back to menu |

Confirmations on this screen (discard, undo) answer `y` to confirm and any other key to cancel. In gitignore mode: `space` toggles, `a` toggles all, `enter` applies, `g` or `esc` cancels.

### Diff preview
| Key | Action |
|-----|--------|
| `↑↓` | Scroll |
| `e` | Edit file (not offered for deleted files; binary files are refused on open) |
| `esc` | Back to file list |

### Edit mode
| Key | Action |
|-----|--------|
| `ctrl+s` | Save |
| `esc` | Back (prompts if unsaved: `y` discards, any key cancels) |

### Commit wizard
| Key | Action |
|-----|--------|
| `↑↓` | Navigate / move between fields |
| `enter` | Confirm / next |
| `tab` | Move between scope, subject and body (adds the body the first time) |
| `ctrl+d` | Leave the body and continue |
| `!` | Toggle breaking-change marker (type step) |
| `esc` | Back |

### Push
| Key | Action |
|-----|--------|
| `enter` | Push (or `publish branch` when it has no upstream) |
| `f` | Force-push with lease — offered only after a rewrite origin still contradicts |
| `n` / `esc` | Skip (the commit is already made) or back to the menu |

### Done
| Key | Action |
|-----|--------|
| `p` | Push now — labelled `push now (force-with-lease)` after amending a pushed commit |
| `enter` or `esc` | Return to menu |
| `q` | Quit |

### Remote sync
| Key | Action |
|-----|--------|
| `p` | Pull the current branch (`pull anyway` when the branch has diverged) |
| `s` | Merge `origin/<main>` into the current branch |
| `enter` | Skip |

### Branch manager
| Key | Action |
|-----|--------|
| `↑↓` | Navigate |
| `enter` | Switch to selected branch |
| `c` | Create branch |
| `r` | Rename branch (local branches only) |
| `x` | Delete branch — asks first; unmerged branches get a second, force-delete confirmation |
| `m` | Merge (opens target picker) |
| `S` | Open the stash manager (visible when something is stashed) |
| `esc` | Back to menu (`q` quits when launched as `git-assist branch`) |

### Stash manager
| Key | Action |
|-----|--------|
| `↑↓` | Navigate |
| `enter` or `d` | Toggle the patch preview (`↑↓` scrolls it, `esc` closes it) |
| `a` | Apply — restores the changes, keeps the entry |
| `p` | Pop — restores the changes and removes the entry |
| `x` | Delete the entry (asks first; there is no undo) |
| `esc` | Back to menu |

### History browser
| Key | Action |
|-----|--------|
| `↑↓` | Navigate (older pages load as you approach the end) |
| `enter` or `d` | Open the commit — message, author, date, refs, stat |
| `p` | Toggle the full patch (in the detail pane) |
| `esc` | Patch → detail → list → menu |

### Conflict resolver
| Key | Action |
|-----|--------|
| `↑↓` | Navigate the conflicting files |
| `o` | Keep **your** version (deletes the file when your side deleted it) |
| `t` | Take the incoming branch's version (deletes the file when that side deleted it) |
| `e` | Edit the file with the conflict markers visible (`ctrl+s` saves) |
| `m` | Mark the file resolved (asks first if markers are still present) |
| `c` | Finish the merge — offered once every file is decided |
| `a` | Abort: undo the whole merge, restore any auto-stash — asks first (`y`), stating how many resolutions it discards |
| `esc` | Back to the menu (the merge stays open, and the menu says so) |

### Config editor
| Key | Action |
|-----|--------|
| `↑↓` | Navigate |
| `enter` | Edit value / toggle / open picker |
| `tab` | Toggle Local / Global scope (cancels an open edit) |
| `esc` | Back to menu |

### Setup (first run)
| Key | Action |
|-----|--------|
| `↑↓` | Navigate the options, templates and visibility pickers |
| `enter` | Select / continue / create |
| `esc` | Back a step |
| `q` | Quit without changes |

## Development

```bash
make build      # compile to ./git-assist
make run        # build + run
make install    # build + install to ~/.local/bin/ (+ macOS codesign)
make clean      # remove the binary
go test ./...   # 300+ tests, all against scratch repositories
```

Tests are hermetic: every one that touches git builds its own repository in a temp directory, so running them cannot disturb the checkout you are working in.

Builds embed the current `git describe --tags --always --dirty` output as the version string. Tagged commits show the tag (e.g. `v1.3.0`); untagged commits show the short hash, suffixed `-dirty` if the working tree has uncommitted changes.

### Layout

```
main.go                    Entry, flag and subcommand parsing, chdir to repo root
internal/
  git/                     Pure git operations — no TUI dependencies
    git.go                 Status, commit, branches, remotes, config, diff, discard
    stash.go               Stash stack: list/show/apply/pop/drop, ref↔SHA resolution
    history.go             Paged commit log, detail, stat, patch (read-only)
    conflict.go            Unmerged-index parsing, ours/theirs routing, MERGE_HEAD
    gitignore_templates.go Language templates + marker-file detection
  types/                   Shared types (FileEntry, BranchEntry, …)
  ui/
    model.go               Bubble Tea Model, step enum, async msg handlers
    layout.go, styles.go   Rendering helpers + colour/symbol system
    help.go                Per-screen key lists — footers and the ? overlay
    step_menu.go           Main menu hub with commit graph
    step_files.go          File selector, diff, edit, gitignore, discard, undo/revert, filter
    step_branch.go         Branch manager (switch, create, rename, delete, merge)
    step_stash.go          Stash manager (list, preview, apply, pop, delete)
    step_history.go        History browser (paged commit list, detail, patch)
    step_conflicts.go      Conflict resolver (ours/theirs, marker editor, continue, abort)
    step_config.go         Config editor
    step_init.go           First-run init flow
    step_sync.go           Pull / sync-with-main dialog
    step_type.go           Commit type picker
    step_message.go        Commit message input
    step_confirm.go        Confirmation
    step_push.go           Push confirm (outgoing commits, publish, force-with-lease)
    step_done.go           Done screen
```

## Roadmap

See [ROADMAP.md](ROADMAP.md) for what is still open and what has been ruled out.

## Changelog

See [CHANGELOG.md](CHANGELOG.md) for the full release history.

## License

[MIT](LICENSE)
