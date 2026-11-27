package update

import (
	"context"
	"net/http"
	"testing"
)

// F5 回归：describe 构建（v0.7.0-5-gabc）且最新 tag 就是 v0.7.0 时，
// Check 必须判 up-to-date——与 ApplyAndRestart 的口径对齐（Compare 剥后缀
// 比较基线 semver）。旧实现只用 IsRelease 门控，describe 恒走 available，
// 下载成功后 Apply 永远 verify-failed（待安装版本不高于当前版本），
// 推送一个永远装不上的“新版本”。
func TestCheckDescribeUpToDateAgainstOwnTag(t *testing.T) {
	svc, _ := newSvc(t, "v0.7.0-5-gabc", func(o *Options) {
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			return latestOKResponse("v0.7.0"), nil
		})
	})
	info, err := svc.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("Check 不应向调用方报错: %v", err)
	}
	if info.State != StateUpToDate {
		t.Fatalf("describe 基线 == 最新 tag 必须 up-to-date, got %s (latest=%s)", info.State, info.Latest)
	}
	if info.Latest != "v0.7.0" {
		t.Fatalf("Latest = %q, want v0.7.0", info.Latest)
	}
}

// F5 守卫：describe 构建遇到更高的 tag（v0.8.0）仍须 available，
// 且该版本能通过 Apply 的版本序检查（Compare(v0.8.0, v0.7.0-5-gabc) > 0）。
func TestCheckDescribeAvailableForNewerTag(t *testing.T) {
	svc, _ := newSvc(t, "v0.7.0-5-gabc", func(o *Options) {
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			return latestOKResponse("v0.8.0"), nil
		})
	})
	info, err := svc.Check(context.Background(), true)
	if err != nil {
		t.Fatalf("Check 不应向调用方报错: %v", err)
	}
	if info.State != StateAvailable {
		t.Fatalf("更高 tag 必须 available, got %s", info.State)
	}
	if info.Latest != "v0.8.0" {
		t.Fatalf("Latest = %q, want v0.8.0", info.Latest)
	}
}

// F5 守卫：干净 release 的既有口径不变（相等 → up-to-date，更高 → available）。
func TestCheckCleanReleaseUpToDateUnchanged(t *testing.T) {
	svc, _ := newSvc(t, "v0.7.0", func(o *Options) {
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			return latestOKResponse("v0.7.0"), nil
		})
	})
	info, err := svc.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != StateUpToDate {
		t.Fatalf("干净 release 相等必须 up-to-date, got %s", info.State)
	}
}
