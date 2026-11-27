package sftp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// —— S2：GetTree 下载到 Windows 时的路径穿越守卫 ——
//
// POSIX 文件名可合法含反斜杠（如单个文件名叫 `..\..\evil`）。scanTree 直接用远端原始
// fi.Name()，GetTree 用 filepath.Join(req.Local, filepath.FromSlash(rel)) 落盘：Windows 上
// FromSlash 不处理 \，Join 的 Clean 把 .. 当目录跳转 ⇒ 文件写到目标目录之外。
// 校验函数带 goos 参数（参照 internal/update/extract.go 的注入风格），便于在 Linux 上测
// Windows 语义。transferGOOS 是 GetTree 使用的目标平台注入点（默认 runtime.GOOS）。

// TestValidateRemoteRelWindowsRejects：goos=windows 时危险分量必须被拒。
func TestValidateRemoteRelWindowsRejects(t *testing.T) {
	for _, rel := range []string{
		`..\..\evil`, // 单个 POSIX 名含反斜杠 + .. 跳转
		`a\b`,        // 反斜杠会被 Windows 当分隔符
		"a/../b",     // .. 分量
		"..",         // 纯 ..
		"a/./b",      // . 分量
		"C:evil",     // 盘符相对路径
		"a:b",        // 冒号（ADS/盘符）
		"a\x01b",     // 控制字符
	} {
		if err := validateRemoteRel(rel, "windows"); err == nil {
			t.Fatalf("goos=windows 应拒绝 %q", rel)
		}
	}
}

// TestValidateRemoteRelLinuxAllowsBackslash：goos=linux 时含反斜杠的合法名不受影响。
func TestValidateRemoteRelLinuxAllowsBackslash(t *testing.T) {
	for _, rel := range []string{`a\b`, `..\..\evil`, "a/b", "normal.txt"} {
		if err := validateRemoteRel(rel, "linux"); err != nil {
			t.Fatalf("goos=linux 不应拒绝合法名 %q: %v", rel, err)
		}
	}
}

// TestValidateRemoteRelNormalTree：正常树（任意平台）不受影响。
func TestValidateRemoteRelNormalTree(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, rel := range []string{"a.txt", "sub/a.txt", "sub/deep/b.bin"} {
			if err := validateRemoteRel(rel, goos); err != nil {
				t.Fatalf("正常树 %q (%s) 不应被拒: %v", rel, goos, err)
			}
		}
	}
}

// TestGetTreeRejectsWindowsPathTraversal：goos=windows 时，远端单个文件名叫 `..\..\evil`
// 必须让整项 GetTree 失败（fail-fast），错误指明问题文件名，且绝不写到目标目录之外。
func TestGetTreeRejectsWindowsPathTraversal(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	srcDir := filepath.Join(remoteRoot, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const evil = `..\..\evil`
	if err := os.WriteFile(filepath.Join(srcDir, evil), []byte("EVIL"), 0600); err != nil {
		t.Fatal(err)
	}

	orig := transferGOOS
	transferGOOS = "windows"
	t.Cleanup(func() { transferGOOS = orig })

	g := backendForTestServer(t, remoteRoot)
	err := g.GetTree(TransferRequest{ID: "s2", Host: "h", Remote: "src", Local: dst, Atomic: true}, nil)
	if err == nil {
		t.Fatal("goos=windows 时 GetTree 必须拒绝含 \\ 与 .. 的远端名")
	}
	if !strings.Contains(err.Error(), evil) {
		t.Fatalf("错误必须指明问题文件名 %q: %v", evil, err)
	}
	// 目标目录之外绝不能被写入。
	if _, serr := os.Stat(filepath.Join(filepath.Dir(dst), "evil")); !os.IsNotExist(serr) {
		t.Fatalf("危险名不得写到目标目录之外: stat err=%v", serr)
	}
}

// TestGetTreeRejectsWindowsBackslashSubdir：危险名出现在**子目录**上同样必须被拒（subdirs 也要校验）。
func TestGetTreeRejectsWindowsBackslashSubdir(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	// 远端子目录名含反斜杠（POSIX 合法）：src/a\b/inner.txt
	badDir := filepath.Join(remoteRoot, "src", `a\b`)
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "inner.txt"), []byte("X"), 0600); err != nil {
		t.Fatal(err)
	}

	orig := transferGOOS
	transferGOOS = "windows"
	t.Cleanup(func() { transferGOOS = orig })

	g := backendForTestServer(t, remoteRoot)
	err := g.GetTree(TransferRequest{ID: "s2dir", Host: "h", Remote: "src", Local: dst, Atomic: true}, nil)
	if err == nil {
		t.Fatal("goos=windows 时 GetTree 必须拒绝含 \\ 的子目录名")
	}
	if !strings.Contains(err.Error(), `a\b`) {
		t.Fatalf("错误必须指明问题子目录名 %q: %v", `a\b`, err)
	}
}

// TestGetTreeLinuxAllowsBackslashNames：goos=linux 时含反斜杠的合法名正常下载（回归守卫）。
func TestGetTreeLinuxAllowsBackslashNames(t *testing.T) {
	remoteRoot, dst := t.TempDir(), t.TempDir()
	srcDir := filepath.Join(remoteRoot, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const name = `a\b.txt`
	if err := os.WriteFile(filepath.Join(srcDir, name), []byte("OK"), 0600); err != nil {
		t.Fatal(err)
	}

	orig := transferGOOS
	transferGOOS = "linux"
	t.Cleanup(func() { transferGOOS = orig })

	g := backendForTestServer(t, remoteRoot)
	if err := g.GetTree(TransferRequest{ID: "s2ok", Host: "h", Remote: "src", Local: dst, Atomic: true}, nil); err != nil {
		t.Fatalf("linux 上含反斜杠的合法名不应被拒: %v", err)
	}
	if b, rerr := os.ReadFile(filepath.Join(dst, name)); rerr != nil || string(b) != "OK" {
		t.Fatalf("下载内容不对: err=%v content=%q", rerr, string(b))
	}
}
