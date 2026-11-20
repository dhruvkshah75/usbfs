# AGENTS.md

Follow these mandatory rules for every change in this repository.

## 1. TUI Keybinding Policy (No Single-Letter Commands)

Both panes (left External Device pane and right Linux Host pane) use **automatic Type-to-Search**: any bare printable character typed by the user goes directly into the live search filter.

- **NEVER bind a command or action to a single bare letter, digit, or printable character** (e.g., `m`, `r`, `d`, `s`, `/`).
- **ALWAYS use a modifier combination or special/function key** for all new and updated commands:
  - Preferred: `Alt+<key>` (matching the existing `Alt+c` for Copy) or `Ctrl+<key>` (avoiding terminal control signals like `Ctrl+c`, `Ctrl+z`, `Ctrl+s`, `Ctrl+q`).
  - Also allowed: Function keys (`F1`–`F12`) and non-printable navigation keys (`Tab`, `Esc`, `Enter`, arrows).
- **No clashes:** Before adding a shortcut, check all existing bindings across the header, footer, help overlay, and key handler to ensure it neither conflicts with another command nor intercepts search input.
- **Keep UI labels in sync:** Update the top bar, footer hints, and `[F1]` Help screen whenever a keybinding is added or changed.

## 2. Build & Local Binary Replacement (Mandatory)

I invoke this tool globally via the `usbfs` command from `$HOME/.local/bin/usbfs`. Whenever you modify Go code or build a new executable, you **MUST** rebuild and replace the installed binary before finishing the task.

### Required Steps After Any Code Change
1. Run verification checks:
   ```bash
   go vet ./...
   go test ./...
