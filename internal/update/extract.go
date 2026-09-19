package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
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
// 安全约束：只接受常规文件条目、拒绝路径遍历与符号/硬链接、拒绝多份匹配、拒绝超限条目。
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

func extractTarGz(archive, goos, dest string) error {
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
			if cleanEntry(hdr.Name) == wantName(goos) {
				return fmt.Errorf("归档条目 %q 不是常规文件（拒绝链接/设备）", hdr.Name)
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
		if err := writeExact(dest, tr, hdr.Size); err != nil {
			return err
		}
	}
	if found == 0 {
		return fmt.Errorf("归档里没有找到 %s", wantName(goos))
	}
	return os.Chmod(dest, 0o755)
}

func extractZip(archive, goos, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := 0
	for _, zf := range zr.File {
		if unsafeEntry(zf.Name) {
			return fmt.Errorf("归档条目 %q 含路径遍历，拒绝", zf.Name)
		}
		if cleanEntry(zf.Name) != wantName(goos) {
			continue
		}
		if zf.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("归档条目 %q 是符号链接，拒绝", zf.Name)
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
		err = writeExact(dest, rc, int64(zf.UncompressedSize64))
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	if found == 0 {
		return fmt.Errorf("归档里没有找到 %s", wantName(goos))
	}
	return os.Chmod(dest, 0o755)
}

// writeExact 以 0600 写临时目标并核对字节数，写完再由调用方 chmod。
func writeExact(dest string, r io.Reader, size int64) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, size+1))
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size {
		_ = os.Remove(dest)
		return fmt.Errorf("解包字节数不符：期望 %d 实际 %d", size, n)
	}
	return nil
}
