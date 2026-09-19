package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 只比较本任务新增的四个字段：既有 Normalize 还会把 Theme 置 system、FontScale 置 1，
// 对完整结构体做 == 会让这些用例全部失败（两阶段评审实测）。
// 区间语义：Normalize 只把 <0 归一为 12；「缺键」的默认值由 DefaultAppConfig/LoadConfig 给，
// 因此 0 表示用户显式关闭轮询（配置文件写了 update_check_interval_hours = 0）。
func TestUpdateSettingsNormalize(t *testing.T) {
	cases := []struct {
		name                    string
		in                      AppSettings
		wantInterval            int
		wantSource, wantSkipVer string
	}{
		{"负间隔回 12", AppSettings{UpdateCheckIntervalHours: -3}, 12, "", ""},
		{"上限 168", AppSettings{UpdateCheckIntervalHours: 999}, 168, "", ""},
		{"0 表显式关闭轮询", AppSettings{UpdateCheckIntervalHours: 0}, 0, "", ""},
		{"非法源清空", AppSettings{UpdateSource: " ftp://x ", UpdateCheckIntervalHours: 12}, 12, "", ""},
		{"合法源去空白保留", AppSettings{UpdateSource: " https://mirror.corp/api ", UpdateCheckIntervalHours: 12}, 12, "https://mirror.corp/api", ""},
		{"跳过版本去 v 与空白", AppSettings{UpdateSkippedVersion: " v0.7.0 ", UpdateCheckIntervalHours: 12}, 12, "", "0.7.0"},
		// fix round 1：spec §6 字符集白名单 [0-9A-Za-z.+-]（正例：合法后缀必须保留）。
		{"跳过版本保留 rc 后缀", AppSettings{UpdateSkippedVersion: "0.7.0-rc.1", UpdateCheckIntervalHours: 12}, 12, "", "0.7.0-rc.1"},
		{"跳过版本保留 build 元数据", AppSettings{UpdateSkippedVersion: "1.2.3+build.5", UpdateCheckIntervalHours: 12}, 12, "", "1.2.3+build.5"},
		// fix round 1：负例（含白名单外字符必须清空，不能把任意输入带进 tag 比较）。
		{"跳过版本含空格清空", AppSettings{UpdateSkippedVersion: "0.7.0 beta", UpdateCheckIntervalHours: 12}, 12, "", ""},
		{"跳过版本含分号清空", AppSettings{UpdateSkippedVersion: ";rm", UpdateCheckIntervalHours: 12}, 12, "", ""},
		{"跳过版本含中文清空", AppSettings{UpdateSkippedVersion: "中文", UpdateCheckIntervalHours: 12}, 12, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.in
			s.Normalize()
			if s.UpdateCheckIntervalHours != c.wantInterval || s.UpdateSource != c.wantSource || s.UpdateSkippedVersion != c.wantSkipVer {
				t.Fatalf("新字段归一错误: interval=%d source=%q skipped=%q", s.UpdateCheckIntervalHours, s.UpdateSource, s.UpdateSkippedVersion)
			}
			if s.Theme != "system" || s.FontScale != 1 {
				t.Fatalf("既有字段归一被破坏: theme=%q fontScale=%v", s.Theme, s.FontScale)
			}
		})
	}
}

func TestDefaultConfigEnablesUpdateCheck(t *testing.T) {
	cfg := DefaultAppConfig()
	if !cfg.App.UpdateCheckAuto {
		t.Fatal("默认必须开启自动检查（否则老配置升级后静默不再检查）")
	}
	// fix round 1：默认间隔也必须钉死为 12，缺了它前端下拉/调度都会拿到 0。
	if cfg.App.UpdateCheckIntervalHours != 12 {
		t.Fatalf("默认轮询间隔必须为 12 小时, got %d", cfg.App.UpdateCheckIntervalHours)
	}
}

// fix round 1：老配置文件只有 [app] 段、不含任何 update_* 键时，
// decode 不得把预先填充的默认值覆盖成零值（否则升级后自动检查静默关闭）。
func TestLoadConfigGivesUpdateDefaultsWhenKeysMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshore.toml")
	content := "[app]\nauto_reconnect_default = false\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.App.UpdateCheckAuto {
		t.Fatal("缺 update_check_auto 键时必须回默认 true（spec §6 默认 true）")
	}
	if cfg.App.UpdateCheckIntervalHours != 12 {
		t.Fatalf("缺 update_check_interval_hours 键时必须回默认 12, got %d", cfg.App.UpdateCheckIntervalHours)
	}
	if cfg.App.AutoReconnectDefault {
		t.Fatal("文件里显式写入的 auto_reconnect_default=false 必须被保留")
	}
}

// fix round 1：钉死「4 个新字段都不加 omitempty」的决定 —— 显式关闭（false/0）
// 必须经 SaveConfig→LoadConfig 往返后仍然保持，不能因零值被省略而回升为默认。
func TestUpdateSettingsRoundTripThroughSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sshore.toml")
	cfg := DefaultAppConfig()
	cfg.App.UpdateCheckAuto = false
	cfg.App.UpdateCheckIntervalHours = 0 // 显式关闭轮询
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.App.UpdateCheckAuto {
		t.Fatal("update_check_auto=false 必须持久化（若加 omitempty 会被省略后回升为 true）")
	}
	if got.App.UpdateCheckIntervalHours != 0 {
		t.Fatalf("update_check_interval_hours=0（显式关闭轮询）必须持久化, got %d", got.App.UpdateCheckIntervalHours)
	}
}
