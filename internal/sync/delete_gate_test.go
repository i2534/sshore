package sync

import "testing"

func TestDeleteGateBlocksAllUnsafeCases(t *testing.T) {
	base := DeleteGateInput{MirrorDelete: true, Complete: true, PrevCount: 20, CurCount: 20}
	cases := []struct {
		name string
		in   DeleteGateInput
		want bool
	}{
		{"镜像关闭", DeleteGateInput{MirrorDelete: false, Complete: true}, false},
		{"本轮扫描不完整", DeleteGateInput{MirrorDelete: true, Complete: false}, false},
		{"根目录消失", DeleteGateInput{MirrorDelete: true, Complete: true, RootGone: true}, false},
		{"内核队列溢出", DeleteGateInput{MirrorDelete: true, Complete: true, Overflow: true}, false},
		{"重连/启动后的第一轮", DeleteGateInput{MirrorDelete: true, Complete: true, FirstRound: true}, false},
		{"远端看起来空了", DeleteGateInput{MirrorDelete: true, Complete: true, CountKnown: true, PrevCount: 5, CurCount: 0}, false},
		{"正常小批量", base, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EvaluateDeleteGate(c.in).Allowed; got != c.want {
				t.Fatalf("Allowed = %v want %v", got, c.want)
			}
		})
	}
}

// 小集合也必须被阈值保护：5 个条目删 3 个就要挂起等确认。
func TestDeleteGateThresholdCoversSmallSets(t *testing.T) {
	got := EvaluateDeleteGate(DeleteGateInput{MirrorDelete: true, Complete: true, CountKnown: true, PrevCount: 5, CurCount: 5, PendingCount: 3})
	if got.Allowed {
		t.Fatal("删 5 个里的 3 个必须先确认")
	}
	if !got.NeedsConfirm {
		t.Fatalf("应为待确认而不是硬拒绝，得到 %+v", got)
	}
}

// 阈值之下的删除必须放行，否则 mirror_delete 形同虚设。
func TestDeleteGateAllowsSmallDelete(t *testing.T) {
	got := EvaluateDeleteGate(DeleteGateInput{MirrorDelete: true, Complete: true, PrevCount: 100, CurCount: 100, PendingCount: 1})
	if !got.Allowed || got.NeedsConfirm {
		t.Fatalf("1/100 应直接放行，得到 %+v", got)
	}
}
