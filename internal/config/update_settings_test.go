package config

import "testing"

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
	if !DefaultAppConfig().App.UpdateCheckAuto {
		t.Fatal("默认必须开启自动检查（否则老配置升级后静默不再检查）")
	}
}
