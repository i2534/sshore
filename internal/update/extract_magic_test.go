package update

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// zipWith 生成含单个常规文件条目的 zip（Windows 资产的真实形态）。
func zipWith(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 2026-10-11 Windows 真机缺陷的回归：下载落点固定是
// <tmp>/sshore-update-<pid>.part（永不带 .zip 后缀，见 download 的实现），而 Windows
// 平台挑中的是 .zip 资产。旧实现按「文件名后缀」分派，于是 zip 被送进 extractTarGz、
// 撞在 gzip.NewReader 上，必现 "gzip: invalid header" —— 且因为 sha256 校验在解包之前
// 且 zip 内容确实与 checksums.txt 一致，校验先通过，用户只会看到这句误导性的报错。
//
// 因此格式判定必须只看内容魔数，与文件名无关。本用例用的正是生产的 part 名。
func TestExtractBinaryDetectsZipUnderProductionPartName(t *testing.T) {
	body := []byte("MZ-fake-windows-exe")
	part := filepath.Join(t.TempDir(), "sshore-update-1234.part")
	if err := os.WriteFile(part, zipWith(t, "sshore.exe", body), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out.exe")
	if err := ExtractBinary(part, "windows", dest); err != nil {
		t.Fatalf("生产 part 名下的 zip 必须按内容识别为 zip: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("zip 提取内容错误: %q %v", got, err)
	}
}

// 同一契约的 tar.gz 侧：linux 资产落在 .part 上必须仍然走 gzip 分支。
func TestExtractBinaryDetectsTarGzUnderProductionPartName(t *testing.T) {
	body := []byte("ELF-fake-linux-bin")
	part := filepath.Join(t.TempDir(), "sshore-update-1234.part")
	if err := os.WriteFile(part, tarGzWith(t, "./sshore", body), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(part, "linux", dest); err != nil {
		t.Fatalf("生产 part 名下的 tar.gz 必须按内容识别为 gzip: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("tar.gz 提取内容错误: %q %v", got, err)
	}
}

// 2026-10-11 次级要求：既不是 gzip 也不是 zip 的内容（代理错误页、被替换的下载等）必须
// 报出可诊断的错误，而不是把 gzip 库的 "gzip: invalid header" 直接甩给用户 —— 那句话正是
// 本次误诊的根源（它让人以为是源产物坏了/下载损坏，实际是格式判定用错了信号）。
func TestExtractBinaryRejectsUnknownArchiveWithClearError(t *testing.T) {
	part := filepath.Join(t.TempDir(), "sshore-update-1234.part")
	if err := os.WriteFile(part, []byte("<html><body>proxy error</body></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	err := ExtractBinary(part, "linux", dest)
	if err == nil {
		t.Fatal("无法识别的归档必须报错")
	}
	if strings.Contains(err.Error(), "invalid header") {
		t.Fatalf("不得再暴露 gzip 库的原始错误（gzip: invalid header）: %v", err)
	}
	if !strings.Contains(err.Error(), "既不是 gzip 也不是 zip") {
		t.Fatalf("错误信息应说明是格式无法识别, got %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("错误返回后 dest 必须不存在（stat: %v）", statErr)
	}
	if ents := dirEntries(t, filepath.Dir(dest)); len(ents) != 0 {
		t.Fatalf("错误返回后不得留残余文件: %v", ents)
	}
}

// 端到端回归：Windows 平台走完整 StartDownload（下载 → sha256 → 解包 → sidecar → ready）。
// 修复前这里必然停在 io-failed，错误即 "gzip: invalid header"。
func TestDownloadWindowsZipEndToEndUnderProductionPartName(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore.exe")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("MZ-NEW-EXE-v0.7.0\x00\x01")
	const archiveName = "sshore-v0.7.0-windows-amd64.zip"
	zipBytes := zipWith(t, "sshore.exe", body)
	srv := fakeSource(t, "v0.7.0", archiveName, zipBytes, sha256Hex(zipBytes))

	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.Goos = "windows"
		o.ExePath = exe
		o.Doer = srv.Client()
		o.Config = func() Settings { return Settings{Source: srv.URL} }
	})

	_ = os.Remove(partPath())
	if _, err := svc.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := svc.Info().State; got != StateAvailable {
		t.Fatalf("check state = %s, want available", got)
	}
	if err := svc.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	info := waitState(t, svc, StateReady, 5*time.Second)

	plan := PlanFor("windows", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if info.ReadyPath != plan.Pending {
		t.Fatalf("ready_path = %s, want %s", info.ReadyPath, plan.Pending)
	}
	got, err := os.ReadFile(plan.Pending)
	if err != nil {
		t.Fatalf("pending 必须存在: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("pending 内容与 fixture 不符: %q", got)
	}
	sidecar, err := os.ReadFile(plan.Sidecar)
	if err != nil {
		t.Fatalf("sidecar 必须存在: %v", err)
	}
	m, err := ParseChecksums(bytes.NewReader(sidecar))
	if err != nil {
		t.Fatalf("sidecar 不可解析: %v", err)
	}
	if want := sha256Hex(body); m[filepath.Base(plan.Pending)] != want {
		t.Fatalf("sidecar 哈希不符: got %q want %q", m[filepath.Base(plan.Pending)], want)
	}
	waitGone(t, partPath(), 3*time.Second)
	if names := strings.Join(dirEntries(t, dir), ","); names != "sshore.exe,sshore.v0.7.0.exe,sshore.v0.7.0.exe.sha256" {
		t.Fatalf("ExeDir 残留不符: %s", names)
	}
}
