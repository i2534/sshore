# sshore

**English** | [简体中文](README.zh-CN.md)

Cross-platform SSH port-forward + SFTP manager. Built with Wails v2 (Go) + Vue 3.

## Requirements

- Go 1.26
- Node.js 22+ (LTS)
- OpenSSH (`ssh`, `sftp`) on `PATH`
- wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)

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
- **SFTP file management**: browse / upload / download / recursive download / delete / rename / mkdir
- **Config**: reads `~/.ssh/config` read-only (no credential storage); tunnel rules stored in
  `~/.config/sshore/sshore.toml` (`%APPDATA%\sshore\sshore.toml` on Windows)
- **Import** a pasted `ssh -L/-R/-D ...` command into rules
- **Live log panel**: in-memory ring buffer (1000 entries) of tunnel/SFTP events
- Uses system OpenSSH for auth (keys/agent/config), so password/keyboard-interactive
  auth is not supported in the GUI — configure key or agent auth for your hosts

## Architecture

- `internal/config` — parse `~/.ssh/config` (enumeration via kevinburke/ssh_config,
  authoritative fields via `ssh -G`) and read/write TOML config store
- `internal/forward` — spawn/manage long-lived `ssh -N` subprocesses, lifecycle state
  machine, port pre-check, error classification
- `internal/sftp` — one `sftp -b` process per operation, `ls -la` parsing
- `internal/importer` — tokenize `ssh -L/-R/-D` command lines into rules (inject-safe)
- `frontend/src` — Vue 3 UI (left-nav module switcher: Forward / SFTP) + Pinia log store

## Test

```bash
go test ./...          # Go subsystem tests (mocked ssh/sftp)
cd frontend && npx vitest run   # log store ring-buffer tests
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

`.github/workflows/ci.yml` runs on push/PR: `go vet`, Go tests (`-race`),
frontend tests + build, and `wails build` for Linux (webkit2gtk-4.1) and
Windows (icon via `.syso`), both UPX-compressed.
