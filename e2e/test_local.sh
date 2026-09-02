#!/usr/bin/env bash
# E2E for sshore core: verifies ssh -G config resolution, -N -L forward, and sftp ls.
# Uses a throwaway local sshd in a temp dir. Run from repo root.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMPD="$(mktemp -d)"
PORT=22901
SOCK="$TMPD/sshd.sock"
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
  if ssh -F "$HOME/.ssh/config" -o BatchMode=yes -o ConnectTimeout=2 e2e-test 'echo READY' 2>/dev/null | grep -q READY; then break; fi
  sleep 0.3
done

echo "== TEST 1: ssh -G resolves alias =="
G_OUT="$(ssh -G -F "$HOME/.ssh/config" e2e-test 2>&1 || true)"
echo "--- ssh -G output (grep hostname/port) ---"
echo "$G_OUT" | grep -iE "hostname|^port" || echo "(no hostname/port lines)"
echo "$G_OUT" | grep -q "hostname 127.0.0.1" && echo "PASS: ssh -G resolves hostname" || { echo "FAIL: ssh -G did not resolve hostname 127.0.0.1"; exit 1; }

echo "== TEST 2: -N -L forward authorizes and binds =="
# verbose diagnostic to a log first
ssh -v -N -o BatchMode=yes -o StrictHostKeyChecking=no \
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
sftp_out="$(sftp -o BatchMode=yes -o StrictHostKeyChecking=no -F "$HOME/.ssh/config" \
  -o "UserKnownHostsFile /dev/null" -b "$BATCH" e2e-test 2>&1 || true)"
echo "--- sftp output ---"
echo "$sftp_out"
echo "$sftp_out" | grep -q "a.txt" && echo "PASS: sftp ls lists file" || { echo "FAIL: sftp ls"; exit 1; }

echo "== ALL E2E TESTS PASSED =="
