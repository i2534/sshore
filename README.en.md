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
- OpenSSH (`ssh`) on `PATH` (the default `gosftp` transport no longer needs the `sftp`
  binary; only the `sftp_transport = "batch"` fallback does)
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

## Updates & upgrades

sshore polls the **update source** for the latest Release at the configured interval and lights a
badge on the sidebar "⚙ Settings" button when a newer version exists; checking, downloading and
verification progress are all visible under Settings → Updates.

### Update source and verification

- The default source is the GitHub API: `https://api.github.com/repos/i2534/sshore/releases/latest`
  (the configurable source is `https://api.github.com/repos/i2534/sshore`; the app requests
  `<source>/releases/latest`).
- Settings → Updates lets you point the source at a mirror/intranet address (`update_source`), but it
  must expose a **GitHub-compatible API** (the same `<source>/releases/latest` JSON).
- **Non-local addresses must use `https://`**; only loopback (`127.0.0.1` / `localhost` / `::1`) may
  use `http://` (for offline mirrors and end-to-end self-tests). A non-loopback `http://` source falls
  back to the default source (the service logs one line); the settings field keeps showing "Non-local
  addresses must use https." for anything still not `https://` and not a loopback `http://`. A source
  without an `http(s)://` prefix is normalized to empty on load and also falls back to the default.
- Check timing: **one automatic check after launch** + "Check now" in Settings + polling every
  `update_check_interval_hours` hours (default 12; 12/24/48/168 selectable; `0` = polling off while
  manual checks keep working). Unchecking "Check for updates automatically" stops automatic requests.
- **Dev / non-release builds are excluded from automatic checks**: when `Version` is `dev` or a
  `git describe` string (e.g. `v0.6.0-80-gc2d2a36`) it is not comparable to a Release tag, so the
  automatic check is skipped (state stays "not checked yet") and Settings labels it "local build …
  (dev)"; manual "Check now" still works.
- Verification strength: releases ship a `checksums.txt` (SHA256) compared after download; **a mismatch
  refuses installation**, and temporary/partial files never land in the program directory. A verified
  binary is written together with a hash sidecar (`sshore.<target-version>[.exe].sha256`) and re-hashed
  once more before "Restart and upgrade".
- What it protects against: transfer corruption, truncated downloads, mismatched assets. It does **not**
  protect against a compromised update source, or a tampered release published after the GitHub account
  was stolen — `checksums.txt` and the archive share the same origin, so HTTPS and upstream account
  security are the only defence for that class.
- If the Release has no package for this platform, the app says "no package available for this platform"
  and offers "Open release page" for a manual download.

### Upgrade flow (requires one click from you)

1. When a newer version exists, click "Download update": download the archive → compare the SHA256 from
   `checksums.txt` → unpack the pending file (same directory as the current binary, named after the
   **target version**, e.g. `sshore.v0.7.0` / `sshore.v0.7.0.exe`) → write the matching `.sha256`.
   Unpacking only accepts the whitelisted `sshore` / `sshore.exe` and rejects symlinks and path traversal.
2. Click "Restart and upgrade": re-hash, write a one-shot upgrade script, start it detached, then quit.
3. The script waits for the old process to exit → backs up the current binary as `sshore.<current-version>`
   (**only the most recent backup is kept**) → renames the pending file into place → starts the new
   version; if it fails to start, the script **rolls back automatically** and keeps the pending file.
4. On success it deletes the script and log; on failure it keeps `sshore-update.log`, and the next launch
   shows it under Settings → Updates (truncated to 2000 chars) with "last upgrade did not finish"; the
   log's last line is `RESULT=fail:<step>`, and "Open release page" is offered for a manual replacement.

Upgrade-related files all live in `<ExeDir>` (the binary's directory):

| File | Naming |
| --- | --- |
| Pending file (with **target version**) | `sshore.<target-version>` (Windows: `sshore.<target-version>.exe`) |
| Pending verification sidecar | `sshore.<target-version>[.exe].sha256` |
| Backup (with **current version**, only the most recent one kept) | `sshore.<current-version>` (dev builds: `sshore.dev-<timestamp>`) |
| Upgrade log | `sshore-update.log` |
| Upgrade lock | `.sshore-update.lock` (Linux; Windows uses a named mutex, no file) |

The lock file is only the carrier for a kernel lock and is released when the process exits — it **must
not be deleted manually** (deleting it invites two instances upgrading at once and overwriting each
other's binary).

### Privacy

A check sends exactly one HTTPS request bearing a version-string User-Agent to the update source; no
machine information is reported. After unchecking "Check for updates automatically", a request is sent
only when you click "Check now". With a non-default update source, "Restart and upgrade" asks for
confirmation once more.

### Manual upgrade (no script)

```bash
# Linux: unpack and overwrite in the directory holding sshore (directories are created first
# so the block is paste-ready)
mkdir -p /tmp/upd ~/bin
tar -xzf sshore-v0.7.0-linux-amd64.tar.gz -C /tmp/upd
install -m 0755 /tmp/upd/sshore ~/bin/sshore
```

```powershell
# Windows: quit sshore, then overwrite the old sshore.exe with the unpacked one
```

If the install directory is not writable (e.g. `/usr/bin`, `Program Files`), Settings shows "install
directory is not writable, please upgrade manually (you can open the release page to download)" — the
exact steps are the manual commands above.

### Antivirus false positives

Self-upgrade "downloads an executable from the network and replaces itself", which overlaps with malware
behaviour; releases are also UPX-compressed (see "Size optimization" — UPX itself is frequently flagged)
and the current binaries are **not code-signed**. If your AV (Windows Defender, 360, Huorong, …) blocks
the download, the replacement or the new version's start-up:

1. allow-list just `sshore` / `sshore.exe` and the upgrade script as narrowly as your AV allows (no need
   to allow the whole `<ExeDir>` directory, which would also allow anything later dropped there), then retry;
2. if it is still blocked, replace the binary manually with the **manual upgrade** steps above;
3. or build a non-UPX version yourself with `make both COMPRESS=0`.

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
  sftp_transport = ""            # ""/auto (= gosftp built-in default) | "gosftp" | "batch" (legacy fallback)

[[tunnels]]                      # port-forward rules (-L / -R / -D)
[[syncs]]                        # remote → local sync rules
[[bookmarks]]                    # pinned locations (scope = local | remote)
[[local_recent]]                 # local recent locations (recorded automatically, max 20)
[[remote_recent]]                # remote recent locations (isolated per host, max 20)
[[recent_sftp]]                  # legacy field: migrated on first launch into the two above; still parsed for downgrades
```

## SFTP transport (`sftp_transport`)

Two SFTP implementations can be selected in Settings or in the config. Connections and
authentication always stay with the system `ssh` (keys / ssh-agent / `~/.ssh/config` /
jump hosts); only who drives the transfer and directory listing differs:

- `gosftp` (**default**) — a long-lived `ssh -s <host> sftp` subsystem driven directly by
  `pkg/sftp`; **a transfer takes an exclusive, freshly dialed session (concurrency 1)** while
  **list/change operations reuse an idle session (at most 2 per host)**, with real byte-level
  progress, whole-batch cancel, retry and `.part` resume.
- `batch` (compat, kept for one release) — one `sftp -b` process per operation parsing
  `ls -la` text; no progress and no resume, i.e. the pre-upgrade behaviour.

**Precedence: `SSHORE_SFTP_TRANSPORT` env > `[app] sftp_transport` config > built-in default
(`gosftp`).** Invalid values never block startup: an invalid **env** value is ignored and the
config value is consulted next; an invalid **config** value (including `""` / `auto`) is
normalized on load (`Normalize` in `internal/config/store.go`) and finally falls back to the
built-in default (`resolveTransport` in `internal/sftp/backend.go`).

```bash
SSHORE_SFTP_TRANSPORT=batch sshore    # temporarily fall back to the legacy backend
SSHORE_SFTP_TRANSPORT=gosftp sshore   # explicitly select the new backend
```

- Settings → Transport → "SFTP transport backend" writes the `sftp_transport` key
  (`""`/`auto` = built-in default, i.e. `gosftp`).
- To **revert the whole default** (e.g. if on-machine acceptance fails), change
  `const defaultTransport = KindGo` back to `KindBatch` in `internal/sftp/backend.go` and
  rebuild; the two explicit overrides above are unaffected.
- The `batch` backend and the switch are scheduled for removal in **v0.8**.

### Progress, cancel and resume (`gosftp` only)

- **Progress** comes from real transferred bytes (throttled frames plus a forced final frame):
  per-file percentage, speed and ETA; trees are enumerated then counted file by file, and
  degrade to "N files done" when enumeration exceeds its budget.
- **Cancel is whole-batch**: it cancels the in-flight item (closing the session makes the
  in-flight IO fail at once, so no half-written target name ever appears) and marks the rest of
  the batch as "已取消，停止后续项". Cancel reports three honest states instead of pretending it
  succeeded: (1) `Cancel` returns `true` and the item ends in failure ⇒ final state "取消";
  (2) `Cancel` returns `false` (already committed / not in flight / never started) while the item
  is still running ⇒ the running row shows "未能取消"; if that item later fails, the final state
  is "失败" with the original error; (3) a cancel was requested but the item finally **committed**
  ⇒ final state "完成" with the row note "取消过晚（已完成）", and the target file is kept.
- **Retry** re-runs a failed item; **resume** reuses the `.part` kept from the failure and
  continues from the bytes already on disk. Resume first re-checks the source size/mtime
  fingerprint: if the source changed between attempts it re-transfers in full instead of
  splicing a corrupt file (and a `.part` already as large as the source is committed directly).
- Transfers are mutually exclusive per target (host + user + direction + target): a duplicate
  fails immediately instead of being silently serialized and overwriting.

### Internal temp names (hidden from panes / ignored by sync)

Every atomic-commit temp name carries the `.sshore-sftppart-` infix (single source of truth:
`internal/sftp.PartMarker`):

- normal: `<name>.sshore-sftppart-<id8>-<rand>` (same directory as the target)
- backup (backup-swap when `posix-rename` is unavailable): `<name>.sshore-sftppart-bak-<rand>`
- long-name degradation (original over 200 bytes, to avoid `ENAMETOOLONG`):
  `.sshore-sftppart-<id8>-<rand>` / `.sshore-sftppart-bak-<rand>`

These names **never show up in the file panes** (frontend filters on the infix) and are
**ignored by sync / change watching** (`watch.IsInternalTemp` + `sync.inScope`), so they are
never synced out or downloaded as new files.

### Known limitations (not softened)

- **Must run in an interactive desktop session**: in Session 0 (Windows service / non-interactive
  session) the `ssh.exe` this app spawns hangs after the SFTP INIT (observed ≥15–25s; **no**
  experiment was run on whether a longer wait would recover). This is an observation on **this
  machine's mixed install** (client 9.5p1 + the only server sshd 9.2p1; no second sshd was
  available as a control): all four tested console-creation flags were ineffective, window
  station / desktop was untested, and it is not a general claim about other Windows / OpenSSH
  combinations. Conclusion: **do not start it as a service**; even a scheduled task needs `/it`
  to land in the interactive session.
- **An over-budget tree scan transfers only the enumerated prefix**: when a single tree scan
  exceeds **20000 files** or a **5s** budget it stops enumerating and transfers only what it
  enumerated; the call returns **success** and signals the unknown denominator with `-1`
  (`total`/`filesTotal` negative) — i.e. "transfer succeeded" does not mean "the whole tree
  was transferred".
- **The `Atomic=false` path writes directly, with no `.part` and no journal**: the legacy
  four-argument `Get/Put` and the legacy tree face `GetRecursive/PutRecursive` (including
  `internal/sync`'s use) both write the target directly, produce no resumable `.part` and write
  no backup-swap journal; atomicity is left to sync (its own `.part` + rename).
- **The temp-file paths are trusted to be private**: the `.part` / `.sshore-sftppart-bak-` paths
  (local or remote) assume only this app writes them; **other processes on the same machine are
  not defended against** if they rewrite/truncate them mid-transfer. The only commit precondition
  is the byte-count guard `done == total` (`decideCommit` in `internal/sftp/copy.go`), which
  cannot see a **sparse hole** — e.g. seeking past EOF is zero-filled on the server while the
  count still matches, so a holed file may be committed as the final file. Do not treat
  multi-user-writable / shared directories as trusted transfer targets.
- **Tree transfers dial before enumerating**: `GetTree` / `PutTree` take the single transfer
  session (`AcquireTransfer`, i.e. one fresh handshake) *before* enumerating, so the whole scan
  window holds the transfer concurrency slot. The scan phase is not session-IO-free: `GetTree`
  enumerates the remote side through `s.Conn.ReadDirContext` (`scanTree` in
  `internal/sftp/copy.go`), cancelling via ctx; only `PutTree`'s local `WalkDir` touches no
  session. But **an instantly cancelled scan has already paid one handshake**, and no subsequent
  transfer can start while it scans.
- **Stale `.part` cleanup only scans the configured LocalRecent directories**: at startup only
  recently used local directories are cleaned (temp files older than 7 days); there is no
  whole-disk index, so orphaned `.part` files elsewhere are left for manual cleanup.
- **`RemoveRecursive` has hard limits**: trees deeper than 64 levels or larger than 100000
  entries are rejected outright (during collection, so nothing is half-deleted), with **no
  override switch**.
- **`Connected` is a sticky connection intent, not a liveness probe**: it stays true after one
  successful handshake until an explicit `Disconnect`/`CloseAll`; evicting or closing pooled
  sessions never flips it. Do not use it as a liveness check.
- **Multi-host journal recovery only acts on reachable hosts**: entries are grouped by
  `(host, user)` and each group only probes its own host; an unreachable group is kept for the
  next attempt, and **legacy entries without a host are never touched or deleted**.
- **No recovery at startup**: journal (backup-swap) recovery runs on the **graceful exit** path;
  interrupted commits after a crash / power loss converge on the next graceful exit.
- **5s shutdown grace**: exit waits at most 5s for in-flight transfers, then force-cancels and
  closes sessions. A transfer stuck in an uninterruptible local write may still finish after
  `CloseAll` returns (it writes no journal intent, so recovery sees an unambiguous state); an
  extreme ordering also has a narrow window that escapes both `CloseAll` pool snapshots
  (low probability).

## Architecture

- `internal/config` — parse `~/.ssh/config` (enumeration via kevinburke/ssh_config,
  authoritative fields via `ssh -G`) and read/write the atomic TOML config store
- `internal/forward` — spawn/manage long-lived `ssh -N` subprocesses, lifecycle state
  machine, port pre-check, error classification, auto-reconnect backoff
- `internal/sftp` — facade + two backends (default `gosftp` = `pkg/sftp` long-lived
  session pool, `batch` = the legacy `sftp -b` process), protocol driven by `pkg/sftp`
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

**When running the e2e tests directly on Windows (or any machine), note** that the e2e tests in
`internal/sftp` and `internal/sync` now require all **three** variables:
`SSHORE_E2E_HOST` / `SSHORE_E2E_REMOTE` / `SSHORE_SFTP_TRANSPORT`. Specifically:

- missing `SSHORE_E2E_HOST` / `SSHORE_E2E_REMOTE` ⇒ **Skip** (so `go test ./...` stays silent
  without a network or sshd);
- `SSHORE_E2E_HOST` set but `SSHORE_SFTP_TRANSPORT` missing ⇒ a deliberate `t.Fatalf`
  (`SSHORE_SFTP_TRANSPORT 未送达：后端身份无法证明`) — an anti-false-green guard, not a bug:
  if both iterations silently ran the same backend, the "backend matrix" would be fake.

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
