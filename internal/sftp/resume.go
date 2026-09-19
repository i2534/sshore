package sftp

import (
	"os"
	"time"
)

// —— Task 11：双向续传的判定与锚点 ——
//
// 判定是纯函数（decideResume），只吃指纹；任何 Stat/记录都由调用方在喂进来之前完成，
// 这样表驱动单测能逐条钉住「什么情况允许续、什么情况必须整份重传」。

// resumeKind 是「已有 .part 能否继续」的判定结果。
type resumeKind int

const (
	// resumeFull：不可续 —— 清掉旧 .part，整份重传。
	resumeFull resumeKind = iota
	// resumeAppend：接收侧 .part 是源的合法前缀 —— 从 partSize 处续写。
	resumeAppend
	// resumeCommit：接收侧 .part 已完整（size == total）—— 直接提交，不搬字节。
	resumeCommit
)

func (k resumeKind) String() string {
	switch k {
	case resumeAppend:
		return "append"
	case resumeCommit:
		return "commit"
	default:
		return "full"
	}
}

// resumeInput 是 decideResume 的指纹输入。
//
//	PartSize  接收侧 .part 当前大小（下载=本地 .part，上传=远端 .part）
//	Total     源当前大小（下载=远端 Stat，上传=本地 os.Stat）
//	SrcSize   传输开始时记录的源大小（0 = 无记录）
//	SrcMtime  源当前 mtime
//	RecMtime  传输开始时记录的源 mtime（零值 = 无记录）
//	SrcChanged 显式「源已变 / 不可验证」标记
//
// mtime 用 time.Time 的 Equal 比较（计划 Produces 的签名就是 time.Time）：SFTP 的时间戳
// 精确到秒，本地 os.Stat 到纳秒，但两侧都取自同一种 Stat，所以纯粹因为精度造成的假不等
// 不会出现；而 ns 精度恰好能让「同一分钟内的同尺寸改写」也被识别出来。
// Item.ModTime 的 2006-01-02 15:04 字符串格式属于展示/watch-sync 契约，本文件不碰。
type resumeInput struct {
	PartSize   int64
	Total      int64
	SrcSize    int64
	SrcMtime   time.Time
	RecMtime   time.Time
	SrcChanged bool
}

// decideResume 决定「这个 .part 能不能续」，返回判定与续传起点。
//
// 拒绝续传的每一条都有代价理由：
//   - SrcChanged / SrcSize != Total / mtime 不等：源已经不是当初那份了。只比大小发现不了
//     「同尺寸改写」，续写会把旧前缀与新内容拼成一份静默损坏的文件；
//   - PartSize <= 0：没有可续的东西；
//   - PartSize > Total：.part 比源还长，必是旧残留，拼不出正确结果。
//
// 只有「源指纹未变 + part 是 [0, PartSize) 的合法前缀」才是 append；part == total 时是
// commit（避免「续传 0 字节」这种既没意义又可能重新 Stat 出错的动作）。
func decideResume(in resumeInput) (resumeKind, int64) {
	if in.SrcChanged {
		return resumeFull, 0
	}
	if in.SrcSize != in.Total {
		return resumeFull, 0
	}
	if !in.SrcMtime.Equal(in.RecMtime) {
		return resumeFull, 0
	}
	if in.PartSize <= 0 || in.PartSize > in.Total {
		return resumeFull, 0
	}
	if in.PartSize == in.Total {
		return resumeCommit, in.Total
	}
	return resumeAppend, in.PartSize
}

// —— 源指纹锚点表（内存态）——
//
// 传输开始时把源的 (size, mtime) 记在接收侧 .part 路径上；续传请求带着同一个 PartPath 回来
// 时才能验证「这份 .part 还是当初那份源的前缀吗」。进程重启后表为空 —— 无记录一律判不可续
// （多传一次总比拼错文件好），这与技术审核 S10 的代价裁决一致。

const resumeAnchorMax = 256

type resumeAnchor struct {
	// src 是源的**身份**（下载 = 远端源路径，上传 = 本地源路径）。只存 (size, mtime) 时，
	// 另一份同尺寸、同 mtime 的无关源会被误当成合法前缀（M1）；身份必须逐字相符。
	src   string
	size  int64 // 传输开始时记录的源大小（期望长度）
	mtime time.Time
}

// downloadAnchorKey / uploadAnchorKey 给两个方向分开命名空间：本地方向的 .part 路径与远端
// 方向理论上可能字符串相同（如都在 /tmp 下），加前缀避免串号。
//
// Task 12 加固（M1 同类残余）：键里必须含 host + user。同一个 GoBackend 会服务多个 host，
// 而 .part 路径（尤其退化的短名或都用 /tmp 的场景）在不同 host 上完全可能相同；只按路径
// 做键会让 A 主机留下的锚点被 B 主机的续传请求命中，从而把 B 的 .part 当成合法前缀续写。
// 上传方向还含**目的远端路径**：同一 host 把同一个 PartPath 用于不同目标时，源相同也不能
// 互认锚点。键里多算一个维度只会让查不到的锚点退化为整份重传（方向安全）。
func downloadAnchorKey(host, user, part string) string {
	return "d:" + host + "\x00" + user + "\x00" + part
}
func uploadAnchorKey(host, user, dest, part string) string {
	return "u:" + host + "\x00" + user + "\x00" + dest + "\x00" + part
}

// recordResumeAnchor 记录（或覆盖）某个 .part 对应的源指纹（身份 + 期望长度 + mtime）。
// key 为空（无锚点）时 no-op。表有上限：失败/取消留下的锚点会一直留着供续传，长时间运行
// 必须有界；超限时随机淘汰一条（被淘汰的锚点会让下一次续传退化为整份重传，方向安全）。
func (g *GoBackend) recordResumeAnchor(key, src string, size int64, mtime time.Time) {
	if key == "" {
		return
	}
	g.resumeMu.Lock()
	defer g.resumeMu.Unlock()
	if g.resumeAnchors == nil {
		g.resumeAnchors = map[string]resumeAnchor{}
	}
	if _, ok := g.resumeAnchors[key]; !ok && len(g.resumeAnchors) >= resumeAnchorMax {
		for k := range g.resumeAnchors {
			delete(g.resumeAnchors, k)
			break
		}
	}
	g.resumeAnchors[key] = resumeAnchor{src: src, size: size, mtime: mtime}
}

func (g *GoBackend) lookupResumeAnchor(key string) (resumeAnchor, bool) {
	g.resumeMu.Lock()
	defer g.resumeMu.Unlock()
	a, ok := g.resumeAnchors[key]
	return a, ok
}

func (g *GoBackend) forgetResumeAnchor(key string) {
	if key == "" {
		return
	}
	g.resumeMu.Lock()
	delete(g.resumeAnchors, key)
	g.resumeMu.Unlock()
}

// alignRemotePart 在续写前把远端 .part 的长度对齐到 offset（事实 4：Seek 越过 EOF 会零填充
// 出洞，绝不能依赖库的补洞行为）。
//   - 长度 == offset：可续；
//   - 长度 >  offset：Truncate(offset) 丢弃上一次运行遗留的陈旧尾部；
//   - 长度 <  offset：无法凭空补齐（Truncate(offset) 会把缺口补成零洞，正是要避免的损坏），
//     返回 false 让调用方退回整份重传；
//   - 不存在：返回 false（不可续），不报错。
func (g *GoBackend) alignRemotePart(s *Session, part string, offset int64) (bool, error) {
	if offset <= 0 {
		return true, nil
	}
	st, err := s.Conn.Stat(part)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	switch {
	case st.Size() == offset:
		return true, nil
	case st.Size() > offset:
		if err := s.Conn.Truncate(part, offset); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

// decideDownloadResume 为下载方向取指纹：远端 Stat（大小 + mtime）+ 本地 .part 大小 +
// 锚点表里记录的远端指纹。无锚点 → SrcChanged（不可验证 → 整份）。
func (g *GoBackend) decideDownloadResume(s *Session, req TransferRequest) (resumeKind, int64, error) {
	st, err := s.Conn.Stat(req.Remote)
	if err != nil {
		return resumeFull, 0, err
	}
	pst, err := os.Stat(req.PartPath)
	if err != nil {
		// 契约上 PartPath 非空 ⇔ .part 真在磁盘；真丢了就整份重传（防御，不报错）。
		return resumeFull, 0, nil
	}
	in := resumeInput{PartSize: pst.Size(), Total: st.Size(), SrcMtime: st.ModTime()}
	// M1：锚点必须记的是**同一个远端源**。仅比 (size,mtime) 时，另一份同尺寸同 mtime 的
	// 无关源会被误当成合法前缀；身份不符一律按「不可验证」处理。
	if a, ok := g.lookupResumeAnchor(downloadAnchorKey(req.Host, req.User, req.PartPath)); ok && a.src == req.Remote {
		in.SrcSize, in.RecMtime = a.size, a.mtime
	} else {
		in.SrcChanged = true
	}
	k, off := decideResume(in)
	return k, off, nil
}

// decideUploadResume 是上传方向的对称判定：远端 .part 的 Stat + 本地源的大小/mtime +
// 锚点表里记录的本地源指纹。
func (g *GoBackend) decideUploadResume(s *Session, req TransferRequest, total int64, srcMtime time.Time) (resumeKind, int64, error) {
	pst, err := s.Conn.Stat(req.PartPath)
	if err != nil {
		if os.IsNotExist(err) {
			return resumeFull, 0, nil
		}
		return resumeFull, 0, err
	}
	in := resumeInput{PartSize: pst.Size(), Total: total, SrcMtime: srcMtime}
	// M1：同下载方向，锚点必须记的是**同一个本地源**。
	if a, ok := g.lookupResumeAnchor(uploadAnchorKey(req.Host, req.User, req.Remote, req.PartPath)); ok && a.src == req.Local {
		in.SrcSize, in.RecMtime = a.size, a.mtime
	} else {
		in.SrcChanged = true
	}
	k, off := decideResume(in)
	return k, off, nil
}

// resumeSourceUnchanged 用**已打开数据句柄**拿到的 (size, mtime) 复核锚点（I3）。
// decide*Resume 的判定用的是另一次 Stat；从那次 Stat 到真正 Open 之间存在窗口，
// 同尺寸改写若发生在这个窗口里，只靠判定时的指纹会把旧前缀拼到新内容上。
// 因此追加之前必须在打开的句柄上再核一次：锚点存在、源身份相符（M1）、size/mtime 都相等，
// 任一不满足即返回 false，由调用方退回整份重传（绝不写坏文件）。
func (g *GoBackend) resumeSourceUnchanged(key, src string, size int64, mtime time.Time) bool {
	a, ok := g.lookupResumeAnchor(key)
	return ok && a.src == src && a.size == size && a.mtime.Equal(mtime)
}
