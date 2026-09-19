package main

import "testing"

func TestUpdateBindingsAreSafeWithoutStartup(t *testing.T) {
	// 测试里直接构造的 App 没走 startup，updater 为 nil；绑定必须返回 disabled 快照而不是 panic。
	a := &App{}
	info := a.GetUpdateInfo()
	if info.State != "disabled" {
		t.Fatalf("state = %s, want disabled", info.State)
	}
	if _, err := a.CheckUpdate(true); err == nil {
		t.Fatal("未初始化时必须返回错误而不是 panic")
	}
	if err := a.StartUpdateDownload(); err == nil {
		t.Fatal("未初始化时必须返回错误")
	}
	if err := a.ApplyUpdateAndRestart(); err == nil {
		t.Fatal("未初始化时必须返回错误")
	}
	if a.CancelUpdateDownload() {
		t.Fatal("未初始化时取消应返回 false")
	}
}
