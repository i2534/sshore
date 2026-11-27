package main

import (
	"fmt"
	"path/filepath"
	stdsync "sync"
	"testing"

	"sshore/internal/config"
	"sshore/internal/forward"
)

// A1 回归：Tunnels/Syncs 的增删改必须与 Bookmarks 同一套锁规约 ——
// 写锁内只构造新 slice（整字段赋值）、锁内无 IO、解锁后再 saveConfig；
// 读点一律锁内快照。否则与 snapshotConfig 浅拷贝共享底层数组的锁外 TOML
// 编码构成数据竞争（-race 可复现），并发 append 还会丢条目。
//
// 修复前：本用例在 -race 下报 DATA RACE（并可能因并发 append 丢条目）。
// 修复后：无竞争报告，且并发创建的 n 条隧道一条不丢。
func TestTunnelAndSyncWritesAreRaceFreeAgainstSaveConfig(t *testing.T) {
	a := NewApp()
	a.Init(func(forward.Event) {})
	a.cfgPath = filepath.Join(t.TempDir(), "sshore.toml")

	const n = 50
	localDir := t.TempDir()
	// 预置 n 条同步规则，供并发 UpdateSyncRule 使用（不同 ID/远端路径，互不冲突）。
	for i := 0; i < n; i++ {
		r := config.SyncRule{
			ID: fmt.Sprintf("s%d", i), Host: "prod", Kind: "dir",
			RemotePath: fmt.Sprintf("/r%d", i), LocalPath: localDir, PollIntervalS: 5,
		}
		if _, err := a.CreateSyncRule(r); err != nil {
			t.Fatalf("预置同步规则 s%d 失败: %v", i, err)
		}
	}

	var wg stdsync.WaitGroup
	wg.Add(5)
	// worker 1：并发创建 n 条隧道（端口互不相同 ⇒ 不触发冲突检查拒绝）。
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			tun := config.Tunnel{
				ID: fmt.Sprintf("t%d", i), Host: "prod", Mode: "local",
				ListenBind: "127.0.0.1", ListenPort: 20000 + i,
				TargetHost: "127.0.0.1", TargetPort: 80,
			}
			if err := a.CreateTunnel(tun); err != nil {
				t.Errorf("并发 CreateTunnel t%d 失败: %v", i, err)
			}
		}
	}()
	// worker 2：创建→更新→删除自己的隧道（Enabled=true 覆盖「运行中先停」分支；
	// forward 引擎里并无这些进程，Stop 幂等返回 nil，不产生真实 ssh 进程）。
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("d%d", i)
			tun := config.Tunnel{
				ID: id, Host: "prod", Mode: "local", Enabled: true,
				ListenBind: "127.0.0.1", ListenPort: 30000 + i,
				TargetHost: "127.0.0.1", TargetPort: 80,
			}
			if err := a.CreateTunnel(tun); err != nil {
				t.Errorf("并发 CreateTunnel %s 失败: %v", id, err)
				continue
			}
			tun.Name = "改名"
			if err := a.UpdateTunnel(tun); err != nil {
				t.Errorf("并发 UpdateTunnel %s 失败: %v", id, err)
			}
			if err := a.DeleteTunnel(id); err != nil {
				t.Errorf("并发 DeleteTunnel %s 失败: %v", id, err)
			}
		}
	}()
	// worker 3：并发改写既有同步规则。
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			r := config.SyncRule{
				ID: fmt.Sprintf("s%d", i), Host: "prod", Kind: "dir",
				RemotePath: fmt.Sprintf("/r%d", i), LocalPath: localDir, PollIntervalS: 5,
				Name: fmt.Sprintf("更新%d", i),
			}
			if err := a.UpdateSyncRule(r); err != nil {
				t.Errorf("并发 UpdateSyncRule s%d 失败: %v", i, err)
			}
		}
	}()
	// worker 4：并发落盘（snapshotConfig 浅拷贝 + 锁外 TOML 编码）。
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := a.saveConfig(); err != nil {
				t.Errorf("并发 saveConfig 失败: %v", err)
			}
		}
	}()
	// worker 5：并发读绑定（读点也必须走锁内快照）。
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_ = a.ListTunnels()
			_ = a.ListSyncRules()
			_ = a.snapshotConfig()
		}
	}()
	wg.Wait()

	// 功能断言：并发创建的 n 条 t* 一条不丢；d* 全部删净；同步规则仍是 n 条。
	got := a.ListTunnels()
	if len(got) != n {
		t.Fatalf("并发创建丢条目：want %d tunnels, got %d", n, len(got))
	}
	seen := map[string]bool{}
	for _, tun := range got {
		seen[tun.ID] = true
	}
	for i := 0; i < n; i++ {
		if !seen[fmt.Sprintf("t%d", i)] {
			t.Fatalf("隧道 t%d 丢失", i)
		}
		if seen[fmt.Sprintf("d%d", i)] {
			t.Fatalf("隧道 d%d 应已被删除", i)
		}
	}
	if rules := a.ListSyncRules(); len(rules) != n {
		t.Fatalf("同步规则数量被并发改动破坏：want %d, got %d", n, len(rules))
	}
}
