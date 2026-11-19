# Project Specification: Universal USB Mobile & Tablet File Explorer TUI

## 1. Project Overview & Goal
Build a fast, reliable, single-binary Terminal User Interface (TUI) application in **Go** for Linux (Ubuntu) that automatically detects, mounts, browses, opens, and transfers files between a Linux host machine and a USB-connected mobile or tablet device (supporting both **iOS/iPadOS** and **Android** devices over USB-C / USB).

The primary design priority is **data safety and zero file corruption**:
1. The TUI must automatically detect which type of external device is plugged in (or gracefully report if no device is connected).
2. The TUI must automatically mount the connected external device upon startup.
3. The external device must be protected against accidental deletion or overwriting—the user can **read/open** files on the external device, **copy/download** files from the external device to the laptop, and **copy/upload** files from the laptop into permitted directories on the external device, **without any ability to delete, rename, or overwrite existing files on the external device**.
4. Exiting the TUI must trigger an interactive confirmation prompt and execute a verified unmount sequence before the process terminates, ensuring the USB cable can be unplugged safely without filesystem corruption.

---

## 2. Recommended Tech Stack & Architecture
* **Language:** Go (1.22+) — Unified single-binary architecture handling both the TUI loop and OS subprocess/FUSE lifecycle management. No separate backend service is required because Linux FUSE drivers (`ifuse` / `simple-mtpfs`) handle the low-level USB protocols at the OS level.
* **TUI Framework:**
  * `github.com/charmbracelet/bubbletea` (Elm-architecture event loop)
  * `github.com/charmbracelet/bubbles` (viewport, list, spinner, progress/status components)
  * `github.com/charmbracelet/lipgloss` (clean, minimal dark-themed layout styling)
* **Linux System Dependencies (invoked via `os/exec`):**
  * **For iOS / iPadOS devices:** `libimobiledevice-utils` (`idevice_id`, `ideviceinfo`) and `ifuse`
  * **For Android devices (MTP):** `simple-mtpfs` (or `jmtpfs`) and `libmtp`
  * **Unmounting & File Launch:** `fusermount3` (with fallback to `fusermount`) and `xdg-open`

---

## 3. Core Functional Requirements

### 3.1 Startup & Device Detection Flow
When the user launches the executable from any terminal:
1. **Dependency Check:** Verify that required system utilities (`fusermount3` or `fusermount`, plus `idevice_id` / `simple-mtpfs`) are installed on the host. If missing, display a clear, actionable installation message and exit cleanly.
2. **Automatic Device Detection (No Hardcoded Device Type):**
   * Query connected iOS/iPadOS devices via `idevice_id -l`.
   * Query connected Android MTP devices via `simple-mtpfs -l`.
   * **Case A — No Device Connected:** Do NOT crash or hang. Either:
     * Launch into a clean "Waiting for USB Device..." TUI screen that polls every 2 seconds (with a `[r] Retry` and `[q] Quit` option), OR
     * Display a clear status screen stating *"No external mobile/tablet device detected. Ensure the USB cable is connected, the device is unlocked, and 'Trust' / 'File Transfer (MTP)' is enabled."*
   * **Case B — Device Detected:** Identify the device type (`iOS/iPadOS` or `Android MTP`) and retrieve its friendly device name if available.
3. **Automatic Mounting:**
   * Create a dedicated mount directory at `~/.cache/usbfs-mount` (creating parent directories if needed).
   * Before mounting, discover and clear stale usbfs mounts at the root and below it, working from children to parents.
   * Execute the appropriate mount command:
     * iOS/iPadOS: mount Goodnotes with `ifuse --documents com.goodnotesapp.x ~/.cache/usbfs-mount/Goodnotes` and the media partition with `ifuse ~/.cache/usbfs-mount/Media`. Discover additional file-sharing apps with `ifuse --list-apps` and mount each app's shared Documents folder under its sanitized display name. Skip unavailable shares and show a warning.
     * Android MTP: `simple-mtpfs --device <id> ~/.cache/usbfs-mount`
   * Verify the mount succeeded and the directory is readable before rendering the file panes.

---

### 3.2 Dual-Pane TUI Interface & Navigation
Once mounted, render a responsive side-by-side dual-pane TUI:
* **Top Header Bar:** Displays connected device type/name, mount path status (`MOUNTED`), and active read-safety badge (`DEVICE DELETE PROTECTED`).
* **Left Pane (External Device):** Displays directories and files inside `~/.cache/usbfs-mount`. On iOS/iPadOS, the root lists mounted shares with Goodnotes selected first when available. To upload, enter a mounted share first.
  * Show clear visual indicators for folders (`[DIR]`), readable files, and restricted/unreadable items (`[LOCKED]`).
  * Prevent directory traversal above `~/.cache/usbfs-mount` (sandbox root enforcement).
* **Right Pane (Linux Local Host):** Defaults to the user's current working directory (or `~/Downloads`), allowing full standard directory navigation on the local machine.
* **Bottom Status / Keybinding Bar:** Displays live feedback (copy progress, permission warnings, open status) and keybinding hints.

#### Required Keybindings:
* `Up` / `Down`: Move the cursor in the active pane.
* `Enter` or `Right`:
  * If directory: Enter directory (if permission denied, show a non-fatal status warning: *"Permission denied: external device restricts access to this folder"* without crashing).
  * If file on External Device or Linux Host: Launch the file directly on the Linux desktop using `xdg-open <filepath>` in a non-blocking subprocess so PDFs, images, and notes open immediately.
* `Left`: Navigate to the parent directory (clamped at the external device root).
* `Tab`: Switch active focus between the External Device pane and the Linux Local pane.
* Use `Alt+I` to show the selected item’s name, location, path, type, file size, and modification time in a centered dialog.
* Typing searches names in the focused pane's current folder and its subfolders. Show current-folder matches immediately and display nested matches by relative path. Each pane keeps its own search. `Backspace` erases a character, and `Esc` or `Ctrl+U` clears the search.
* `Alt+c`: Copy selected file to the directory currently open in the opposite pane:
  * **External Device $\rightarrow$ Linux Host (Download):** Copies file from external device to local Linux directory using buffered `io.Copy` inside a background goroutine so the UI stays responsive.
  * **Linux Host $\rightarrow$ External Device (Upload):** Copies file from Linux directory into the currently open folder on the external device. If the destination file already exists on the external device, **block overwriting** and notify the user (*"Overwrite blocked: file already exists on external device"*). If the target folder on the external device is read-only/sandboxed, catch the `EACCES`/`EPERM` error gracefully and display *"Upload blocked: external device folder is read-only."*
* Drag a file or folder between panes to copy it. Drop it onto a folder row to copy into that folder, or elsewhere in the opposite pane to copy into its open folder. The status area shows the drop target, copy progress, and the result. The terminal must support mouse reporting and have room to show both panes.
* Folder copies in either direction preview the destination and file count before confirmation. Copy the tree into a new directory, preserve existing items, and report copied files, skips, and failures.
* `Ctrl+N` creates a folder and `Ctrl+F` creates an empty file. Enter a single name in the prompt to create it in the focused pane's open folder. On iOS, open a mounted share first. Existing names and write restrictions block creation.
* `Delete` removes one selected regular file from the Linux host after confirmation with `y`. Press `n` or `Esc` to cancel. Directories, symbolic links, and external-device items are protected.
* `F1` opens a centered, scrollable command reference over the panes. Press `F1` or `Esc` to close it.
* `Alt+r`: Refresh directory listings and rerun active searches in both panes.
* `Alt+q` or `Esc`: Trigger the **Safe Unmount & Exit Confirmation Modal**. Plain `q` also works during device setup.

---

### 3.3 Strict Safety & Permission Rules
1. **Zero Deletion on External Device:** There must be **NO delete (`rm`), move (`mv`), or truncate/overwrite functionality** implemented for files on the external device pane. Even if a user presses `Delete` or `d`, nothing on the external device may be modified or removed.
2. **Graceful Handling of Restricted / Sandboxed Files:**
   * Mobile operating systems (especially iPadOS/iOS and scoped Android directories) restrict read access to certain system or app-sandboxed files.
   * Before reading, opening, or copying a file from the external device, check readability (`os.Open` / `os.Stat`).
   * If a file or folder returns `os.ErrPermission` or an I/O read error, catch it cleanly and display a clear message in the status bar (*"Cannot read file: restricted by external device permissions"*) without panicking or corrupting the terminal state.
3. **Non-Blocking File Transfers:**
   * Large file copies (e.g., multi-GB videos or PDFs) must run asynchronously in a Go goroutine sending completion/error messages back to Bubble Tea's `Update` loop (`tea.Msg`), preventing UI freezes.

---

### 3.4 Guaranteed Safe Unmount & Exit Lifecycle (Critical Requirement)
To prevent filesystem corruption on either device:
1. **Exit Confirmation Modal:**
   * Pressing `Alt+q` or `Esc` opens a centered modal dialog:
     > `"Unmount external device and exit? [y] Yes, Unmount & Quit  /  [n] Cancel"`
   * If a background file copy is currently in progress, warn the user:
     > `"Transfer in progress! Wait for copy to finish before unmounting. Press [n] to return."`
2. **Verified Unmount Sequence:**
   * Upon confirming `y`:
     1. Exit Bubble Tea's alternate screen buffer and restore normal terminal mode.
     2. Execute `fusermount3 -u` for every active mount path, falling back to `fusermount -u`.
     3. Verify through `/proc/mounts` that all mounts at or under `~/.cache/usbfs-mount` are gone, then remove empty local share directories.
     4. If any mount is busy, stay running and retry with backoff until unmount is verified.
     5. Print an explicit confirmation message to `stdout` before exiting:
        * On success: `[OK] External device cleanly unmounted. It is now safe to unplug the USB cable.`
        * While busy: ask the user to close files opened from the device and report that unmount will be retried.
3. **Signal Handling (`SIGINT` / `SIGTERM`):**
   * Trap OS interrupt signals so unexpected terminal closes still attempt a clean `fusermount3 -u` via `defer` cleanup before terminating.

---

## 4. Suggested Project Directory Structure

```text
usb-explorer-tui/
├── go.mod
├── go.sum
├── SPEC.md
├── cmd/
│   └── main.go              # Entry point, signal trapping, guaranteed defer unmount
└── internal/
    ├── device/
    │   ├── detector.go      # Detects iOS (idevice_id) vs Android (simple-mtpfs)
    │   └── mounter.go       # Mount, unmount, retry verification, & stale-mount cleanup
    ├── fsops/
    │   └── transfer.go      # Safe read checks, non-overwriting copy, xdg-open launcher
    └── tui/
        ├── model.go         # Bubble Tea state (dual panes, modal state, async messages)
        ├── update.go        # Keybindings, copy commands, permission error handling
        └── view.go          # Lip Gloss dual-pane layout & confirmation modal rendering
