package watch

import "testing"

const root = "/srv/conf"

func TestParseInotifyLineScenarios(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Event
		ok   bool
	}{
		// 生产格式（带 %T 时间戳）：远程命令用的是 '%T|%w%f|%e'，必须覆盖
		{"生产格式-创建", "1789033487|/srv/conf/a.txt|CREATE", Event{"a.txt", KindCreate}, true},
		{"生产格式-含CRLF", "1789033487|/srv/conf/a.txt|CLOSE_WRITE,CLOSE\r", Event{"a.txt", KindWrite}, true},
		// 生产形状 + 路径含 | ：判别式的回归护栏（%T 纯数字 → 取首个 | 与最后一个 | 之间）。
		{"生产格式-路径含|", "1789033487|/srv/conf/we|ird.txt|CREATE", Event{"we|ird.txt", KindCreate}, true},
		{"创建文件", "/srv/conf/a.txt|CREATE", Event{"a.txt", KindCreate}, true},
		{"写入文件", "/srv/conf/a.txt|CLOSE_WRITE,CLOSE", Event{"a.txt", KindWrite}, true},
		{"仅 MODIFY", "/srv/conf/a.txt|MODIFY", Event{"a.txt", KindWrite}, true},
		{"删除文件", "/srv/conf/a.txt|DELETE", Event{"a.txt", KindDelete}, true},
		{"移出文件", "/srv/conf/a.txt|MOVED_FROM", Event{"a.txt", KindDelete}, true},
		{"移入文件", "/srv/conf/a.txt|MOVED_TO", Event{"a.txt", KindCreate}, true},
		{"嵌套路径", "/srv/conf/d/b.txt|CREATE", Event{"d/b.txt", KindCreate}, true},
		// 目录事件一律触发子树对账：mv 进来的目录里已有文件是零事件的
		{"新建目录", "/srv/conf/d|CREATE,ISDIR", Event{"d", KindDirAdded}, true},
		{"移入目录", "/srv/conf/d|MOVED_TO,ISDIR", Event{"d", KindDirAdded}, true},
		{"删除目录", "/srv/conf/d|DELETE,ISDIR", Event{"d", KindDirGone}, true},
		{"移出目录", "/srv/conf/d|MOVED_FROM,ISDIR", Event{"d", KindDirGone}, true},
		// *_SELF 路径带尾斜杠（实测），必须归一化掉
		{"目录自删", "/srv/conf/d/|DELETE_SELF", Event{"d", KindDirGone}, true},
		{"目录自移", "/srv/conf/d/|MOVE_SELF", Event{"d", KindDirGone}, true},
		{"根目录自删", "/srv/conf/|DELETE_SELF", Event{"", KindRootGone}, true},
		// 噪声：目录的 OPEN/ACCESS/CLOSE_NOWRITE 必须丢弃，
		// 但 CLOSE_NOWRITE,CLOSE,ISDIR 里同时含 CLOSE —— 所以必须先判 ISDIR
		{"目录噪声", "/srv/conf/d|CLOSE_NOWRITE,CLOSE,ISDIR", Event{}, false},
		{"目录噪声2", "/srv/conf/d|OPEN,ISDIR", Event{}, false},
		{"目录噪声3", "/srv/conf/d|ACCESS,ISDIR", Event{}, false},
		{"文件噪声", "/srv/conf/a.txt|OPEN", Event{}, false},
		{"文件噪声2", "/srv/conf/a.txt|ACCESS", Event{}, false},
		{"属性噪声", "/srv/conf/a.txt|ATTRIB", Event{}, false},
		{"非本规则路径", "/srv/other/a.txt|CREATE", Event{}, false},
		{"兄弟目录（前缀相同）", "/srv/conf-backup/a.txt|CREATE", Event{}, false},
		{"根路径自身带尾斜杠", "/srv/conf/|CREATE,ISDIR", Event{}, false},
		{"格式不符", "Watches established.", Event{}, false},
		{"空行", "", Event{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseInotifyLine(root, c.line)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (line %q)", ok, c.ok, c.line)
			}
			if ok && got != c.want {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

// pty 下每行都带 CR（实测），解析必须先裁掉。
func TestParseInotifyLineToleratesCRLF(t *testing.T) {
	got, ok := ParseInotifyLine(root, "/srv/conf/a.txt|CLOSE_WRITE,CLOSE\r")
	if !ok || got.Kind != KindWrite {
		t.Fatalf("CRLF 行必须可解析，得到 %+v ok=%v", got, ok)
	}
}

// 路径中可能含 '|'：事件恒取最后一个 '|' 之后；路径取（首个 '|' 之后、最后一个 '|' 之前），
// 无时间戳的 path|events 形状则取（行首、最后一个 '|' 之前）。首个 '|' 之前的段是否为纯数字
// epoch（%T/--timefmt '%s'）用于区分这两种形状。
func TestParseInotifyLineHandlesPipeInPath(t *testing.T) {
	got, ok := ParseInotifyLine(root, "/srv/conf/we|ird.txt|CREATE")
	if !ok || got.RelPath != "we|ird.txt" {
		t.Fatalf("含 | 的路径必须正确切分，得到 %+v ok=%v", got, ok)
	}
}
