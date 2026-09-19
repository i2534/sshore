package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxBinarySize 是单个归档条目的解压上限；测试可临时改小以覆盖越界路径。
var maxBinarySize int64 = 64 << 20

// binaryNames 返回该平台归档里允许提取的条目 basename。
func binaryNames(goos string) []string {
	if goos == "windows" {
		return []string{"sshore.exe"}
	}
	return []string{"sshore"}
}

// ExtractBinary 从 tar.gz / zip 中取出唯一的目标二进制写到 dest，并置 0755。
// 原子性：先写 dest 同目录的独占临时文件，全部校验通过后才 rename 到 dest；
// 任何错误路径都会删掉临时文件与 dest，绝不把半成品留在磁盘上（也不写穿预置在 dest 的符号链接）。
// 安全约束：只接受常规文件条目、拒绝含 .. 或绝对路径的条目、拒绝「目标二进制名」的链接条目、
// 跳过无关链接条目、拒绝多份匹配、拒绝超限条目。
func ExtractBinary(archive, goos, dest string) error {
	if strings.HasSuffix(archive, ".zip") {
		return extractZip(archive, goos, dest)
	}
	return extractTarGz(archive, goos, dest)
}

func wantName(goos string) string {
	names := binaryNames(goos)
	return names[0]
}

// cleanEntry 把归档条目归一成 basename；带 ./ 前缀也会被 Clean 掉（F6 实测）。
func cleanEntry(name string) string {
	return path.Base(path.Clean(strings.ReplaceAll(name, "\\", "/")))
}

// unsafeEntry 判断条目名是否含路径遍历或绝对路径。
// 必须在归一之前判断：path.Base(path.Clean("../sshore")) 会得到 "sshore"，
// 只靠归一化会把遍历条目「洗白」成合法条目（两阶段评审实测）。
func unsafeEntry(name string) bool {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || strings.Contains(n, ":") {
		return true
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// isTargetLink 判断 tar 的链接类条目是否指向目标二进制。
// 策略：只有「目标二进制名」的 symlink/hardlink 才整档拒绝，无关链接条目一律跳过。
// hardlink 是 tar.TypeLink：它的条目名可能无关，但 Linkname 指向目标名时同样算目标条目。
func isTargetLink(hdr *tar.Header, goos string) bool {
	if cleanEntry(hdr.Name) == wantName(goos) {
		return true
	}
	return hdr.Typeflag == tar.TypeLink && cleanEntry(hdr.Linkname) == wantName(goos)
}

// createTemp 在 dest 同目录创建独占临时文件（dest+".tmp-<随机>"，O_CREATE|O_EXCL，0600）。
func createTemp(dest string) (*os.File, error) {
	return os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".tmp-")
}

// commit 把临时文件置 0755 后原子替换为 dest。
// 同目录 rename 只替换目录项，不会写穿预置在 dest 路径上的符号链接。
func commit(out *os.File, dest string) error {
	if err := out.Chmod(0o755); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), dest)
}

func extractTarGz(archive, goos, dest string) (err error) {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	out, err := createTemp(dest)
	if err != nil {
		return err
	}
	// 任何非 nil 返回都清掉临时文件与 dest，保证 dest 要么是完整产物、要么不存在。
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(out.Name())
			_ = os.Remove(dest)
		}
	}()

	tr := tar.NewReader(gz)
	found := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if unsafeEntry(hdr.Name) {
			return fmt.Errorf("归档条目 %q 含路径遍历，拒绝", hdr.Name)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			if isTargetLink(hdr, goos) {
				return fmt.Errorf("归档条目 %q 是目标二进制的链接，拒绝", hdr.Name)
			}
			continue
		}
		if cleanEntry(hdr.Name) != wantName(goos) {
			continue
		}
		found++
		if found > 1 {
			return fmt.Errorf("归档里有多份 %s，拒绝安装", wantName(goos))
		}
		if hdr.Size > maxBinarySize {
			return fmt.Errorf("归档条目过大（%d > %d）", hdr.Size, maxBinarySize)
		}
		if err := writeExact(out, tr, hdr.Size); err != nil {
			return err
		}
	}
	if found == 0 {
		return fmt.Errorf("归档里没有找到 %s", wantName(goos))
	}
	return commit(out, dest)
}

func extractZip(archive, goos, dest string) (err error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()

	out, err := createTemp(dest)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = out.Close()
			_ = os.Remove(out.Name())
			_ = os.Remove(dest)
		}
	}()

	found := 0
	for _, zf := range zr.File {
		if unsafeEntry(zf.Name) {
			return fmt.Errorf("归档条目 %q 含路径遍历，拒绝", zf.Name)
		}
		// 只有目标名条目才需要判定链接；其余条目（含无关 symlink）一律跳过。
		if cleanEntry(zf.Name) != wantName(goos) {
			continue
		}
		if zf.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("归档条目 %q 是目标二进制的符号链接，拒绝", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		found++
		if found > 1 {
			return fmt.Errorf("归档里有多份 %s，拒绝安装", wantName(goos))
		}
		if int64(zf.UncompressedSize64) > maxBinarySize {
			return fmt.Errorf("归档条目过大（%d > %d）", zf.UncompressedSize64, maxBinarySize)
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		writeErr := writeExact(out, rc, int64(zf.UncompressedSize64))
		_ = rc.Close()
		if writeErr != nil {
			return writeErr
		}
	}
	if found == 0 {
		return fmt.Errorf("归档里没有找到 %s", wantName(goos))
	}
	return commit(out, dest)
}

// writeExact 把条目内容原样写入已打开的临时文件并核对字节数。
// 失败时不做删除，清理由调用方的 defer 统一负责。
func writeExact(out *os.File, r io.Reader, size int64) error {
	n, err := io.Copy(out, io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("解包字节数不符：期望 %d 实际 %d", size, n)
	}
	return nil
}
