#!/usr/bin/env bash
# E2E for sshore core: verifies ssh -G config resolution, -N -L forward, and sftp ls.
# Uses a throwaway local sshd in a temp dir. Run from repo root.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMPD="$(mktemp -d)"
PORT=22901
SOCK="$TMPD/sshd.sock"
# HOME 下面会指向临时目录；先把真实的 Go 缓存路径固定下来，否则 go 会把模块/编译缓存
# 写进 $TMPD（只读的模块缓存还会让 cleanup 的 rm -rf 刷 Permission denied）。
REAL_GOPATH="$(go env GOPATH)"
REAL_GOMODCACHE="$(go env GOMODCACHE)"
REAL_GOCACHE="$(go env GOCACHE)"
export HOME="$TMPD/home"
mkdir -p "$HOME/.ssh"

cleanup() {
  if [ -f "$SOCK" ]; then ssh -o BatchMode=yes -S "$SOCK" -O exit localhost -p "$PORT" 2>/dev/null || true; fi
  [ -n "${SSHDPID:-}" ] && kill "$SSHDPID" 2>/dev/null || true
  rm -rf "$TMPD"
}
trap cleanup EXIT

echo "== generating test keys =="
ssh-keygen -t ed25519 -N "" -f "$TMPD/hostkey" -q
ssh-keygen -t ed25519 -N "" -f "$TMPD/client_key" -q
cp "$TMPD/client_key.pub" "$HOME/.ssh/authorized_keys"
chmod 600 "$HOME/.ssh/authorized_keys"
mkdir -p "$TMPD/home"
echo "unused sshd lease dir" >/dev/null

echo "== starting sshd on :$PORT =="
HOME="$TMPD/home" /usr/sbin/sshd -D -f /dev/null \
  -p "$PORT" \
  -h "$TMPD/hostkey" \
  -o "AuthorizedKeysFile=$HOME/.ssh/authorized_keys" \
  -o "PasswordAuthentication no" \
  -o "StrictModes no" \
  -o "AllowTcpForwarding yes" \
  -o "Subsystem sftp internal-sftp" \
  -E "$TMPD/sshd.log" \
  -o "PidFile $TMPD/sshd.pid" &
SSHDPID=$!

# Wait for sshd to accept a real SSH connection (key-based readiness probe).
echo "== host alias config =="
cat > "$HOME/.ssh/config" <<CFG
Host e2e-test
  HostName 127.0.0.1
  Port $PORT
  User $(whoami)
  IdentityFile $TMPD/client_key
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
CFG
chmod 600 "$HOME/.ssh/config"

for i in $(seq 1 20); do
  if ssh -F "$HOME/.ssh/config" -o BatchMode=yes -o IdentitiesOnly=yes -o ConnectTimeout=2 e2e-test 'echo READY' 2>/dev/null | grep -q READY; then break; fi
  sleep 0.3
done

echo "== TEST 1: ssh -G resolves alias =="
G_OUT="$(ssh -G -F "$HOME/.ssh/config" e2e-test 2>&1 || true)"
echo "--- ssh -G output (grep hostname/port) ---"
echo "$G_OUT" | grep -iE "hostname|^port" || echo "(no hostname/port lines)"
echo "$G_OUT" | grep -q "hostname 127.0.0.1" && echo "PASS: ssh -G resolves hostname" || { echo "FAIL: ssh -G did not resolve hostname 127.0.0.1"; exit 1; }

echo "== TEST 2: -N -L forward authorizes and binds =="
# verbose diagnostic to a log first
ssh -v -N -o BatchMode=yes -o StrictHostKeyChecking=no -o IdentitiesOnly=yes \
  -F "$HOME/.ssh/config" \
  -o "UserKnownHostsFile /dev/null" \
  -o "ExitOnForwardFailure=yes" \
  -L "22990:127.0.0.1:22902" e2e-test 2>"$TMPD/fwd_verbose.log" &
FWD_PID=$!
sleep 1.5
if ! kill -0 "$FWD_PID" 2>/dev/null; then
  echo "--- forward SSH verbose (death cause) ---"
  cat "$TMPD/fwd_verbose.log"
  echo "--- sshd.log ---"
  tail -20 "$TMPD/sshd.log" || true
  echo "FAIL: forward process died"
  exit 1
fi
python3 - <<'PY' || { echo "FAIL: forward did not bind"; kill "$FWD_PID" 2>/dev/null; exit 1; }
import socket
s = socket.socket()
try:
    s.settimeout(2)
    s.connect(("127.0.0.1", 22990))
    print("PASS: local forward port bound")
except Exception as e:
    print("FAIL: forward bind:", e)
    raise
finally:
    s.close()
PY
kill "$FWD_PID" 2>/dev/null || true

echo "== TEST 3: sftp ls -l works (via -b) =="
mkdir -p "$TMPD/home/remote_dir"
echo "hello" > "$TMPD/home/remote_dir/a.txt"
BATCH="$TMPD/sftp.bat"
printf "ls -l %s\n" "$TMPD/home/remote_dir" > "$BATCH"
sftp_out="$(sftp -o BatchMode=yes -o StrictHostKeyChecking=no -o IdentitiesOnly=yes -F "$HOME/.ssh/config" \
  -o "UserKnownHostsFile /dev/null" -b "$BATCH" e2e-test 2>&1 || true)"
echo "--- sftp output ---"
echo "$sftp_out"
echo "$sftp_out" | grep -q "a.txt" && echo "PASS: sftp ls lists file" || { echo "FAIL: sftp ls"; exit 1; }

echo "== sync e2e (Go side) =="
REMOTE_DIR="$TMPD/remote-conf"
mkdir -p "$REMOTE_DIR"
echo "v1" > "$REMOTE_DIR/app.conf"
# 注意：别名只写进 $TMPD 下的临时配置，绝不碰真实 ~/.ssh/config。
cat > "$HOME/.ssh/config" <<EOF
Host sshore-e2e
  HostName 127.0.0.1
  Port $PORT
  User $(id -un)
  IdentityFile $TMPD/client_key
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
EOF
chmod 600 "$HOME/.ssh/config"
# OpenSSH 的默认用户配置取自 passwd 家目录，而不是 $HOME：即使把 HOME 指向临时目录，
# go 测试里的 ssh/sftp 仍会去读真实 ~/.ssh/config，找不到 sshore-e2e。这里生成只作用于
# 本次 go test 的 ssh/sftp 垫片，用 -F 指向临时配置（垫片在 $TMPD 内，PATH 仅本命令前置）。
SHIM="$TMPD/shim"
mkdir -p "$SHIM"
cat > "$SHIM/ssh" <<SHIMSH
#!/usr/bin/env bash
exec /usr/bin/ssh -F "$HOME/.ssh/config" -o IdentitiesOnly=yes "\$@"
SHIMSH
cat > "$SHIM/sftp" <<SHIMSF
#!/usr/bin/env bash
exec /usr/bin/sftp -F "$HOME/.ssh/config" -o IdentitiesOnly=yes "\$@"
SHIMSF
chmod +x "$SHIM/ssh" "$SHIM/sftp"
if PATH="$SHIM:$PATH" \
   GOPATH="$REAL_GOPATH" GOMODCACHE="$REAL_GOMODCACHE" GOCACHE="$REAL_GOCACHE" \
   SSHORE_E2E_HOST=sshore-e2e SSHORE_E2E_REMOTE="$REMOTE_DIR" \
   HOME="$HOME" go test ./internal/sync/ -run TestSyncE2E -count=1 -v; then
  echo "sync e2e OK"
else
  echo "sync e2e FAILED" >&2
  exit 1
fi

echo "== ALL E2E TESTS PASSED =="
