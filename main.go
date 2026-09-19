package main

import (
	"context"
	"embed"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"sshore/internal/forward"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:  appTitle(),
		Width:  1100,
		Height: 720,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		// 只开"接收系统文件拖入"；绝不设置 DisableWebViewDrop —— 那会全局关掉
		// webview 的拖放接收，把本源拖入（HTML5 DnD）一起废掉（spec §13 R4）。
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop: true,
		},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			app.Init(func(e forward.Event) {
				runtime.EventsEmit(ctx, "log", e)
			})
			// 系统文件拖入走前端：App.vue 调用 JS 版 runtime.OnFileDrop 注册 webview 的
			// dragover/drop 监听，再由系统拖入分发器交给当前视图。
			// 不要改回 Go 版 runtime.OnFileDrop(ctx, cb) —— 它只订阅 wails:file-drop 事件、
			// 不注册任何 webview 监听，而该事件正是由前端 postMessage 触发的，
			// 单用 Go 版会形成死环：真机上从资源管理器拖文件进来什么都不发生。
			// （2026-09-19 真机复现：面板 drop 只弹 dataTransfer TypeError 红条。）
			runtime.EventsEmit(ctx, "log", forward.Event{
				SourceType: "system",
				SourceID:   "app",
				TS:         time.Now().Format(time.RFC3339),
				Level:      "info",
				Message:    "sshore 已就绪",
			})
			// Wails 的 OnStartup 早于前端页面加载:此时前端尚未订阅 log 事件,
			// 同步 AutoStart 的所有隧道事件(connecting/reconnecting/...)都会
			// 被丢弃,表现为"启动后自动重连无日志"。延迟 1s 再自动开启,
			// 保证日志面板已就绪;启动失败仍可通过规则状态圆点反映。
			go func() {
				time.Sleep(time.Second)
				_ = app.AutoStartEnabled()
			}()
		},
		OnShutdown: func(ctx context.Context) {
			app.OnShutdown()
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
