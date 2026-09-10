package sync

import (
	"strings"
	"testing"

	"sshore/internal/config"
)

func validRule() config.SyncRule {
	return config.SyncRule{ID: "x", Host: "prod-01", Kind: "dir",
		RemotePath: "/srv/conf", LocalPath: "/tmp/conf", MaxDepth: 2, PollIntervalS: 5}
}

func TestValidateSyncRuleAccepts(t *testing.T) {
	if err := ValidateSyncRule(validRule()); err != nil {
		t.Fatalf("合法规则被拒: %v", err)
	}
}

func TestValidateSyncRuleRejects(t *testing.T) {
	cases := map[string]func(*config.SyncRule){
		"host 含注入字符":   func(r *config.SyncRule) { r.Host = "-oProxyCommand=x" },
		"host 为空":      func(r *config.SyncRule) { r.Host = "" },
		"kind 非法":      func(r *config.SyncRule) { r.Kind = "symlink" },
		"远端路径为空":       func(r *config.SyncRule) { r.RemotePath = "" },
		"远端路径含换行":      func(r *config.SyncRule) { r.RemotePath = "/srv/a\nb" },
		"本地路径为空":       func(r *config.SyncRule) { r.LocalPath = "" },
		"max_depth 越界": func(r *config.SyncRule) { r.MaxDepth = 999 },
		"轮询间隔为 0":      func(r *config.SyncRule) { r.PollIntervalS = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := validRule()
			mutate(&r)
			if err := ValidateSyncRule(r); err == nil {
				t.Fatal("必须被拒绝")
			}
		})
	}
}

// 单文件的 mirror_delete 无意义：源消失一律不删本地，校验层显式拒绝以免误会。
func TestValidateSyncRuleRejectsMirrorDeleteForFile(t *testing.T) {
	r := validRule()
	r.Kind = "file"
	r.MirrorDelete = true
	err := ValidateSyncRule(r)
	if err == nil || !strings.Contains(err.Error(), "mirror_delete") {
		t.Fatalf("应拒绝 kind=file 且 mirror_delete=true，得到 %v", err)
	}
}
