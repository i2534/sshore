// Package sshconn 是 ControlMaster socket 路径与 SSH 连接参数的唯一来源。
// sftp 与 watch 必须共用同一份路径计算——两个包各自算一遍必然漂移。
package sshconn

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"sshore/internal/osutil"
)

// controlDirName 沿用既有目录名，保证 sftp.Ctrl.CloseAll 的清理语义不变。
const controlDirName = "sshore-sftp-ctrl"

func ControlDir() string {
	return filepath.Join(os.TempDir(), controlDirName)
}

// ControlPath 的 key 必须包含 user：同一 host 上不同 user 的规则若共用
// 一个 master，ssh 要么拒绝复用、要么用错身份读写远端文件。
func ControlPath(host, user string) string {
	_ = os.MkdirAll(ControlDir(), 0700)
	name := url.PathEscape(host)
	if user != "" {
		name += "+" + url.PathEscape(user)
	}
	return filepath.Join(ControlDir(), "cm-"+name+".sock")
}

// EnsureMaster 幂等建立 ControlMaster。
// 判活必须用 ssh -O check 的退出码，不能用 os.Stat：kill -9 残留的 socket
// 文件会一直 stat 成功，此后所有连接静默退化成各自建连。
func EnsureMaster(ctx context.Context, r osutil.CtxRunner, host, user string) error {
	cp := ControlPath(host, user)
	if out, err := r(ctx, "ssh", "-O", "check", "-o", "ControlPath="+cp, host); err == nil && out.ExitCode == 0 {
		return nil
	}
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=30",
		"-o", "ControlPath=" + cp,
		"-N", "-f",
	}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	args = append(args, host)
	out, err := r(ctx, "ssh", args...)
	if err != nil {
		return fmt.Errorf("建立 ControlMaster 失败: %w (%s)", err, strings.TrimSpace(out.Stderr))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("建立 ControlMaster 失败: %s", strings.TrimSpace(out.Stderr))
	}
	return nil
}

// Exec 执行一次性远端命令（降级探测等）。remoteCmd 原样交给远端 shell；
// 调用方必须用 QuoteRemote 包住任何外部来源的路径。
func Exec(ctx context.Context, r osutil.CtxRunner, host, user, remoteCmd string) (osutil.Outcome, error) {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlPath=" + ControlPath(host, user),
	}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	args = append(args, host, remoteCmd)
	return r(ctx, "ssh", args...)
}

// QuoteRemote 用单引号包住字符串供远端 shell 使用；内部单引号按 POSIX 规则转义。
func QuoteRemote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
