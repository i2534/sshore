# Windows 真机验收清单（gosftp 传输底座）

> 本文是 Task 16 交付的**人工验收清单**，由人在 Windows 客户机上执行；编写者未在真机上跑过本清单。
> 配套设计：`docs/superpowers/specs/2026-09-18-sftp-gosftp-transport-design.md`（D1/D3/D15）；
> 前序真机探测结论：`.superpowers/sdd/2026-09-18-sftp-gosftp-transport/task-0-report.md` 与 `probe.md`。
> README 的「已知限制」是本文各项的来源，两边必须保持一致。

## 0. 环境与前置

| 项 | 值 |
|---|---|
| 客户机 | VirtualBox 客户机 `win10`（Windows 10），用户 `lan` |
| SSH 通道 | NAT 端口转发 `127.0.0.1:2222 -> 22` |
| 客户机私钥 | **非默认名** `C:/Users/lan/.ssh/id_winlocal`（客户机 `.ssh` 没有默认名私钥） |
| 自连写法 | 必须先 `-i` 指定：`ssh -i C:/Users/lan/.ssh/id_winlocal -p 2222 lan@127.0.0.1`（或在客户机内用 `winlocal` 别名） |
| 运行会话 | **交互桌面会话（Session 1）**，绝不用服务方式启动 |
| 默认传输 | Task 16 起内置默认 = `gosftp`（`internal/sftp/backend.go` 的 `defaultTransport`） |

**为什么必须在 Session 1（Task 0 实测）**：Session 0（服务 / 非交互会话）里由 Go 进程 spawn 的
`ssh.exe` 会卡在 SFTP INIT 之后（≥15–25s 无响应，未观察到更长等待下的恢复）；被测的 4 个 console
创建标志（`CREATE_NO_WINDOW` / `CREATE_NEW_CONSOLE` / `DETACHED_PROCESS` /
`CREATE_NEW_PROCESS_GROUP`）都无效，window station/desktop 未试。改到 Session 1 后全部正常。
**任一步骤若在 Session 0 执行，得到的是环境噪声，不是产品缺陷。**

启动方式二选一：

- 在客户机桌面上直接双击 exe（最直接）；
- 用计划任务投到交互会话：`schtasks /create /tn sshore-accept /tr C:\\sshore\\sshore.exe /sc once /st 00:00 /it /f` 然后 `schtasks /run /tn sshore-accept`（`/it` 是关键）。

## 1. 准备本轮产物

在宿主仓库根目录：

```bash
git rev-parse --short HEAD        # 记下验收对应的提交
make ci                           # 必须先全绿（构建 + vet + -race 测试 + 前端测试）
make windows                      # 见 Makefile 的 windows 目标；产物在 build/bin/
ls -l build/bin/                  # 记下产物文件名、字节数与 sha256sum
```

把 `build/bin/` 下的 Windows amd64 产物投放进客户机（按实际文件名）：

```bash
scp -P 2222 build/bin/sshore.exe lan@127.0.0.1:C:/sshore/
```

> `make windows` 实际产物名是 `build/bin/sshore.exe`（release 压缩包 
> `sshore-<version>-windows-amd64.zip` 由 CI 打包，本地构建不生成）。以 
> `build/bin/` 实际列表为准。客户机上没有 Go 工具链时用交叉构建（Task 0 的 D-1 先例），
> `make windows` 已是交叉构建。

## 2. 先补 Task 0 的边界判定（四行复核）

Task 0 只验到文件内部回填（`Seek(3)` on 5B），**末尾与越过 EOF 的边界当时未验**（评审 Minor-3）。
在客户机里用非默认键名自连，先补这两条，再验续传。

准备一个 5 字节文件并用交互式 sftp 验；或在客户机的 PowerShell 里用 `ssh -s` 起子系统后跑一个小 Go
程序（Task 0 的 `probe2.exe` 流程）。最小可复现命令（客户机内，PowerShell）：

```powershell
# 2a. -s 前置与后置两种写法都要能起子系统（Task 0 已 PASS，这里复跑确认）
ssh -i C:/Users/lan/.ssh/id_winlocal -p 2222 -s lan@127.0.0.1 sftp   # 前置
ssh -i C:/Users/lan/.ssh/id_winlocal -p 2222 lan@127.0.0.1 -s sftp   # 后置
```

逐项打勾（每项的期望来自 Task 0 的实测，本清单要求复核**同一台机器**上仍然成立）：

- [ ] **2.1 `-s` 写法**：前置 `ssh -s <host> sftp` 与后置 `ssh <host> -s sftp` 都能建立子系统（无
  `subsystem request failed`）。生产实现采用**前置**写法。
- [ ] **2.2 `posix-rename` 真覆盖**：目标文件已存在（内容 `OLD`）时 `PosixRename(源, 目标)` 返回
  `<nil>`，读回内容为新值，并且磁盘大小的确变成新内容的字节数（Task 0 用 `statSize=4` + 磁盘 4 字节
  双重旁证）。**同时**确认 plain `Rename` 到已存在目标按期望失败（`SSH_FX_FAILURE`）且目标内容不变。
- [ ] **2.3 `Seek(partSize)` 续写，含两个边界**：
  - `Seek(3)+Write("XY")` → `ABCXY`（文件内部回填）
  - `Seek(5)+Write("YZ")`（**恰好末尾**）→ `ABCDEYZ`（7 B）
  - `Seek(7)+Write("XY")`（**越过 EOF**）→ `ABCDE\x00\x00XY`（9 B，**空洞补 0**）
  - **结论要点**：续传偏移必须来自真实 part 大小；越过 EOF 会零填充，不能靠推算。
- [ ] **2.4 stderr 处理**：故意请求不存在的子系统（`ssh -s <host> nosuchsubsystem`）时 `ssh` 会写
  stderr（Task 0 观测 117 B，含 `subsystem request failed on channel 0`）；应用日志里要能看到这段原文，
  且传输过程不因 stderr 管道无人读而卡住（raw-pipe 原语后台 drain）。

## 3. 进度 / 取消 / 重试 / 续传（人眼）

### 3.1 大文件下载与上传（进度 UI 可见）

- [ ] 选一个 **>2GB** 的远端文件下载到本地：进度条随字节推进、百分比/速度/ETA 有变化，完成后
  目标文件大小与远端一致。
- [ ] 同样大小本地文件上传到远端：进度条同样推进，完成后远端大小一致。
- [ ] 传输期间**绝对不能**在文件面板里看到 `.sshore-sftppart-` 开头的项（见 §5）。

### 3.2 取消（整批）+ 重试 + 续传

- [ ] **整批取消**：一次派发 ≥3 项，在第 1 项进行中点「取消」。当前项在**百毫秒级**进入「取消」，
  后面的项直接标记为「已取消，停止后续项」，**目标名不出现半截文件**（不出现目标文件只有一部分内容
  的情况）。若该项其实已提交，界面应显示「取消过晚（已完成）」而不是假装取消成功。
- [ ] **重试**：制造一个失败项（例如目标目录不可写），点「重试」能重新执行。
- [ ] **续传**：制造一个中途失败的下载，保留其 `.part`，点「续传」；续传完成后在客户机上比对：
  ```powershell
  certutil -hashfile C:\\path\\to\\downloaded.bin SHA256
  ```
  与**一次性完整传输**的同一文件 sha256 一致。上传方向同理（在远端比对）。
- [ ] **源被改写时不许拼接**（可选但推荐）：续传前把源文件改成不同内容/大小，续传应整份重传而不是
  拼出坏文件。

### 3.3 目录传输

- [ ] 下载一棵多级目录树：结构与逐文件大小一致；已存在目标同名目录按**并入**语义（不嵌套）。
- [ ] 上传一棵多级目录树：远处结果与本地一致。

## 4. 退出后无残留进程（Windows 用 tasklist）

bash harness 里的进程断言在 Windows 会被跳过，**必须在客户机上用 tasklist 手工验**。
在三个时点各跑一次：

```powershell
tasklist /FI "IMAGENAME eq ssh.exe"
tasklist /FI "IMAGENAME eq sftp.exe"
```

- [ ] **传输完成后**：无 `ssh.exe` 残留（应用持有的长连会话按池上限存在是正常的；关键是**关闭应用后**必须清零，见下一条）。
- [ ] **取消后**：被取消项对应的 `ssh.exe` 应当消失（关会话即回收子进程）。
- [ ] **应用退出后**：`ssh.exe` / `sftp.exe` 均无残留（信息: 没有运行的任务匹配指定标准）。

## 5. 长文件名退化与 .part/.bak 不可见

- [ ] 准备一个**名字 >200 字节**的文件（例如 210 个字符的长名）做一次下载与一次上传：传输成功，
  不报 `ENAMETOOLONG`（内部临时名退化为同目录短名 `.sshore-sftppart-<id8>-<rand>`）。
- [ ] 传输中、失败后、取消后，**文件面板里都不出现** `.sshore-sftppart-` / `.sshore-sftppart-bak-`
  开头的项（面板按中缀过滤）。
- [ ] 同步 / 变更监控不会把这类临时名当业务文件同步（可在有同步规则的目录里制造一次传输，
  观察同步日志没有该临时名的下载/上传记录）。

## 6. Windows 上直接跑 Go 测试的注意事项

在客户机上直接 `go test` 时，e2e 用例会读 `SSHORE_SFTP_TRANSPORT` 并与运行时选中的后端做身份断言；
**变量为空会故意 Fatal**（`SSHORE_SFTP_TRANSPORT 未送达：后端身份无法证明`），这是防假绿设计，
不是缺陷。要直接在 Windows 上跑这些用例，必须显式导出：

```powershell
$env:SSHORE_SFTP_TRANSPORT = "gosftp"    # 或 "batch"
$env:SSHORE_E2E_HOST = "<ssh 别名>"
$env:SSHORE_E2E_REMOTE = "<远端目录>"
go test ./internal/sftp/ ./internal/sync/ -run E2E -count=1 -v
```

（完整的双后端矩阵仍以仓库的 `bash e2e/test_local.sh` 为准，它在 Linux 上跑临时 sshd。）

## 7. 验收失败时的一行回退

若 §2–§5 任一项失败且不能当场归因，**不要把默认留在 `gosftp`**：

```go
// internal/sftp/backend.go
const defaultTransport = KindBatch   // 回退：把 KindGo 改回 KindBatch，重新构建
```

改完重跑 `make ci` 并重新出包。显式覆盖（`SSHORE_SFTP_TRANSPORT=gosftp` 或配置
`sftp_transport = "gosftp"`）仍然可用，便于继续定位；失败现象与原始输出请回写进本清单或 spec 的
issue 段（不要只留在聊天记录里）。

## 8. 记录模板

```
验收提交：<git rev-parse --short HEAD>
产物：    <文件名>  sha256=<...>  构建时间=<...>
客户机：  win10 / lan / 会话 Session <N>（必须为 1）
§2 四行复核：  2.1 [ ]  2.2 [ ]  2.3 [ ]  2.4 [ ]
§3 进度/取消/重试/续传：  3.1 [ ]  3.2 [ ]  3.3 [ ]
§4 无残留进程：  完成 [ ]  取消 [ ]  退出 [ ]
§5 长名/.part 不可见：  [ ]
失败项与原始输出：
结论：PASS ⇒ 保留默认 gosftp / FAIL ⇒ 已回退 KindBatch（附 commit）
```
