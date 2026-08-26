# sshkit

Cross-platform SSH port-forward + SFTP manager. Built with Wails v2 (Go) + Vue 3.

## Requirements

- Go 1.26
- Node.js 20+
- OpenSSH (`ssh`, `sftp`) on `PATH`
- wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)

## Build

```bash
wails build
```

## Run (dev)

```bash
wails dev
```

## Features

- **SSH port forwarding**: local `-L`, remote `-R`, dynamic SOCKS `-D`, jump host `-J`
- **SFTP file management**: browse / upload / download / delete / rename / mkdir
- **Config**: reads `~/.ssh/config` read-only (no credential storage); tunnel rules stored in
  `~/.config/sshkit/sshkit.toml` (`%APPDATA%\sshkit\sshkit.toml` on Windows)
- **Import** a pasted `ssh -L/-R/-D ...` command into rules
- **Live log panel**: in-memory ring buffer (1000 entries) of tunnel/SFTP events
- Uses system OpenSSH for auth (keys/agent/config), so password/keyboard-interactive
  auth is not supported in the GUI — configure key or agent auth for your hosts

## Architecture

- `internal/config` — parse `~/.ssh/config` (enumeration via kevinburke/ssh_config,
  authoritative fields via `ssh -G`) and read/write TOML config store
- `internal/forward` — spawn/manage long-lived `ssh -N` subprocesses, lifecycle state
  machine, port pre-check, error classification
- `internal/sftp` — one `sftp -b` process per operation, `ls -l` parsing
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
sshkit relies on: `ssh -G` alias resolution, `-N -L` local forward binding, and
`sftp ls -l` output parsing. Requires `/usr/sbin/sshd`, `ssh-keygen`, and `python3`.

## CI

`.github/workflows/ci.yml` runs on push/PR: `go vet`, Go tests (`-race`),
frontend tests + build, and a Linux `wails build` (webkit2gtk-4.1).
