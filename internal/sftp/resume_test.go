package sftp

import (
	"os"
	"testing"
	"time"
)

// —— Task 11 hermetic 单测：续传判定是纯函数 ——
// 指纹 = 接收侧 .part 大小 + 源当前大小 / mtime + 传输开始时记录的源 mtime。
// 同尺寸改写（大小不变、mtime 变）必须整份重传，绝不把旧前缀拼到新内容上。
func TestDecideResume(t *testing.T) {
	t1 := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 18, 10, 30, 0, 0, time.UTC)

	cases := []struct {
		name string
		in   resumeInput
		kind resumeKind
		off  int64
	}{
		{"续写：part 是合法前缀", resumeInput{PartSize: 40, Total: 100, SrcSize: 100, SrcMtime: t1, RecMtime: t1}, resumeAppend, 40},
		{"提交：part 已完整", resumeInput{PartSize: 100, Total: 100, SrcSize: 100, SrcMtime: t1, RecMtime: t1}, resumeCommit, 100},
		{"整份：计划无 mtime 指纹的最简形态（commit）", resumeInput{PartSize: 100, Total: 100, SrcSize: 100}, resumeCommit, 100},
		{"整份：计划无 mtime 指纹的最简形态（append）", resumeInput{PartSize: 40, Total: 100, SrcSize: 100}, resumeAppend, 40},
		{"整份：同尺寸改写（mtime 变）", resumeInput{PartSize: 40, Total: 100, SrcSize: 100, SrcMtime: t2, RecMtime: t1}, resumeFull, 0},
		{"整份：同尺寸改写（part 已完整但 mtime 变）", resumeInput{PartSize: 100, Total: 100, SrcSize: 100, SrcMtime: t2, RecMtime: t1}, resumeFull, 0},
		{"整份：只有一侧有 mtime（不可验证）", resumeInput{PartSize: 40, Total: 100, SrcSize: 100, SrcMtime: t1}, resumeFull, 0},
		{"整份：源大小变了", resumeInput{PartSize: 40, Total: 100, SrcSize: 120, SrcMtime: t1, RecMtime: t1}, resumeFull, 0},
		{"整份：显式标记源已变", resumeInput{PartSize: 40, Total: 100, SrcSize: 100, SrcChanged: true}, resumeFull, 0},
		{"整份：零长度 part（没有可续的东西）", resumeInput{PartSize: 0, Total: 100, SrcSize: 100}, resumeFull, 0},
		{"整份：part 超出总量", resumeInput{PartSize: 120, Total: 100, SrcSize: 100}, resumeFull, 0},
		{"整份：空源", resumeInput{PartSize: 0, Total: 0, SrcSize: 0}, resumeFull, 0},
	}
	for _, c := range cases {
		gotKind, gotOff := decideResume(c.in)
		if gotKind != c.kind || gotOff != c.off {
			t.Fatalf("%s: decideResume(%+v) = %v/%d, want %v/%d", c.name, c.in, gotKind, gotOff, c.kind, c.off)
		}
	}
}

// TestResumeKindString 钉住日志/错误文案里的可读名，顺便防止常量顺序被无意调换。
func TestResumeKindString(t *testing.T) {
	if resumeFull.String() != "full" || resumeAppend.String() != "append" || resumeCommit.String() != "commit" {
		t.Fatalf("resumeKind 文案错: %v/%v/%v", resumeFull, resumeAppend, resumeCommit)
	}
}

// TestAlignRemotePartLength（事实 4）：Seek 越过 EOF 会零填充出洞，因此续写前必须把远端 .part
// 的长度对齐到 offset：长于 offset 截断丢弃陈旧尾部；短于 offset 绝不 Truncate 补 0（那正是洞），
// 而是返回 false 让调用方退回整份重传。
func TestAlignRemotePartLength(t *testing.T) {
	root := t.TempDir()
	longPart := writeRemote(t, root, "long.part", []byte("0123456789")) // 10 字节，比目标 offset 长
	shortPart := writeRemote(t, root, "short.part", []byte("0123"))     // 4 字节，比目标 offset 短
	g := backendForTestServer(t, root)
	s := acquireTransferSession(t, g)

	ok, err := g.alignRemotePart(s, "long.part", 4)
	if err != nil || !ok {
		t.Fatalf("长于 offset 应截断后继续，ok=%v err=%v", ok, err)
	}
	if b, rerr := os.ReadFile(longPart); rerr != nil || string(b) != "0123" {
		t.Fatalf("陈旧尾部必须被截断，err=%v content=%q", rerr, string(b))
	}

	ok, err = g.alignRemotePart(s, "short.part", 8)
	if err != nil {
		t.Fatalf("短于 offset 不应报错（应退回整份）: %v", err)
	}
	if ok {
		t.Fatal("短于 offset 不得续传：Truncate(offset) 会把缺口补成 0，最终文件必被静默损坏")
	}
	if b, rerr := os.ReadFile(shortPart); rerr != nil || string(b) != "0123" {
		t.Fatalf("拒绝续传时不得改动文件: err=%v content=%q", rerr, string(b))
	}

	if ok, err := g.alignRemotePart(s, "missing.part", 8); err != nil || ok {
		t.Fatalf("不存在的 .part 应判定为不可续且不报错，ok=%v err=%v", ok, err)
	}
}
