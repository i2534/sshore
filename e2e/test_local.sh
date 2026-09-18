#!/usr/bin/env bash
# E2E for sshore core: verifies ssh -G config resolution, -N -L forward, and sftp ls.
# Uses a throwaway local sshd in a temp dir. Run from repo root.
set -euo pipefail

usage() {
  cat <<'USAGE'
用法: bash e2e/test_local.sh [-h|--help]

环境变量:
  E2E_RUN  逗号分隔的**用例全名**列表（不是正则，也不接受 | 等正则元字符）。
           每个名字都必须在该后端的 go test 输出里出现 "--- PASS: <名字>"，
           少一个就判失败；防线 1/2（有 SKIP / 一个 PASS 都没有）同时生效。
           未设置时使用脚本内置的完整期望名单：TestGoBackendE2E,TestCancelWholeBatchE2E,TestResumeE2E
           （TestSyncE2E 仍未进默认名单：它的 gosftp 迭代走 GoBackend.ListMany，
            该方法是 Task 13 的桩；Task 8 已补齐 Put，但 List 未落地前追加必红）
           例：E2E_RUN='TestGoBackendE2E,TestCancelWholeBatchE2E,TestResumeE2E' bash e2e/test_local.sh
USAGE
}
case "${1:-}" in
  -h|--help) usage; exit 0 ;;
esac

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

# --- E2E_RUN 校验（必须在起 sshd/生成密钥之前，失败要快速且无残留）---
# E2E_RUN：逗号分隔的**用例全名**列表（不是正则）。未设置时用内置完整期望名单 ——
# 之前这里直接引用 $E2E_RUN，在 set -u 下 make e2e 会 "unbound variable" 直接死掉（I1）。
# 每个名字都走防线 3 逐字比对，少一个就判失败（I3：名单里任何一个缺失都算失败）。
# 默认名单 = 当前**两个后端都真能跑绿**的完整期望集合。
# 为什么不直接照抄 plan 的 TestGoBackendE2E,TestSyncE2E：TestSyncE2E 走 sync → Ctrl → backend()
# → 当前选中的后端。Task 8 已实现 GoBackend.Put（上传），但 GoBackend.ListMany 仍是
# 「未实现」桩（Task 13），实测 E2E_RUN=TestSyncE2E 时 batch 迭代 PASS、gosftp 迭代
# 15s 超时失败，所以此刻把它放进默认名单会让 make e2e 变红。等 Task 13 的
# List/ListMany 落地、两个后端都绿之后，再把 TestSyncE2E 追加进这一行。
# Task 10：追加 TestCancelWholeBatchE2E —— 它取消的是 GoBackend 直连的在飞传输（与所选
# 后端无关，两个迭代都真跑绿），并额外断言 batch 迭代下 Ctrl.Cancel 诚实返回 false。
# Task 11：追加 TestResumeE2E —— GoBackend 直连的真实 .part 续传 + 同尺寸改写拒绝 +
# 同目标去重（两个迭代都真跑），batch 迭代额外断言其 Atomic 被硬拒（绝不冒充「batch 支持续传」）。
E2E_DEFAULT_LIST='TestGoBackendE2E,TestCancelWholeBatchE2E,TestResumeE2E'
E2E_RUN="${E2E_RUN:-$E2E_DEFAULT_LIST}"

# 首/尾逗号会被 read -a 折叠掉（"A," 拆成 [A]，不是 [A,""]），这里显式拒绝，
# 免得「名单少写一个名字却看不出来」；中间的空项（",,"）仍由下面的遍历兜住。
if [[ "$E2E_RUN" == ,* || "$E2E_RUN" == *, ]]; then
  echo "FAIL: E2E_RUN 名单不得以逗号开头或结尾：'$E2E_RUN'" >&2
  exit 2
fi
IFS=',' read -r -a E2E_NAMES <<< "$E2E_RUN"
if [ "${#E2E_NAMES[@]}" -eq 0 ]; then
  echo "FAIL: E2E_RUN 为空：需要至少一个用例全名" >&2
  exit 2
fi
# 若允许 ERE，用户传 "." 会把「任一用例 PASS」悄悄变成「目标用例 PASS」（Task 6 重审 I2）。
# 故逐字拒绝正则元字符与 /。不用 case 的括号类：bash 对括号类里的 ( ) 有配对歧义，
# 实测 *[][.\*?+^$(){}|/]* 会把 "." 判成合法。
E2E_BAD_CHARS='][.*?+^$(){}|/\\'
for _name in "${E2E_NAMES[@]}"; do
  if [ -z "$_name" ]; then
    echo "FAIL: E2E_RUN 名单里含空项：'$E2E_RUN'" >&2
    exit 2
  fi
  for (( _i=0; _i<${#E2E_BAD_CHARS}; _i++ )); do
    _c="${E2E_BAD_CHARS:_i:1}"
    if [[ "$_name" == *"$_c"* ]]; then
      echo "FAIL: E2E_RUN 只接受用例全名（不得含正则元字符或 /）：'$_name'" >&2
      exit 2
    fi
  done
done
unset _i _c _name

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

echo "== PROBE A/B/C: sftp 语义（供 spec R1/R2/R9 取值） =="
PB="$TMPD/home/probe"
SFTP_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=no -o IdentitiesOnly=yes
  -F "$HOME/.ssh/config" -o "UserKnownHostsFile /dev/null")
run_batch() { sftp "${SFTP_OPTS[@]}" -b "$1" e2e-test 2>&1 || true; }

# --- PROBE A: put -r 目标同名目录已存在：并入还是嵌套？ ---
rm -rf "$PB"; mkdir -p "$PB/localdir/sub" "$PB/remote/localdir"
echo A > "$PB/localdir/a.txt"
echo BB > "$PB/localdir/sub/b.txt"
echo EXISTING > "$PB/remote/localdir/existing.txt"
echo LOCAL_ONLY > "$PB/remote/localdir/a.txt"
printf 'put -r %s %s\n' "$PB/localdir" "$PB/remote" > "$PB/pa.bat"
PA_OUT="$(run_batch "$PB/pa.bat")"
echo "--- probe A output ---"; echo "$PA_OUT"
if [ -d "$PB/remote/localdir/localdir" ]; then
  echo "PUT-R: nest"
elif [ -f "$PB/remote/localdir/a.txt" ] && [ -f "$PB/remote/localdir/sub/b.txt" ]; then
  echo "PUT-R: merge"
  echo "PUT-R-OVERWRITE: $(cat "$PB/remote/localdir/a.txt")"
else
  echo "PUT-R: error-or-other"
fi

# --- PROBE B: rename 覆盖已存在目标 ---
printf 'OLD' > "$PB/r_old.txt"; printf 'NEW' > "$PB/r_new.txt"
printf 'rename %s %s\n' "$PB/r_old.txt" "$PB/r_new.txt" > "$PB/pb.bat"
PB_OUT="$(run_batch "$PB/pb.bat")"
echo "--- probe B output ---"; echo "$PB_OUT"
if [ -f "$PB/r_new.txt" ]; then echo "RENAME-OVERWRITE: yes content=$(cat "$PB/r_new.txt")"; else echo "RENAME-OVERWRITE: target-gone"; fi
[ -f "$PB/r_old.txt" ] && echo "RENAME-SOURCE: still-exists" || echo "RENAME-SOURCE: moved"

# --- PROBE C: rm 路径含 glob 元字符是否被远端展开 ---
mkdir -p "$PB/glob"
echo ONE > "$PB/glob/lit*name.txt"
echo TWO > "$PB/glob/litZZname.txt"
printf 'rm %s\n' "$PB/glob/lit*name.txt" > "$PB/pc.bat"
PC_OUT="$(run_batch "$PB/pc.bat")"
echo "--- probe C output ---"; echo "$PC_OUT"
echo "GLOB-RM: star-file=$([ -f "$PB/glob/lit*name.txt" ] && echo kept || echo deleted) zz-file=$([ -f "$PB/glob/litZZname.txt" ] && echo kept || echo deleted)"

echo "== PROBE D: 真机 stderr 形状回归（T8 的 failedDeletePaths 依赖它） =="
# 背景：T8 最初的测试夹具是手写的 Can't rm: "<path>"，与真实 OpenSSH 不符——
# 真机上 '-' 前缀下 -rm 失败**不带引号**（remote delete <path>: ...），只有 -rmdir 失败带引号。
# 那段误判曾让 -rm 失败被当成整批成功（Critical）。这里用同一次 sshd 把两种形状钉死：
# 形状一变，本节立刻 FAIL 并打印实际输出，避免再次只靠"评审期一次性证据"。
SHAPE_FAIL=0
rm -rf "$PB/shape"; mkdir -p "$PB/shape/ro" "$PB/shape/nonempty"
echo X > "$PB/shape/ro/f.txt"
echo Y > "$PB/shape/nonempty/child.txt"

# 形状 1：把父目录设为不可写 ⇒ '-rm' 失败（unlink 需要父目录写权限）
chmod 555 "$PB/shape/ro"
printf -- '-rm %s\n' "$PB/shape/ro/f.txt" > "$PB/pd1.bat"
PD1_OUT="$(run_batch "$PB/pd1.bat")"
chmod 755 "$PB/shape/ro" # 立刻恢复，否则 cleanup 的 rm -rf 会因权限失败
echo "--- probe D1 output (expect: remote delete <path> … 不带引号) ---"; echo "$PD1_OUT"
if printf '%s' "$PD1_OUT" | grep -qF "remote delete $PB/shape/ro/f.txt"; then
  echo "RM-STDERR-SHAPE: ok"
else
  echo "RM-STDERR-SHAPE: FAIL（真机 -rm 失败文案不再是 'remote delete <path>'，T8 的 failedDeletePaths 需同步）"
  SHAPE_FAIL=1
fi
if printf '%s' "$PD1_OUT" | grep -qF "\"$PB/shape/ro/f.txt\""; then
  echo "RM-STDERR-QUOTING: FAIL（-rm 失败本该**不带引号**；若变成带引号，T8 的两种形状判定需重新评估）"
  SHAPE_FAIL=1
else
  echo "RM-STDERR-QUOTING: ok (unquoted)"
fi

# 形状 2：对非空目录执行 '-rmdir' ⇒ 失败，且文案**自带双引号**
printf -- '-rmdir %s\n' "$PB/shape/nonempty" > "$PB/pd2.bat"
PD2_OUT="$(run_batch "$PB/pd2.bat")"
echo "--- probe D2 output (expect: remote rmdir \"<path>\": Failure) ---"; echo "$PD2_OUT"
if printf '%s' "$PD2_OUT" | grep -qF "remote rmdir \"$PB/shape/nonempty\""; then
  echo "RMDIR-STDERR-SHAPE: ok"
else
  echo "RMDIR-STDERR-SHAPE: FAIL（真机 -rmdir 失败文案不再是 'remote rmdir \"<path>\"'，T8 的 failedDeletePaths 需同步）"
  SHAPE_FAIL=1
fi

if [ "$SHAPE_FAIL" -ne 0 ]; then
  echo "FAIL: sftp 失败 stderr 形状与 internal/sftp 的 failedDeletePaths 解析不一致" >&2
  exit 1
fi

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

# 双后端循环：每次迭代显式导出 SSHORE_SFTP_TRANSPORT，并保留 SSHORE_E2E_*、PATH 垫片、
# GOPATH/GOMODCACHE/GOCACHE。漏掉任一项都会「别名解析不到 → 测试全 skip → 假绿」。
#
# E2E_RUN 名单里的每个用例**逐个**用 -run '^NAME$' 精确跑：绝不把名单拼进 -run 正则
# （名字里的 | 会被 go test 当正则，静默跑到名单外的用例上）。每次 go test 的 stdout 与
# stderr 分文件收集，防线只读 stdout（--- PASS / SKIP 行），编译错误留在 stderr 里可读。
E2E_FAIL=0
for backend in batch gosftp; do
  echo "--- backend=$backend run=$E2E_RUN ---"
  OUT=""
  for _name in "${E2E_NAMES[@]}"; do
    printf '>>> -run ^%s$ (backend=%s)\n' "$_name" "$backend"
    _iter_out="$(mktemp)"; _iter_err="$(mktemp)"
    if PATH="$SHIM:$PATH" \
         GOPATH="$REAL_GOPATH" GOMODCACHE="$REAL_GOMODCACHE" GOCACHE="$REAL_GOCACHE" \
         SSHORE_E2E_HOST=sshore-e2e SSHORE_E2E_REMOTE="$REMOTE_DIR" \
         SSHORE_SFTP_TRANSPORT="$backend" \
         HOME="$HOME" go test ./internal/sftp/ ./internal/sync/ -run "^$_name\$" -count=1 -v >"$_iter_out" 2>"$_iter_err"; then
      :
    else
      E2E_FAIL=1
    fi
    cat "$_iter_out"
    if [ -s "$_iter_err" ]; then cat "$_iter_err" >&2; fi
    OUT="$OUT$(cat "$_iter_out")"$'\n'
    rm -f "$_iter_out" "$_iter_err"
  done
  # 假绿防线 1：任何用例 SKIP 都说明环境没送达（缺 env / 垫片失效），必须失败。
  if printf '%s\n' "$OUT" | grep -q '^--- SKIP'; then
    echo "FAIL: backend=$backend 有用例被 SKIP（SSHORE_E2E_* 或 PATH 垫片没生效）—— 假绿，不接受" >&2
    E2E_FAIL=1
  fi
  # 假绿防线 2：该后端必须至少真正跑过并 PASS 一个用例，否则 -run 打空/编译失败也会是 0。
  if ! printf '%s\n' "$OUT" | grep -q '^--- PASS'; then
    echo "FAIL: backend=$backend 没有任何用例真正通过（-run 未匹配 / 编译失败）" >&2
    E2E_FAIL=1
  fi
  # 假绿防线 3（I2/I3，Task 6 重审 + Task 7 评审）：**名单里的每一个**名字都必须出现
  # "--- PASS: <name>"。用 grep -qxF **逐字精确**比对（-x 整行、-F 字面量、无 -E）：
  # Task 6 原版是 -qE 非锚定 ERE，重审已证明「目标用例改名 + 一个名字含同正则的空用例」
  # 能让 harness 假绿；只比对名单第一项则会让「名单里有名字缺失」蒙混过关。
  PASSED_NAMES="$(printf '%s\n' "$OUT" | sed -n 's/^--- PASS: \([^ ]*\).*/\1/p')"
  for _name in "${E2E_NAMES[@]}"; do
    if [ -z "$PASSED_NAMES" ] || ! printf '%s\n' "$PASSED_NAMES" | grep -qxF -- "$_name"; then
      echo "FAIL: backend=$backend 没有名字**精确等于** E2E_RUN 名单项 '$_name' 的用例真正 PASS（目标用例被改名 / 被 -run 漏掉）" >&2
      E2E_FAIL=1
    fi
  done
done
unset _name
[ "$E2E_FAIL" -eq 0 ] || exit 1

echo "== ALL E2E TESTS PASSED (backend matrix: batch + gosftp; E2E_RUN=$E2E_RUN) =="
