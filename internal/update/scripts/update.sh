#!/bin/sh
# sshore 升级脚本：由主程序按本次升级写出并传参，不要手工修改。
# 参数：--pid <旧PID> --target <正式二进制> --pending <待安装文件> --backup <备份路径>
#       --size <字节> --log <日志> --wait <秒>
# 日志协议（末行由主程序残留自检读取）：RESULT=ok 或 RESULT=fail:<step>
set -u

# 解析自身绝对路径：脚本会在替换前 cd 到目标目录，$0 若是相对路径就再也删不掉自己（已实测）。
SELF=$(cd "$(dirname "$0")" 2>/dev/null && pwd)/$(basename "$0")

PID=""; TARGET=""; PENDING=""; BACKUP=""; SIZE=""; LOG=""; WAIT=60
while [ $# -gt 0 ]; do
  case "$1" in
    --pid) PID="$2"; shift 2 ;;
    --target) TARGET="$2"; shift 2 ;;
    --pending) PENDING="$2"; shift 2 ;;
    --backup) BACKUP="$2"; shift 2 ;;
    --size) SIZE="$2"; shift 2 ;;
    --log) LOG="$2"; shift 2 ;;
    --wait) WAIT="$2"; shift 2 ;;
    *) shift ;;
  esac
done

fail() {
  printf "STEP=%s ERR=%s\nRESULT=fail:%s\n" "$1" "$2" "$1" >> "$LOG"
  # 参数类失败按 spec §8.2 退 2，其余（0/3/5/6/launch/wait）退 3
  case "$1" in
    args) exit 2 ;;
    *) exit 3 ;;
  esac
}

[ -n "$LOG" ] || LOG=/dev/null
: > "$LOG" 2>/dev/null || fail args "无法创建日志文件"

case "$PID" in ""|*[!0-9]*) fail args "pid 非法" ;; esac
case "$SIZE" in ""|*[!0-9]*) fail args "size 非法" ;; esac
case "$WAIT" in ""|*[!0-9]*) fail args "wait 非法" ;; esac
[ -n "$TARGET" ] || fail args "target 缺失"
[ -n "$PENDING" ] || fail args "pending 缺失"
[ -f "$TARGET" ] || fail args "target 不存在"
[ -f "$PENDING" ] || fail args "pending 不存在"
# BACKUP/LOG 本次运行才创建，只要求父目录存在且可写（spec §8.2）
[ -d "$(dirname "$BACKUP")" ] || fail args "backup 父目录不存在"
[ -d "$(dirname "$LOG")" ] || fail args "log 父目录不存在"

DIR=$(dirname "$TARGET")
cd "$DIR" || fail 0 "无法进入目标目录"
PB=$(basename "$PENDING")
BB=$(basename "$BACKUP")

# 2) 等旧进程退出（上限 WAIT 秒）
waited=0
while kill -0 "$PID" 2>/dev/null; do
  [ "$waited" -lt "$WAIT" ] || fail wait "等待旧进程退出超时"
  sleep 1
  waited=$((waited+1))
done

# 3) 自检 pending 存在且大小一致
[ -f "$PENDING" ] || fail 3 "pending 已消失"
actual=$(wc -c < "$PENDING")
[ "$actual" = "$SIZE" ] || fail 3 "pending 大小不符：$actual != $SIZE"

# 4) 清理更早备份（只留最近一份；绝不删 pending、sidecar、脚本、日志）
for f in "$DIR"/sshore.v* "$DIR"/sshore.dev-*; do
  [ -e "$f" ] || continue
  b=$(basename "$f")
  [ "$b" = "$PB" ] && continue
  [ "$b" = "$BB" ] && continue
  case "$b" in *.sha256|*.log|*-update.sh|*-update.cmd) continue ;; esac
  rm -f "$f" || true
done

# 5) 旧二进制改名备份；改完立刻确认备份确实就位（spec §8.3：任何一步失败都不得让应用消失）
mv -f "$TARGET" "$BACKUP" || fail 5 "备份旧二进制失败"
[ -f "$BACKUP" ] || fail 5 "备份不完整"
# 6) 待安装文件改名正式名（失败则把备份改回去，避免应用消失）
[ -f "$PENDING" ] || fail 3 "pending 在替换前消失"
if ! mv -f "$PENDING" "$TARGET"; then
  mv -f "$BACKUP" "$TARGET" 2>/dev/null || true
  [ -f "$TARGET" ] || fail 6 "替换失败且回滚未恢复正式名"
  fail 6 "替换正式名失败"
fi
chmod +x "$TARGET" 2>/dev/null || true

# 8) 启动新版并做 3 秒存活探测；失败则回滚
#
# 存活探测的前提（重要）：这里依赖 util-linux setsid 的「非 fork exec」语义 —— 当脚本
# 自身不是进程组首进程时，setsid 直接 exec 目标而不 fork，故 $! 就是新版进程的 PID，
# kill -0 才能真实反映它是否还活着。
# sysvinit/busybox 的 setsid 变体、或脚本自身恰好已是进程组首进程时，setsid 会先 fork
# 再 exec：$! 变成那个转瞬即逝的中间父进程，探测会误判「新版已退出」。正是这类误判
# （以及新版自身启动即崩）让下面的失败回滚分支必须存在：探测失败也要把正式名恢复成
# 旧二进制，绝不让应用消失。
if command -v setsid >/dev/null 2>&1; then
  setsid "$TARGET" >/dev/null 2>&1 &
else
  nohup "$TARGET" >/dev/null 2>&1 &
fi
NEWPID=$!
sleep 3
if ! kill -0 "$NEWPID" 2>/dev/null; then
  mv -f "$TARGET" "$PENDING" 2>/dev/null || true
  mv -f "$BACKUP" "$TARGET" 2>/dev/null || true
  if command -v setsid >/dev/null 2>&1; then
    setsid "$TARGET" >/dev/null 2>&1 &
  else
    nohup "$TARGET" >/dev/null 2>&1 &
  fi
  fail launch "新版本启动失败，已回滚到旧版本"
fi

# 9) 成功：写结果、清日志、自删
printf "RESULT=ok\n" >> "$LOG"
rm -f "$LOG" "$SELF"
exit 0
