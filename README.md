# git-assist

Interactive TUI git commit wizard built with Go.

```
╭──────────────────────────────────────────────────╮
│  git-assist   ⎇ main                            │
│  Step 1/5 · Select files to commit               │
│                                                   │
│  ▸ ●  M   internal/git/git.go                    │
│    ●  M   internal/ui/model.go                   │
│    ○  A   internal/ui/styles.go                  │
│    ○  ?   test.md                                │
│                                                   │
│  2/4 selected                                    │
│                                                   │
│  ↑↓ navigate  space select  d diff  enter next   │
╰──────────────────────────────────────────────────╯
```

## Features

- **File selector** — pick which files to commit with checkboxes
- **Conventional commits** — choose commit type (feat, fix, refactor, etc.) with optional scope
- **Diff preview** — press `d` to view colored diffs inline
- **Inline editing** — press `e` in diff preview to edit files directly
- **Gitignore mode** — press `g` to add/remove `.gitignore` entries
- **Undo** — revert the last commit while keeping changes
- **Push** — pick a branch and push after committing

## Installation

Requires **Go 1.26+** and **Git**.

```bash
git clone https://github.com/y4hyya/Git-Assistant.git
cd Git-Assistant
make build
sudo make install
```

Then run inside any git repo:

```bash
git-assist
```

## Keybindings

### Step 1 — File selector

| Key | Action |
|-----|--------|
| `↑/↓` or `j/k` | Navigate |
| `space` | Toggle file selection |
| `a` | Select / deselect all |
| `d` | Preview diff |
| `g` | Gitignore mode |
| `u` | Undo last commit |
| `enter` | Next step |
| `q` | Quit |

### Diff preview

| Key | Action |
|-----|--------|
| `↑/↓` or `j/k` | Scroll |
| `e` | Edit file |
| `esc` | Back to file list |

### Edit mode

| Key | Action |
|-----|--------|
| `ctrl+s` | Save |
| `esc` | Back (prompts if unsaved) |

### Steps 2-5

| Key | Action |
|-----|--------|
| `↑/↓` | Navigate options |
| `enter` | Confirm / next |
| `tab` | Toggle commit body (step 3) |
| `esc` | Go back |

## License

MIT
