package watch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sshore/internal/osutil"
	"sshore/internal/sshconn"
)

// DetectOpts 是一条规则里与探测有关的参数。
type DetectOpts struct {
	Host         string
	User         string
	RemotePath   string
	ForcePoll    bool
	PollInterval time.Duration
}

// detectTimeout 探测自身的上限。osutil.CtxRunner 会在超时后真正结束子进程，
// 所以"降级"同时意味着"不留泄漏"。
const detectTimeout = 5 * time.Second

// Detect 判定探测路径。每次重连成功后都要重新判定一次。
func Detect(ctx context.Context, r osutil.CtxRunner, o DetectOpts) Info {
	poll := Info{Mode: "poll", Interval: o.PollInterval}
	if poll.Interval <= 0 {
		poll.Interval = 5 * time.Second
	}
	if o.ForcePoll {
		poll.Reason = "用户选择了强制轮询"
		return poll
	}
	cctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	out, err := sshconn.Exec(cctx, r, o.Host, o.User, "command -v inotifywait")
	switch {
	case err != nil && cctx.Err() != nil:
		poll.Reason = fmt.Sprintf("探测超时（%s）", detectTimeout)
		return poll
	case err != nil:
		poll.Reason = fmt.Sprintf("探测失败：%v", err)
		return poll
	case out.ExitCode != 0:
		// 实测：远端没有 inotifywait 时退出码为 1（不是 127），所以只报实际值。
		poll.Reason = fmt.Sprintf("远端无 inotifywait（非 0 退出：%d）", out.ExitCode)
		return poll
	case strings.TrimSpace(out.Stdout) == "":
		poll.Reason = "远端 command -v 未返回路径"
		return poll
	}
	return Info{Mode: "inotify"}
}
