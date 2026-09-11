package sync

// DeleteGateInput 是一次删除判定所需的全部输入。纯数据，便于逐条测试。
type DeleteGateInput struct {
	MirrorDelete bool
	Complete     bool // 本轮扫描是否完整（有目录未知则为 false）
	CountKnown   bool // PrevCount/CurCount 是否是真实统计（事件路径上未知）
	RootGone     bool // 收到根目录 DELETE_SELF/UNMOUNT/IGNORED
	Overflow     bool // 内核队列溢出或 watch 集合不完整
	FirstRound   bool // 规则启动/重连/模式切换后的第一轮
	PrevCount    int  // 上一轮远端条目数
	CurCount     int  // 本轮远端条目数
	PendingCount int  // 本轮待删数量
}

type GateResult struct {
	Allowed      bool
	NeedsConfirm bool
	Reason       string
}

// EvaluateDeleteGate 实现六重闸门：任一安全条件不满足即禁止删除；
// 达到阈值则挂起等待用户在卡片上确认。
func EvaluateDeleteGate(in DeleteGateInput) GateResult {
	deny := func(reason string) GateResult { return GateResult{Allowed: false, Reason: reason} }
	switch {
	case !in.MirrorDelete:
		return deny("镜像删除未开启")
	case in.RootGone:
		return deny("监控根目录已消失，暂停删除直到对账确认")
	case in.Overflow || !in.Complete:
		return deny("本轮扫描不完整（有目录未知或 watch 不完整），禁止删除")
	case in.FirstRound:
		return deny("启动/重连后的第一轮不执行删除")
	case in.CountKnown && in.CurCount == 0 && in.PrevCount > 0:
		return deny("远端本轮为空而上一轮非空，疑似挂载点掉线，禁止删除")
	}
	n := in.PendingCount
	// 阈值对**小集合**同样生效：不要写成 n > max(10, prev*10%)。
	// 事件路径上 CountKnown=false，此时只按绝对数量 10 兜底（没有可用的分母）。
	if n >= 1 && (n >= 10 || (in.CountKnown && n*2 >= in.PrevCount)) {
		return GateResult{Allowed: false, NeedsConfirm: true,
			Reason: "待删数量达到阈值，已挂起等待确认"}
	}
	return GateResult{Allowed: true, Reason: "通过全部删除闸门"}
}
