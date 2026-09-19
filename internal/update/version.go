package update

import (
	"regexp"
	"strconv"
	"strings"
)

// Kind 是版本串的类别。分类必须显式区分「干净 tag」与「git describe 串」：
// Makefile 用 git describe --tags，非 tag 构建会产出 v0.6.0-80-gc2d2a36 这类串，
// 只有干净 tag 才能与 Release tag 做有序比较（spec §4 F8）。
type Kind int

const (
	KindInvalid Kind = iota
	KindClean
	KindDescribe
	KindDev
)

var (
	cleanRe    = regexp.MustCompile(`^v?[0-9]+[.][0-9]+[.][0-9]+$`)
	describeRe = regexp.MustCompile(`^v?[0-9]+[.][0-9]+[.][0-9]+-[0-9]+-g[0-9a-f]+$`)
	// pendingRe 只接受「干净 tag / git describe 串 / dev-<时间戳>」三种版本段形态，
	// 因此 sshore.exe、sshore.v0.7.0.sha256、sshore-update.sh 都不会被误判为候选。
	pendingRe = regexp.MustCompile(`^sshore[.]((?:v?[0-9]+[.][0-9]+[.][0-9]+(?:-[0-9]+-g[0-9a-f]+)?|dev-[0-9]{8}-[0-9]{6}))([.]exe)?$`)
)

// Class 判定版本串类别；空串与 dev 都算开发态。
func Class(v string) Kind {
	v = strings.TrimSpace(v)
	switch {
	case v == "" || v == "dev":
		return KindDev
	case cleanRe.MatchString(v):
		return KindClean
	case describeRe.MatchString(v):
		return KindDescribe
	default:
		return KindInvalid
	}
}

// IsRelease 表示这是可直接与 Release tag 比较的干净版本。
func IsRelease(v string) bool { return Class(v) == KindClean }

// Base 去掉 v 前缀，用于「是否已被用户跳过同一版本」的比对。
func Base(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// Compare 比较主三段版本号；非 semver 输入视为不可比（返回 0）。
func Compare(a, b string) int {
	an, aok := semver(a)
	bn, bok := semver(b)
	if !aok || !bok {
		return 0
	}
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func semver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ParsePendingName 判断文件名是否属于「待安装文件 / 备份」候选并取出版本段。
// 候选的版本段必须以数字或 dev- 开头，因此正式二进制 sshore/sshore.exe、
// sidecar（.sha256）与升级脚本都不会被误判。
func ParsePendingName(name string) (string, bool) {
	m := pendingRe.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}
