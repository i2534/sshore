package watch

import "strings"

// ParseInotifyLine 解析一行 inotifywait 输出。格式（实测）：
//
//	<epoch>|<绝对路径>|<逗号分隔 token 列表>
//
// 例：`1789033487|/srv/conf/a.txt|CLOSE_WRITE,CLOSE`、
// `...|/srv/conf/d|CREATE,ISDIR`、`...|/srv/conf/d/|DELETE_SELF`。
//
// 三个必须遵守的实测约束：
//  1. 行尾可能带 CR（ssh -tt 的 pty ONLCR），先裁掉；
//  2. %e 是 token **列表**，必须按逗号切分——整串比较会全部匹配失败；
//  3. *_SELF 事件的路径带尾斜杠，必须归一化。
func ParseInotifyLine(root, line string) (Event, bool) {
	line = strings.TrimRight(line, "\r\n")
	// 事件永远是最后一个 "|" 之后的那段；路径则是"首个 "|" 之后、最后一个 "|" 之前"
	// 的整段（路径里可能含 "|"，见 TestParseInotifyLineHandlesPipeInPath）。
	first := strings.Index(line, "|")
	last := strings.LastIndex(line, "|")
	if first < 0 {
		return Event{}, false
	}
	var full, ev string
	if first == last || !allDigits(line[:first]) {
		// 无时间戳：<path>|<events>。注意不能取 line[first+1:last]——
		// 那样会把首个 "|" 之前的路径前缀（如 "/srv/conf/we"）整段丢掉。
		full, ev = line[:last], line[last+1:]
	} else {
		// 生产格式：<epoch>|<path>|<events>。%T 是 epoch 秒（纯数字），据此
		// 与"路径含 |"区分；随后取第一个 | 与最后一个 |，中间整段是路径。
		full, ev = line[first+1:last], line[last+1:]
	}
	full = strings.TrimSuffix(full, "/")
	// 必须按**路径边界**判断，不能用裸 HasPrefix：root="/srv/conf" 时
	// "/srv/conf-backup/x" 也会通过前缀检查，把邻居目录的事件混进来。
	base := strings.TrimSuffix(root, "/")
	if full != base && !strings.HasPrefix(full, base+"/") {
		return Event{}, false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(full, base), "/")

	toks := map[string]bool{}
	for _, tk := range strings.Split(ev, ",") {
		toks[strings.TrimSpace(tk)] = true
	}
	// 根目录自身消失：靠事件检测，不能靠进程退出（实测进程会继续运行）。
	if rel == "" {
		if toks["DELETE_SELF"] || toks["UNMOUNT"] || toks["IGNORED"] {
			return Event{RelPath: "", Kind: KindRootGone}, true
		}
		return Event{}, false
	}
	// 必须先判 ISDIR：CLOSE_NOWRITE,CLOSE,ISDIR 同时含 CLOSE，
	// 若先按文件 token 过滤会把目录事件误伤成噪声。
	// *_SELF 的 token **不带 ISDIR**（实测 d/|DELETE_SELF），但路径带尾斜杠；
	// 只有被 watch 的目录才会收到自己的 SELF 事件，故提前归一到目录事件。
	// **注意只提前 SELF 两类**：DELETE / MOVED_FROM 不能放进来 —— 它们既可能是
	// 文件删除（应归 delete）也可能是目录删除（带 ISDIR，应归 dir_gone）。
	if !toks["ISDIR"] && (toks["DELETE_SELF"] || toks["MOVE_SELF"]) {
		return Event{RelPath: rel, Kind: KindDirGone}, true
	}
	// 先判 ISDIR 再看文件 token：CLOSE_NOWRITE,CLOSE,ISDIR 同时含 CLOSE，
	// 顺序反了会把目录事件误伤成噪声。
	if toks["ISDIR"] {
		switch {
		case toks["CREATE"] || toks["MOVED_TO"]:
			return Event{RelPath: rel, Kind: KindDirAdded}, true
		case toks["DELETE"] || toks["MOVED_FROM"] || toks["DELETE_SELF"] || toks["MOVE_SELF"]:
			return Event{RelPath: rel, Kind: KindDirGone}, true
		default:
			return Event{}, false // OPEN,ISDIR / ACCESS,ISDIR / CLOSE_NOWRITE,...,CLOSE,ISDIR
		}
	}
	switch {
	case toks["CREATE"] || toks["MOVED_TO"]:
		return Event{RelPath: rel, Kind: KindCreate}, true
	case toks["CLOSE_WRITE"] || toks["MODIFY"]:
		return Event{RelPath: rel, Kind: KindWrite}, true
	case toks["DELETE"] || toks["MOVED_FROM"]:
		return Event{RelPath: rel, Kind: KindDelete}, true
	default:
		return Event{}, false
	}
}

// allDigits 判断 s 非空且全为 0-9，用于识别 %T epoch 时间戳前缀。
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
