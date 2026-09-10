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

// 事件路径上没有分母：CountKnown 留 false，PrevCount 是零值。
// 相对阈值不得在此触发，否则 n*2 >= 0 对任意 n>=1 都成立，
// 每次删除都会被要求确认，mirror_delete 形同虚设。
func TestDeleteGateEventPathHasNoRelativeThreshold(t *testing.T) {
	got := EvaluateDeleteGate(DeleteGateInput{MirrorDelete: true, Complete: true, PendingCount: 3})
	if !got.Allowed || got.NeedsConfirm {
		t.Fatalf("事件路径无分母时不应按相对阈值挂起，得到 %+v", got)
	}
}

// 事件路径仍受绝对数量 10 兜底（没有分母可用）。
func TestDeleteGateEventPathAbsoluteFallback(t *testing.T) {
	got := EvaluateDeleteGate(DeleteGateInput{MirrorDelete: true, Complete: true, PendingCount: 10})
	if got.Allowed || !got.NeedsConfirm {
		t.Fatalf("事件路径待删 10 个应挂起确认，得到 %+v", got)
	}
}

// 空远端硬拒绝（delete_gate.go 的 in.CountKnown && CurCount==0 && PrevCount>0）
// 必须带 CountKnown 前置条件。原因：事件路径上引擎不掌握 CurCount，会把 CurCount
// 留在零值，而 PrevCount 来自 state 可能 > 0；若去掉该前置条件，这条硬拒绝会在
// 每次事件路径判定时触发，从而永久阻断 inotify 路径上的所有删除。此测试固定该
// 契约，禁止后人"修复"掉这个前置条件。
func TestDeleteGateEmptyRemoteHardDenyRequiresKnownCounts(t *testing.T) {
	got := EvaluateDeleteGate(DeleteGateInput{MirrorDelete: true, Complete: true, PrevCount: 5, CurCount: 0, PendingCount: 0})
	if !got.Allowed {
		t.Fatalf("调用方未声明计数真实时，空远端硬拒绝不得触发，得到 %+v", got)
	}
}
