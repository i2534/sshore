# sshore

**English** | [简体中文](README.md)

Cross-platform SSH port-forwarding, remote-folder-sync and SFTP manager. Built with
Wails v2 (Go) + Vue 3, driving your system's own OpenSSH for every connection.

## Screenshots

> Captured against a throwaway local `sshd` with demo data.

| Port forwarding | File sync |
| :---: | :---: |
| ![Port forwarding: tunnel rules, live state and log panel](docs/images/forward.png) | ![File sync: rule card with sync counters and logs](docs/images/sync.png) |
| **SFTP** | **Settings** |
| ![SFTP: dual-pane browser, recent locations and transfer queue](docs/images/sftp.png) | ![Settings: theme, font size, fonts and startup](docs/images/settings.png) |

## Requirements

- Go 1.26
- Node.js 22+ (LTS)
- OpenSSH (`ssh`, `sftp`) on `PATH`
- wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)

## Install

Download the archive for your platform from
[GitHub Releases](https://github.com/i2534/sshore/releases) —
`sshore-<version>-linux-amd64.tar.gz` or `sshore-<version>-windows-amd64.zip` —
unpack it and run the `sshore` binary.

- Authentication is delegated to system OpenSSH (keys / ssh-agent /
  `~/.ssh/config`); no credentials are stored by the app.
- Linux needs webkit2gtk-4.1 (Ubuntu/Debian: `libwebkit2gtk-4.1-0`).
- Release binaries are already UPX-compressed; UPX is not needed at runtime.

## Build

```bash
make both     # clean + Linux & Windows binaries (default, size-optimized)
make linux    # Linux amd64 only
make windows  # Windows amd64 only (packaged: icon embedded via .syso)
make build    # current platform
```

Or directly with the Wails CLI:

```bash
wails build
```

### Size optimization

The `make` targets strip symbols/DWARF (`-ldflags "-s -w"`), pass `-trimpath`,
and by default UPX-compress the final binaries (Linux ≈3.7 MB, Windows ≈4.5 MB).
The version from `git describe --tags` is injected as `-X main.Version=...` so the
in-app Settings panel can show it.

- Set `COMPRESS=0` to skip UPX (e.g. if your antivirus flags UPX-packed Go
  binaries, or you prefer faster startup): `make both COMPRESS=0`
- UPX must be on `PATH`; if missing, `wails build -upx` warns and skips
  compression (Wails only compresses when UPX is installed).
- Do **not** pass `-nopackage` when building for Windows: Wails only generates
  the icon-bearing `.syso` resource when packaging is enabled (`Pack=true`),
  so `-nopackage` yields an exe with no icon. The `make windows` target is
  intentionally packaged.

## Run (dev)

```bash
wails dev
```

## Features

- **SSH port forwarding**: local `-L`, remote `-R`, dynamic SOCKS `-D`, jump host `-J`
- **Auto reconnect**: per-rule toggle. An unexpected drop is retried with backoff
  (`Ns 后第 N 次重连`) and the card shows `reconnecting` / `error` instead of
  silently pretending to be connected
- **SFTP file management**: dual-pane browse / multi-select (Ctrl-click, Shift-range,
  Ctrl+A) / batch download, upload and delete / right-click menu / drag-and-drop
  delivery (pane to pane, dropped in from the OS, dragged onto a subdirectory to
  move) / instant filter in both panes; per-pane 📍 location dropdown (bookmarks +
  recents, up to 20 each, remote recents isolated per host) and ☆ bookmark toggle /
  recursive deep search in both panes (cancellable, 5 levels by default, up to 500
  hits, unreadable directories reported honestly) / "show hidden files" toggle;
  transfer queue showing direction, source to target, and skipped/failure reasons
- **Remote folder sync**: monitor a remote directory *or a single file* and sync
  changes to local using `inotifywait` when available, falling back to polling
  (or force polling with `force_poll`); configurable `max_depth` (`-1` =
  unlimited) and exclude patterns; local files are never deleted by default
  (mirror-delete must be enabled explicitly, and only applies to directory
  rules); a locally-modified file is not overwritten but queued as a conflict for
  you to resolve (keep local / overwrite with remote / save remote copy); bulk
  deletes are held for an explicit confirmation; failed transfers can be retried
- **Settings**: dark / light / follow-system theme, font size (small / standard /
  large), Latin and CJK font stacks, "connect tunnels on launch", and the default
  auto-reconnect value for new sync rules — all persisted in the TOML config
- **Config**: reads `~/.ssh/config` read-only (host enumeration); tunnel and sync
  rules are stored in `~/.config/sshore/sshore.toml`
  (`%APPDATA%\sshore\sshore.toml` on Windows) and written atomically with `0600`
- **Import** a pasted `ssh -L/-R/-D ...` command into rules
- **Live log panel**: in-memory ring buffer (1000 entries) of tunnel/SFTP/sync
  events; per-rule log filtering (card button / panel chips), with ssh child-process
  stderr streamed line-by-line into the panel
- **Rule validation**: create/edit/import rejects an empty target host
  (`-L 23080::3080` makes ssh fail name resolution: connections reset while the
  process stays alive and the state wrongly shows connected); `ExitOnForwardFailure`
  puts local/remote tunnels into `error` state when forwarding cannot be set up
- Uses system OpenSSH for auth (keys/agent/config), so password/keyboard-interactive
  auth is not supported in the GUI — configure key or agent auth for your hosts

## Configuration

`sshore.toml` is safe to hand-edit: missing keys fall back to defaults, and
empty/invalid values (theme, `font_scale`, `max_depth`, `poll_interval_s`,
missing exclude list) are normalized on load.

```toml
legacy_migrated = true           # legacy recent_sftp already migrated (never written back; parsed for one release so downgrades still work)

[app]
  theme = "dark"                 # dark | light | system
  font_scale = 1.0               # 0.9 | 1.0 | 1.15
  auto_start_on_launch = false   # connect tunnels when the app starts
  auto_reconnect_default = true  # default for newly created sync rules

[[tunnels]]                      # port-forward rules (-L / -R / -D)
[[syncs]]                        # remote → local sync rules
[[bookmarks]]                    # pinned locations (scope = local | remote)
[[local_recent]]                 # local recent locations (recorded automatically, max 20)
[[remote_recent]]                # remote recent locations (isolated per host, max 20)
[[recent_sftp]]                  # legacy field: migrated on first launch into the two above; still parsed for downgrades
```

## Architecture

- `internal/config` — parse `~/.ssh/config` (enumeration via kevinburke/ssh_config,
  authoritative fields via `ssh -G`) and read/write the atomic TOML config store
- `internal/forward` — spawn/manage long-lived `ssh -N` subprocesses, lifecycle state
  machine, port pre-check, error classification, auto-reconnect backoff
- `internal/sftp` — one `sftp -b` process per operation, `ls -la` parsing
- `internal/sync` — sync engine: state machine, conflict queue, delete gate,
  transfers, path mapping
- `internal/watch` — remote change detection: `inotifywait` probing with a
  polling fallback
- `internal/sshconn` — single source of ControlMaster socket paths and shared
  ssh/sftp connection options
- `internal/importer` — tokenize `ssh -L/-R/-D` command lines into rules (inject-safe)
- `internal/osutil` — cross-platform process runner, cancellation and signal handling
- `frontend/src` — Vue 3 UI (left-nav module switcher: Forward / File Sync / SFTP) + Pinia stores

## Test

```bash
go test ./...          # Go subsystem tests (mocked ssh/sftp)
cd frontend && npx vitest run   # frontend unit tests (stores, sync utils)
```

`make ci` runs everything CI runs in one shot (vet + Go tests with `-race` + frontend tests).

## E2E

```bash
make e2e    # equivalent to: bash e2e/test_local.sh
```

`e2e/test_local.sh` starts a throwaway local sshd and verifies the OpenSSH behaviors
sshore relies on: `ssh -G` alias resolution, `-N -L` local forward binding, and
`sftp ls -l` output parsing. Requires `/usr/sbin/sshd`, `ssh-keygen`, and `python3`.

## CI

`.github/workflows/ci.yml` runs on push/PR:

- `go` — `go vet` + Go tests with `-race` on Ubuntu
- `go-windows` — the same vet/tests on a real Windows runner (covers the
  `osutil` Windows branches, `%APPDATA%` config path, Windows OpenSSH `ls` output)
- `frontend` — `vitest run` + Vite build
- `build-linux` / `build-windows` — `wails build` for Linux (webkit2gtk-4.1) and
  Windows (icon via `.syso`), both UPX-compressed and packaged as versioned,
  platform-suffixed archives
- `release` — on a `v*` tag (or manual dispatch with `release_tag`), publishes
  those archives to a GitHub Release
