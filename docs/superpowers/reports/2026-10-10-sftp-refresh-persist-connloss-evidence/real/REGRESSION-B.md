# 旧功能真机回归验收（B 轮）

- 被测二进制：`build/bin/sshore`，`Version=v0.7.2-52-g0aba549`（= A 块清理后的工作树，含 `AddLocalRecent` 空路径守卫）。
- 工作台与 A/F 轮相同：`Xvfb :99` + 自写 XTEST 驱动 `xdo` + 隔离 HOME `/tmp/sshkit-acc/home` + 可控 TCP 代理。
- 目标主机仍是本机 `10.99.152.18`（真实 sshd + 真实 SFTP 协议）；远端校验一律**从 ssh 侧**执行，而不只看本地文件系统。
- 夹具：`b-fixtures/upload-small.bin`(1MiB)、`upload-big.bin`(80MB)、`updir/{a.txt,b.txt,sub/c.txt}`、`multi1..3.bin`(200KB)；远端目录 `/home/lan/acc-b`，本地下载目录 `/home/lan/acc-dl`。

| # | 项 | 结果 | 证据 / 原始输出 |
|---|---|---|---|
| B-1 | 上传单文件 | **PASS** | 队列 `upload-small.bin → /home/lan/acc-b/upload-small.bin` 1s 完成；`批量upload 1 项：成功 1 跳过 0 失败 0`；ssh 侧 `md5sum` = `01b25e765b0ed9c183b25d37bce6165c` = 本地 md5 ✓ |
| B-2 | 上传目录（递归） | **PASS（功能可用）／见 B-9 缺陷** | `updir → /home/lan/acc-b/updir` 完成，`a.txt/b.txt/sub/c.txt` 三个文件 md5 与本地逐个一致；但**多套了一层**（见 B-9） |
| B-3 | 下载单文件 | **PASS** | `/home/lan/acc-dl/upload-small.bin` md5 = `01b25e765b0ed9c183b25d37bce6165c` = 远端/本地源 md5 ✓ |
| B-4 | 下载目录（递归） | **PASS** | 落点 `/home/lan/acc-dl/updir/updir/{a.txt,b.txt,sub/c.txt}`，与远端源树**逐层一致**（远端源目录里本就含一层 `updir`，由 B-2 的缺陷产生）⇒ 下载方向语义正确 |
| B-5 | 批量路径（batch） | **PASS（1 项级）** | 行菜单「上传到远程」走的是批量实现：`批量upload 1 项：成功 1 跳过 0 失败 0`；多选交互本身可用（ctrl+点击 ⇒ 页脚 `已选 2 / 3 项`） |
| B-6 | 多选 >1 项批量上传 | **未完成** | 见「未完成项」；工具侧限制，不下缺陷结论 |
| B-7 | 拖拽移动/复制 | **未完成** | 见「未完成项」；行本身 `draggable="true"` + `dragstart/drop` 处理器俱在（`FilePane.vue:271-277`、`SftpView.vue:1339-1365`），只是合成拖拽没能让 WebKitGTK 起 DnD |
| B-8 | 搜索（远程递归） | **PASS** | 浮层标题 `在 acc-target : /home/lan/acc-b 下递归搜索`，输入 `upload` 回车 ⇒ `命中 1 项 / upload-small.bin 1048576 2026-10-10 14:59` |
| B-9 | 上传目录多套一层（**既有缺陷，非本轮引入**；已于 commit `a5a6702`+`2420121` 修复，见下节） | **确认缺陷** | 复现：本地 `updir/`（含 `a.txt`）上传到远端 `/home/lan/acc-b` ⇒ 落成 `/home/lan/acc-b/updir/updir/a.txt`；期望 `/home/lan/acc-b/updir/a.txt` |
| B-10 | 本轮改动在旧流程上的副作用 | **无** | 单/目录上下的日志、md5、落点均正常；传输期间未出现任何「远端连接已失效/重连」误判 |

## B-9 根因（已定位到行，且与本轮无关）

```go
// internal/sftp/gosftp.go:1055 PutTree —— 把 req.Remote 当**父目录**，再追加源目录名
rootRemote := path.Join(req.Remote, filepath.Base(filepath.Clean(req.Local)))
```
```go
// internal/sftp/gosftp.go:509+ GetTree —— 把 req.Local 当**最终目录**（内容直接落进去）
if merr := os.MkdirAll(req.Local, 0o755); merr != nil { ... }
p := filepath.Join(req.Local, filepath.FromSlash(d))
```
前端两个方向都传"最终路径"：`dst: posixJoin(remotePath.value, name)`（`SftpView.vue:907`）/ `dst: targetDir + '/' + n`（`:1039`）。

⇒ **上传方向多套一层**（`PutTree` 与 `GetTree` 语义不对称；batch 后端 `sftp put -r <remoteDir> <local>` 同理），下载方向正确。

**判定其为既有缺陷的依据**：`internal/sftp/gosftp.go` 与 `master` **md5 完全相同**（`7b2447852f1ecec131ccdca5ab2ff76c`）；前端 dst 计算与 master 也逐字相同。即该行为在本分支之前就存在。

修复选项（**未执行，等决策**）：
1. `PutTree` 改为把 `req.Remote` 当最终目录（与 `GetTree` 对齐）——同时需调整 `cleanup_test.go` 等以 `Remote: "."` 表达的父目录约定与 batch 后端；
2. 前端在 `isDir` 时传父目录 —— 改动小，但会让传输队列展示的 dst 与文件级语义不一致。

## B-9 修复（2026-10-10，选项 1 已落地）

**已在 commit `a5a6702`（`internal/sftp/gosftp.go` PutTree）与 `2420121`（`internal/sftp/batch.go` PutRecursive）修复**，未改前端（前端本来就是「最终路径」约定）：

- `PutTree`：`rootRemote := path.Join(req.Remote, filepath.Base(filepath.Clean(req.Local)))` → `rootRemote := path.Clean(req.Remote)`；逐文件落到 `path.Join(rootRemote, <相对源根路径>)`。
- `PutRecursive`（batch）：不再生成 `sftp put -r <local> <remoteDir>`（OpenSSH 的父目录约定），改为本地遍历源树，在同一 `sftp -b` 脚本里 `-mkdir` 各级目标目录 + 逐文件 `put` 到最终路径。
- 清理边界不变：重试前的 `.part` 清理仍按「本树目标 basename + 本次 id 短前缀」双重判据（`isPartForTarget`），目标目录里别的 id 的 `.part` 与同名普通业务文件都不得删除（`cleanup_test.go` 已补断言）。
- 验证：`TestTreeE2E` 在**一次性真 sshd** 下 batch/gosftp 两个迭代都 PASS（目标目录**故意不预建**，由实现自行建出；断言落点为最终路径且不出现 `<target>/a`）；详见 `puttree-nesting-fix.md`。

期望落点因此从 `/home/lan/acc-b/updir/updir/a.txt` 变为 `/home/lan/acc-b/updir/a.txt`。**注意**：B-4 那条「下载方向正确」的结论当时是基于 B-2 缺陷产生的远端树（远端源里本就含一层 `updir`）；修复后若重跑 B-4，需按修复后的远端树重新取样。

## 未完成项（工具侧限制，非"未发现问题"）

- **多选 >1 项批量上传**：页脚的 `全选` 按钮与 `已选 N▼` 批量下拉对合成点击**无响应**（同一套 XTEST 点击在行/表头/路径栏/右键菜单/连接按钮上都生效，故不是驱动整体失效）；尝试过的路径：`全选`(620,639)、(658,639) 反选、`已选 2▼`(850,639)、行菜单 + 多选（右键会保住多选，但菜单首项位置在"多选"下与标定不同，两次点击未命中）。**需要真实鼠标或人眼确认一次**。
- **HTML5 拖拽**：`xdo drag` 已是 12 步插值 + 40ms 间隔的慢拖，仍被 WebKit 当成文本选择（整面板出现 `::selection`），故 DnD 未触发。该功能的接线由单测覆盖（`filePaneWiring.test.js` 对 `dragstart/drop` 的 emit 断言），但真机手势本轮未取到证据。

## 收尾

- 夹具与远端/本地临时目录：`b-fixtures/`、`/home/lan/acc-b`、`/home/lan/acc-dl`（B 轮结束后清理）。
- 临时 ssh 别名块（`acc-target`/`acc-proxy`）在 C 轮结束后统一删除并做 md5 校验（备份 `/tmp/sshkit-acc/ssh-config.bak2`，md5 `8b71afb349b86c58b35a73e95a97dc4b`）。
