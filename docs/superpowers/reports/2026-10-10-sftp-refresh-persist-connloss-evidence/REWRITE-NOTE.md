# 历史重写说明（2026-10-10）

本仓库在 2026-10-10 做过一次**历史重写**：把误入库的验收夹具目录 `b-fixtures/`（其中 `upload-big.bin` 为 **80 000 000 字节**）从已推送的历史中剥离。

- 手法：`git filter-branch --index-filter 'git rm -r --cached --ignore-unmatch b-fixtures' --prune-empty -- master feat/sftp-refresh-persist-connloss` → 清理 `refs/original` → `git gc --prune=now` → `git push --force-with-lease origin master`。
- 结果：`.git` 体积 **110 MB → 12 MB**；`b-fixtures/` 对象在 `master` 与 `origin/master` 上均为 **0**；工作树内容不变；门禁 `make ci` **EXIT=0**（38 files / 489 tests）。
- 防复发：`.gitignore` 已加入 `b-fixtures/` 与 `__pycache__/`。

**注意**：重写使**所有提交 SHA 变化**。本轮及此前文档（RESULTS/WINDOWS-C/REGRESSION-B/报告与计划）中引用的短 SHA（如 `a5a6702`、`cf9eb92`、`1e583e0`）**已不再存在**；提交**信息**未变，请按信息检索，例如：

```bash
git log --all --oneline --grep='B 轮旧功能真机回归'
git log --all --oneline --grep='windows/amd64'
```

重写前的完整历史备份（含旧 SHA）在会话临时目录：`/tmp/sshkit-pre-rewrite-20261010.bundle`（`git bundle`，约 90 MB，未入库）。
