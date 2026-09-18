package sftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// —— Task 17 修复波：目录项重试前的 .part 清理 / 单文件失败末帧 ——

// TestIsPartForTargetRecognizesOnlyOwnParts：清理判据必须同时认出常规名与退化短名，
// 且**绝不**把别的目标 / 别的 id 的临时文件算成自己的（否则重试会误删并发传输的锚点）。
func TestIsPartForTargetRecognizesOnlyOwnParts(t *testing.T) {
	const id = "t17"
	const target = "a.bin"
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"a.bin" + PartMarker + "t17-ab12cd", true},   // 常规名：<target><marker><id8>-<rand>
		{PartMarker + "t17-ab12cd", true},             // 退化短名：同目录 <marker><id8>-<rand>
		{"b.bin" + PartMarker + "t17-ab12cd", false},  // 同 id、别的目标
		{"a.bin" + PartMarker + "zzz9-ab12cd", false}, // 同目标、别的 id（必须在前缀比对里分开）
		{"a.bin", false},                   // 普通业务文件
		{PartMarker + "bak-ab12cd", false}, // backup-swap 的 bak 不是 part
	} {
		if got := isPartForTarget(tc.name, target, id); got != tc.want {
			t.Fatalf("isPartForTarget(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	// 退化短名必须带**本次** id 前缀才命中（shortID 因 id 而异，t17 vs zzz9 可区分）。
	if isPartForTarget(PartMarker+"t17-ab12cd", target, "zzz9") {
		t.Fatal("别的 id 不得命中本目标的退化短名（否则重试会误删并发传输的 .part）")
	}
	// 长 id：命名里用的是其前 8 个 rune，判据必须与 shortID 同源。
	long := "abcdefgh-9999"
	if !isPartForTarget(PartMarker+shortID(long)+"-ab12cd", "whatever.bin", long) {
		t.Fatal("长 id 的退化短名必须按 shortID 的前 8 rune 命中")
	}
}

// TestCleanupTreePartsRemovesOnlyKnownLocalParts：本地清理按「目标 basename + 本次 id」
// 前缀比对逐条目判定；无关的 .part 必须留下（绝不做整棵子树的无差别清扫）。
func TestCleanupTreePartsRemovesOnlyKnownLocalParts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(root, "sub", "a.bin"+PartMarker+"t17-aaaaaa")
	other := filepath.Join(root, "sub", "b.bin"+PartMarker+"t17-bbbbbb")    // 别的目标
	otherID := filepath.Join(root, "sub", "a.bin"+PartMarker+"zzz9-cccccc") // 别的 id
	plain := filepath.Join(root, "a.bin")
	for _, p := range []string{mine, other, otherID, plain} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	g := NewGoBackend(nil, nil)
	if n := g.cleanupTreeParts(nil, root, []string{"a.bin"}, "t17"); n != 1 {
		t.Fatalf("应恰好删掉 1 个本项 .part，got %d", n)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatalf("本项 .part 必须被删掉, stat err=%v", err)
	}
	for _, p := range []string{other, otherID, plain} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("不得误删 %s: %v", filepath.Base(p), err)
		}
	}
	// 幂等：第二次没有可删的了。
	if n := g.cleanupTreeParts(nil, root, []string{"a.bin"}, "t17"); n != 0 {
		t.Fatalf("第二次清理应为 0，got %d", n)
	}
}

// TestCleanupRemoteTreePartsRemovesOnlyKnownRemoteParts：远端正向对称（走真实内存服务端），
// 且根目录不存在时安静返回 0（重试的 PutTree 在建目录之前调用它）。
func TestCleanupRemoteTreePartsRemovesOnlyKnownRemoteParts(t *testing.T) {
	root := t.TempDir()
	g := backendForTestServer(t, root)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRemote(t, root, "sub/"+"a.bin"+PartMarker+"t17-aaaaaa", []byte("x"))
	writeRemote(t, root, "sub/"+"b.bin"+PartMarker+"t17-bbbbbb", []byte("x"))
	writeRemote(t, root, "sub/"+"a.bin"+PartMarker+"zzz9-cccccc", []byte("x"))
	s, err := g.pool.AcquireList(context.Background(), "h", "")
	if err != nil {
		t.Fatal(err)
	}
	defer g.pool.Release(s, false)
	n := g.cleanupRemoteTreeParts(context.Background(), s, "sub", []string{"a.bin"}, "t17")
	if n != 1 {
		t.Fatalf("应恰好删掉 1 个本项远端 .part，got %d", n)
	}
	if _, serr := os.Stat(filepath.Join(root, "sub", "a.bin"+PartMarker+"t17-aaaaaa")); !os.IsNotExist(serr) {
		t.Fatalf("本项远端 .part 必须被删掉, stat err=%v", serr)
	}
	for _, n2 := range []string{"b.bin" + PartMarker + "t17-bbbbbb", "a.bin" + PartMarker + "zzz9-cccccc"} {
		if _, serr := os.Stat(filepath.Join(root, "sub", n2)); serr != nil {
			t.Fatalf("不得误删 %s: %v", n2, serr)
		}
	}
	// 根目录不存在（PutTree 尚未 MkdirAll）：安静返回 0，绝不报错/阻塞。
	if n := g.cleanupRemoteTreeParts(context.Background(), s, "no-such-dir", []string{"a.bin"}, "t17"); n != 0 {
		t.Fatalf("根不存在时应返回 0，got %d", n)
	}
}

// TestGoBackendGetTreeRetryCleansStaleParts（spec §8 目录行 / D10）：目录项失败后重试前，
// 必须清掉该子树里**本次 id** 的遗留 .part；重试过程本身照常走（这里让重试仍在同一个
// 文件上失败，与第几次尝试无关）。
func TestGoBackendGetTreeRetryCleansStaleParts(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	writeRemoteTree(t, remoteRoot, "src/a.bin", bytes.Repeat([]byte("a"), 300<<10))
	writeRemoteTree(t, remoteRoot, "src/b.bin", bytes.Repeat([]byte("b"), 100))

	g := backendForTestServer(t, remoteRoot)
	const id = "t17-retry"
	// 每次尝试都在第二个文件上短传失败（a.bin 按名字序在前，会先留下自己的 .part）。
	swapCopyStream(t, func(dstW io.Writer, src io.Reader) (int64, error) {
		return io.Copy(dstW, io.LimitReader(src, 10))
	})
	err := g.GetTree(TransferRequest{ID: id, Host: "h", Remote: "src", Local: dst, Atomic: true}, nil)
	if err == nil {
		t.Fatal("短传必须报错（第一次）")
	}
	if parts := treeParts(t, dst); len(parts) == 0 {
		t.Fatal("第一次失败必须留下 .part（否则本用例没有可清理的现场）")
	}
	// 人为制造一个「上一次运行遗留的」本项 .part：重试必须在开始传送之前把它清掉。
	stale := filepath.Join(dst, "a.bin"+PartMarker+shortID(id)+"-stale1")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 再放一个别的 id 的 .part：绝不能被误删。
	foreign := filepath.Join(dst, "a.bin"+PartMarker+"zzz9-keepme")
	if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = g.GetTree(TransferRequest{ID: id, Host: "h", Remote: "src", Local: dst, Atomic: true}, nil)
	if _, serr := os.Stat(stale); !os.IsNotExist(serr) {
		t.Fatalf("重试前必须清掉本项遗留 .part, stat err=%v", serr)
	}
	if _, serr := os.Stat(foreign); serr != nil {
		t.Fatalf("别的 id 的 .part 不得被误删: %v", serr)
	}
}

// TestGoBackendPutTreeRetryCleansStaleRemoteParts：上传侧对称（远端登记的是 path 家族）。
func TestGoBackendPutTreeRetryCleansStaleRemoteParts(t *testing.T) {
	remoteRoot, src := t.TempDir(), t.TempDir()
	makeLocalTree(t, src, map[string]string{"a/big.bin": string(bytes.Repeat([]byte("x"), 300<<10)), "a/b.bin": "bb"})

	g := backendForTestServer(t, remoteRoot)
	const id = "t17-retry-up"
	swapCopyStream(t, func(dstW io.Writer, srcR io.Reader) (int64, error) {
		return io.Copy(dstW, io.LimitReader(srcR, 10))
	})
	err := g.PutTree(TransferRequest{ID: id, Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, nil)
	if err == nil {
		t.Fatal("短传必须报错（第一次）")
	}
	rootRemote := filepath.Join(remoteRoot, "a")
	stale := filepath.Join(rootRemote, "big.bin"+PartMarker+shortID(id)+"-stale1")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(rootRemote, "big.bin"+PartMarker+"zzz9-keepme")
	if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = g.PutTree(TransferRequest{ID: id, Host: "h", Remote: ".", Local: filepath.Join(src, "a"), Atomic: true}, nil)
	if _, serr := os.Stat(stale); !os.IsNotExist(serr) {
		t.Fatalf("重试前必须清掉本项遗留远端 .part, stat err=%v", serr)
	}
	if _, serr := os.Stat(foreign); serr != nil {
		t.Fatalf("别的 id 的远端 .part 不得被误删: %v", serr)
	}
}

// TestGoBackendGetFailureEmitsFinalFrameWithHonestCounts（I4）：单文件下载失败/短传路径
// 也必须补发强制末帧，且 Done 是**本地 .part 真实落盘字节**（不是 0，也不是读超前的计数）。
func TestGoBackendGetFailureEmitsFinalFrameWithHonestCounts(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	// 三块 32KiB 文件（拷贝实现每次读一块）：短传落在第二块，前一块已真实落盘。
	data := bytes.Repeat([]byte("d"), 3*32<<10)
	writeRemote(t, remoteRoot, "src.bin", data)
	local := filepath.Join(localDir, "dst.bin")
	g := backendForTestServer(t, remoteRoot)
	swapCopyStream(t, func(dstW io.Writer, srcR io.Reader) (int64, error) {
		return io.Copy(dstW, io.LimitReader(srcR, 32<<10)) // 只搬一块就 EOF => done!=total
	})
	sink := &progressSink{}
	err := g.Get(TransferRequest{ID: "t17-i4-get", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, sink.report)
	if err == nil {
		t.Fatal("短传必须报错")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("失败路径必须有进度帧")
	}
	last := frames[len(frames)-1]
	if last.Phase != PhaseTransfer || last.Total != int64(len(data)) {
		t.Fatalf("失败末帧必须是 transfer 相且带远端源大小 %d, got %+v", len(data), last)
	}
	// 修复轮 2：失败末帧的 PartPath 是**保留下来的 .part**（与 te.PartPath 同一锚点），
	// 绝不是已提交目标 local。前端会拿非空 partPath 覆盖 rec.partPath 并给出「清理」，
	// 填最终目标会让清理删掉用户已有的目标文件。
	if last.PartPath == "" || last.PartPath == local {
		t.Fatalf("失败末帧 PartPath 必须是保留的 .part，不得是最终目标 %q，got %q", local, last.PartPath)
	}
	if !IsInternalTemp(filepath.Base(last.PartPath)) {
		t.Fatalf("失败末帧 PartPath 应指向内部 .part，got %q", last.PartPath)
	}
	if te.PartPath != last.PartPath {
		t.Fatalf("失败末帧 PartPath 必须与错误锚点一致: frame=%q err=%q", last.PartPath, te.PartPath)
	}
	if st, serr := os.Stat(last.PartPath); serr != nil || !st.Mode().IsRegular() {
		t.Fatalf("失败末帧 PartPath 必须指向磁盘上仍存在的 .part: %q, stat err=%v", last.PartPath, serr)
	}
	if last.Done != 32<<10 {
		t.Fatalf("失败末帧 Done 必须是 .part 真实落盘字节 %d，got %d", 32<<10, last.Done)
	}
	if last.ID != "t17-i4-get" || last.Direction != DirDownload || last.Name != "src.bin" {
		t.Fatalf("失败末帧字段不完整: %+v", last)
	}
}

// TestGoBackendPutFailureEmitsFinalFrameWithHonestCounts（I4 上传侧）：搬运中途失败时
// Done 取远端 .part 的真实大小（Stat 在 Close 之前），不是 0、也不是读超前的字节数。
func TestGoBackendPutFailureEmitsFinalFrameWithHonestCounts(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	data := bytes.Repeat([]byte("u"), 3*32<<10)
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, data, 0o600); err != nil {
		t.Fatal(err)
	}
	g := backendForTestServer(t, remoteRoot)
	swapCopyStream(t, func(dstW io.Writer, srcR io.Reader) (int64, error) {
		return io.Copy(dstW, io.LimitReader(srcR, 32<<10))
	})
	sink := &progressSink{}
	err := g.Put(TransferRequest{ID: "t17-i4-put", Host: "h", Remote: "dst.bin", Local: local, Atomic: true}, sink.report)
	if err == nil {
		t.Fatal("短传必须报错")
	}
	var te *TransferError
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	frames := sink.all()
	if len(frames) == 0 {
		t.Fatal("失败路径必须有进度帧")
	}
	last := frames[len(frames)-1]
	if last.Phase != PhaseTransfer || last.Total != int64(len(data)) {
		t.Fatalf("失败末帧形状不对（应为 transfer/源大小），got %+v", last)
	}
	// 修复轮 2：失败末帧 PartPath 必须是**保留的远端 .part**（与 te.PartPath 同一锚点），
	// 不是已提交目标 dst.bin —— 否则前端「清理」会去删远端已有文件。
	if last.PartPath == "" || last.PartPath == "dst.bin" {
		t.Fatalf("失败末帧 PartPath 必须是保留的远端 .part，不得是最终目标 %q，got %q", "dst.bin", last.PartPath)
	}
	if !IsInternalTemp(filepath.Base(last.PartPath)) {
		t.Fatalf("失败末帧 PartPath 应指向内部 .part，got %q", last.PartPath)
	}
	if te.PartPath != last.PartPath {
		t.Fatalf("失败末帧 PartPath 必须与错误锚点一致: frame=%q err=%q", last.PartPath, te.PartPath)
	}
	if st, serr := os.Stat(filepath.Join(remoteRoot, last.PartPath)); serr != nil || !st.Mode().IsRegular() {
		t.Fatalf("失败末帧 PartPath 必须指向远端仍存在的 .part: %q, stat err=%v", last.PartPath, serr)
	}
	if last.Done != 32<<10 {
		t.Fatalf("失败末帧 Done 必须是远端 .part 真实落盘字节 %d，got %d", 32<<10, last.Done)
	}
	if last.Direction != DirUpload || last.Name != "dst.bin" {
		t.Fatalf("失败末帧字段不完整: %+v", last)
	}
}

// TestGoBackendFailureFramePartPathRegression（Task 17 修复轮 2）：单文件失败末帧的 PartPath
// 必须是**磁盘上仍存在的 .part**，且绝不等于最终目标；当 .part 根本建不出来时必须是空串，
// 且 Done/Total 都是 0（绝不发表一个暗示可续传的非零 Total）。这是前端 failedActions /
// cleanItem 的安全前提：clean 会拿 partPath 去 DeleteLocal / SftpRemove，指向最终目标 =
// 删用户已有文件（下载）或删远端已有目标（上传）。
func TestGoBackendFailureFramePartPathRegression(t *testing.T) {
	t.Run("get", func(t *testing.T) {
		remoteRoot, localDir := t.TempDir(), t.TempDir()
		writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("g"), 3*32<<10))
		local := filepath.Join(localDir, "dst.bin")
		g := backendForTestServer(t, remoteRoot)
		swapCopyStream(t, func(dst io.Writer, srcR io.Reader) (int64, error) {
			return io.Copy(dst, io.LimitReader(srcR, 32<<10))
		})
		sink := &progressSink{}
		err := g.Get(TransferRequest{ID: "rw2-get", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, sink.report)
		if err == nil {
			t.Fatal("短传必须报错")
		}
		var te *TransferError
		if !errors.As(err, &te) {
			t.Fatalf("应为 *TransferError, got %T: %v", err, err)
		}
		frames := sink.all()
		if len(frames) == 0 {
			t.Fatal("失败路径必须有进度帧")
		}
		last := frames[len(frames)-1]
		if last.PartPath == "" || last.PartPath != te.PartPath {
			t.Fatalf("失败末帧 PartPath 必须等于错误锚点 .part: frame=%q err=%q", last.PartPath, te.PartPath)
		}
		if last.PartPath == local || filepath.Dir(last.PartPath) != localDir {
			t.Fatalf("失败末帧 PartPath 不得是最终目标 %q，且必须与目标同目录: %q", local, last.PartPath)
		}
		if !IsInternalTemp(filepath.Base(last.PartPath)) {
			t.Fatalf("失败末帧 PartPath 应指向内部 .part，got %q", last.PartPath)
		}
		if st, serr := os.Stat(last.PartPath); serr != nil || !st.Mode().IsRegular() {
			t.Fatalf("失败末帧 PartPath 必须指向磁盘上仍存在的 .part: %q, stat err=%v", last.PartPath, serr)
		}
		if _, serr := os.Stat(local); !os.IsNotExist(serr) {
			t.Fatalf("失败后最终目标不得出现，stat err=%v", serr)
		}
	})

	t.Run("put", func(t *testing.T) {
		remoteRoot, localDir := t.TempDir(), t.TempDir()
		local := filepath.Join(localDir, "src.bin")
		if err := os.WriteFile(local, bytes.Repeat([]byte("p"), 3*32<<10), 0o600); err != nil {
			t.Fatal(err)
		}
		g := backendForTestServer(t, remoteRoot)
		swapCopyStream(t, func(dst io.Writer, srcR io.Reader) (int64, error) {
			return io.Copy(dst, io.LimitReader(srcR, 32<<10))
		})
		sink := &progressSink{}
		err := g.Put(TransferRequest{ID: "rw2-put", Host: "h", Remote: "dst.bin", Local: local, Atomic: true}, sink.report)
		if err == nil {
			t.Fatal("短传必须报错")
		}
		var te *TransferError
		if !errors.As(err, &te) {
			t.Fatalf("应为 *TransferError, got %T: %v", err, err)
		}
		frames := sink.all()
		if len(frames) == 0 {
			t.Fatal("失败路径必须有进度帧")
		}
		last := frames[len(frames)-1]
		if last.PartPath == "" || last.PartPath != te.PartPath {
			t.Fatalf("失败末帧 PartPath 必须等于错误锚点 .part: frame=%q err=%q", last.PartPath, te.PartPath)
		}
		if last.PartPath == "dst.bin" || !IsInternalTemp(filepath.Base(last.PartPath)) {
			t.Fatalf("失败末帧 PartPath 不得是最终目标，必须是 .part: %q", last.PartPath)
		}
		if st, serr := os.Stat(filepath.Join(remoteRoot, last.PartPath)); serr != nil || !st.Mode().IsRegular() {
			t.Fatalf("失败末帧 PartPath 必须指向远端仍存在的 .part: %q, stat err=%v", last.PartPath, serr)
		}
		if _, serr := os.Stat(filepath.Join(remoteRoot, "dst.bin")); !os.IsNotExist(serr) {
			t.Fatalf("失败后远端最终目标不得出现，stat err=%v", serr)
		}
	})

	t.Run("no-part", func(t *testing.T) {
		// .part 建不出来（只读父目录）⇒ 失败末帧 PartPath 必须为空，且 Done/Total 都是 0：
		// 前端 failedActions 对空 partPath 只给「重试」，任何非零 Total 都会诱导出续传/清理。
		skipUnlessUnixDirPerms(t)
		remoteRoot := t.TempDir()
		roParent := t.TempDir()
		if err := os.Chmod(roParent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(roParent, 0o700) })
		writeRemote(t, remoteRoot, "src.bin", bytes.Repeat([]byte("n"), 64))
		local := filepath.Join(roParent, "dst.bin")
		g := backendForTestServer(t, remoteRoot)
		sink := &progressSink{}
		err := g.Get(TransferRequest{ID: "rw2-nopart", Host: "h", Remote: "src.bin", Local: local, Atomic: true}, sink.report)
		if err == nil {
			t.Fatal("只读目标必须报错")
		}
		var te *TransferError
		if !errors.As(err, &te) {
			t.Fatalf("应为 *TransferError, got %T: %v", err, err)
		}
		if te.PartPath != "" {
			t.Fatalf(".part 未创建时错误锚点必须为空，got %q", te.PartPath)
		}
		frames := sink.all()
		if len(frames) == 0 {
			t.Fatal("失败路径必须有进度帧（终态必达）")
		}
		last := frames[len(frames)-1]
		if last.PartPath != "" {
			t.Fatalf(".part 未创建时失败末帧 PartPath 必须为空（否则前端会给出续传/清理），got %q", last.PartPath)
		}
		if last.Done != 0 || last.Total != 0 {
			t.Fatalf(".part 未创建时 Done/Total 必须归零（不得暗示可续传），got %d/%d", last.Done, last.Total)
		}
	})
}

// TestGoBackendPutCopyErrorKeepsErrorShape：补发末帧绝不能改变错误语义（PartPath 仍指向
// 保留的远端 .part）。
func TestGoBackendPutCopyErrorKeepsErrorShape(t *testing.T) {
	remoteRoot, localDir := t.TempDir(), t.TempDir()
	local := filepath.Join(localDir, "src.bin")
	if err := os.WriteFile(local, bytes.Repeat([]byte("v"), 64<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	g := backendForTestServer(t, remoteRoot)
	swapCopyStream(t, func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("boom-copy") })
	var te *TransferError
	err := g.Put(TransferRequest{ID: "t17-i4-err", Host: "h", Remote: "dst.bin", Local: local, Atomic: true}, nil)
	if !errors.As(err, &te) {
		t.Fatalf("应为 *TransferError, got %T: %v", err, err)
	}
	if te.PartPath == "" || !IsInternalTemp(filepath.Base(te.PartPath)) {
		t.Fatalf("失败时仍必须给出保留的 .part 锚点, got %q", te.PartPath)
	}
	if te.Err == nil {
		t.Fatal("错误链必须保留原始原因")
	}
}
