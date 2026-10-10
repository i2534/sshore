# REFERENCE —— SSHorE 真机验收命令手册

SKILL.md 是流程与规则，本文件是"照着敲"的细节：坐标标定、两条通道的完整命令、客户机侧脚本、排障树。

---

## 1 坐标标定（每次都要重新做）

**不要复用旧坐标**。面板会随"传输队列面板/日志面板是否出现"整体上移。标定流程：

```bash
ACC=/tmp/sshkit-acc
X(){ DISPLAY=:99 $ACC/xdo "$@"; }
X list                                   # 找应用窗口 id（Sshore）或 RDP 客户端（xfreerdp）
X focus <win>                            # 无 WM 时必须显式 focus，否则键盘事件可能不落地
DISPLAY=:99 import -window root /tmp/cal.png
# 用 python/PIL 裁出关键区域来读坐标（比整图读省 token）：
python3 - <<'PY'
from PIL import Image
im = Image.open("/tmp/cal.png")
im.crop((340, 60, 800, 130)).save("/tmp/cal-toolbar.png")    # 工具栏（主机下拉/连接/刷新）
im.crop((340, 150, 800, 300)).save("/tmp/cal-rows.png")      # 本地行
print(im.size)
PY
```

### 本仓库实测过的两组起始坐标（**仅作起点，必须再截图确认**）

窗口 1100×720 @ (250,140)（Xvfb 1600×1000，无队列面板时）：

| 元素 | 坐标 |
|---|---|
| 侧栏 SFTP / 文件同步 / 端口转发 | (281,248) / (281,208) / (281,168) |
| 主机下拉 / 连接-断开 / 工具栏刷新 | (487,165) / (630,165) / (710,165) |
| 本地路径栏 / 位置 | (555,209) / (740,209) |
| 远程路径栏 / 位置 | (1090,209) / (1200,209) |
| 本地过滤框 / 远程过滤框 | (555,639) / (1030,639) |
| 行（`..` 起）| y=267，行距 ≈30.5 |

RDP 1600×900（Windows 通道，窗口约 1083×710 @ (180,18)）：

| 元素 | 坐标 |
|---|---|
| 侧栏 SFTP | (218,158) |
| 主机下拉 / 连接-断开 / 刷新 / 显示隐藏文件 | (407,88) / (476,88) / (570,88) / (630,88) |
| 本地路径栏 / 远程路径栏 | (443,119) / (980,119) |
| 行（`..` 起，无队列）| y=176，行距 ≈25 |
| 本地/远程过滤框（无队列 / 有队列）| y=510 / y≈460 |

**队列面板出现后**（"↓/↑ 传输项"那块）：过滤框与所有行一起上移约 50px；日志面板的"按来源筛选/全部/清空"一行在 y≈577（无队列时）。

---

## 2 Linux 通道：完整命令

```bash
ACC=/tmp/sshkit-acc
# 0) 编译驱动（一次即可）
gcc -O2 -o $ACC/xdo <repo>/.dsh/skills/sshore-real-machine-testing/tools/xdo.c -lX11 -lXtst

# 1) 显示 + 应用（隔离 HOME）
Xvfb :99 -screen 0 1600x1000x24 -nolisten tcp >$ACC/log/xvfb.log 2>&1 &
setsid env HOME=$ACC/home DISPLAY=:99 XDG_RUNTIME_DIR=$ACC/run \
  LIBGL_ALWAYS_SOFTWARE=1 WEBKIT_DISABLE_COMPOSITING_MODE=1 GDK_BACKEND=x11 \
  <repo>/build/bin/sshore >>$ACC/log/app.log 2>&1 &

# 2) 临时 ssh 别名（让应用的 ssh 子进程能解析主机；用完必删）
cp ~/.ssh/config $ACC/ssh-config.bak && md5sum $ACC/ssh-config.bak
cat >> ~/.ssh/config <<'EOF'

# === SSHorE 真机验收临时别名（验收后删除）===
Host acc-target
  HostName <真机IP>
  User <user>
  Port 22
EOF

# 3) 可控代理（可选，用来造"休眠/黑洞"）
RM_PROXY_LISTEN=127.0.0.1:2222 RM_PROXY_TARGET=<真机IP>:22 RM_PROXY_CTL=$ACC/proxy.ctl \
  python3 <repo>/.dsh/skills/sshore-real-machine-testing/tools/proxy.py >>$ACC/log/proxy.log 2>&1 &
echo normal > $ACC/proxy.ctl        # 之后按需切 blackhole / blackhole-all / reset

# 4) 驱动 GUI
X(){ DISPLAY=:99 $ACC/xdo "$@"; }
X focus <win>; X click …; X rclick …; X slowclick …; X clickmod ctrl …
DISPLAY=:99 import -window root /tmp/x.png
```

**模拟"主机休眠"最省事的两种做法**：① 杀应用的 ssh 子进程（`pkill` 自匹配陷阱见 SKILL.md §2.4，用 `pgrep -f 'acc-targe[t] sftp'`）；② 代理切 `blackhole-all`。

---

## 3 Windows 通道：完整命令

```bash
export PATH=~/.dsh/skills/vbox-win/bin:$PATH
ACC=/tmp/sshkit-acc
# 1) 占用与启动
vboxctl who && vboxctl start && vboxctl ping          # ping 返回 pong:true 说明 agent 在 Session 1

# 2) 端口体检（关键！）
ss -ltnp | grep -E ':13389|:2222'                     # 13389 若被别人的 ssh 隧道占用 → 用自己的备端口
VBoxManage controlvm win10 natpf1 "rdpalt,tcp,127.0.0.1,13390,,3389"
echo | openssl s_client -connect 127.0.0.1:13390 2>/dev/null | openssl x509 -noout -subject
#   subject CN 必须等于客户机名（本机实测 DESKTOP-ACCEPT）；若是别的名字 ⇒ 你连错了机器

# 3) 部署与启动（共享目录）
SB=/home/lan/virtual/share/vbox-agent/sshore-mrt && mkdir -p $SB
cp <repo>/build/bin/sshore.exe $SB/
vboxctl agent '{"op":"run","file":"\\\\VBOXSVR\\share\\vbox-agent\\sshore-mrt\\sshore.exe","wd":"\\\\VBOXSVR\\share\\vbox-agent\\sshore-mrt"}'

# 4) RDP 真实输入（凭据只从凭据文件读）
PW=$(python3 -c "import yaml,os;print(yaml.safe_load(open(os.path.expanduser('~/.dsh/.credentials.yaml'))).get('vbox_win10_password',''))")
[ -n "$PW" ] || echo "缺 vbox_win10_password：写入 ~/.dsh/.credentials.yaml 或导出 VBOX_WIN_PASSWORD"
Xvfb :99 -screen 0 1600x900x24 -nolisten tcp & sleep 2
setsid env DISPLAY=:99 ~/.dsh/skills/vbox-win/bin/xfreerdp \
  /v:127.0.0.1:13390 /u:lan /p:"$PW" /cert:ignore /size:1600x900 +clipboard >/tmp/acc-rdp.log 2>&1 &
grep -cE 'LOGON_FAILURE|ERRCONNECT' /tmp/acc-rdp.log   # 必须为 0
DISPLAY=:99 import -window root /tmp/win1.png          # 之后用 xdo 在 RDP 窗口上点击

# 5) 客户机侧读数/造夹具（脚本不要带 BOM）
cat > /tmp/probe.ps1 <<'EOF'
"host: " + $env:COMPUTERNAME
(Get-FileHash C:\vbox-agent\dl-src.bin -Algorithm SHA256).Hash
EOF
vboxctl ps /tmp/probe.ps1

# 6) 收尾
vboxctl agent '{"op":"kill","name":"sshore"}'
for n in xfreerdp Xvfb; do p=$(pgrep -x $n | tr '\n' ' '); [ -n "$p" ] && kill $p; done
vboxctl console-restore
VBoxManage controlvm win10 natpf1 delete rdpalt
rm -rf $SB
vboxctl stop && vboxctl release
```

**哈希核对**：宿主侧 `md5sum/sha256sum` ↔ 客户机侧 `Get-FileHash`，**两侧都必须取**；只对比文件大小不算验证。

**amd64 vs 386**：`wails build -platform windows/amd64` 是发布目标，且**本机不需要 mingw**（实测 9.2s 成功，`file` 显示 `PE32+ x86-64`）。`windows/386` 也能构建，但 32 位进程有 WOW64 重定向问题（已由 `internal/osutil/execpath.go` 的 `Sysnative` 解析修掉）；若只是想验发布行为，直接上 amd64。

---

## 4 排障树（按症状）

```
应用操作报错 "connection lost" / "Connection reset"
├─ 日志里紧跟着 "[WARN] 远端连接已失效…" + "[INFO] 已重新连接"? → 自愈链路正常工作
├─ 只有 "[ERROR] Error: sftp …" + "[INFO] 探活耗时 Nms" 而无失效? → 探针判定链路健康（正确语义）
└─ 是 permission denied / file does not exist 之类? → 非链路错误，不该重连（若不重连=符合设计）

切页签后一直"没有新日志"
├─ 先确认窗口 focus（无 WM）：xdo focus <win>
├─ 再确认点击落点：截图看按钮是否高亮/菜单是否出现
└─ 仍无 → 看应用窗口是否还在（pgrep -x sshore）；RDP 下看 xfreerdp 是否还活着

Windows 上点哪都没反应
├─ 是不是会话内 agent 的 SendInput？→ 换 RDP 通道（本机实测 SendInput 在客户机不落地）
├─ agent 的 click 返回 cursor 恒为屏幕中心? → 该 op 不吃 x/y，必须先 setpos
└─ RDP 里点不动 → 检查 xfreerdp 是否还活着 + 用 xdo focus 后重试

连接报 exec: "ssh": executable file not found in %PATH%
└─ windows/386 的 WOW64 重定向 → 用 amd64 构建，或确认 execpath.go 的 Sysnative 解析生效

vboxctl ping: 客户机 SSH 不通
├─ ss -ltnp | grep :2222 被自己的代理占用? → 杀代理 + 重挂 natpf1 sshfwd,tcp,127.0.0.1,2222,,22
└─ 2222 没人占但仍不通 → 客户机 sshd 未就绪：等 30–60s 再 ping
```

---

## 5 结果表模板

```markdown
| # | 项 | 结果 | 关键观察（日志原文/哈希/量化） | 证据 |
|---|---|---|---|---|
| X-1 | <判据> | PASS/FAIL/未验证 | `2026-10-10 18:57:16 [INFO] 批量download 1 项：成功 1 跳过 0 失败 0`；宿主 sha256 `…` = 客户机 sha256 | `x-1-….jpg` |
```

未验证项单独列，并写清"为什么没验"（缺通道/缺凭据/被上游阻断）。
