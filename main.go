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
			// 系统文件拖入：Wails 只提供拖入 API（无拖出）。这里把 (x, y, paths)
			// 转给前端，由前端按落点坐标决定目标面板（本地面板=复制，远程面板=上传）。
			runtime.OnFileDrop(ctx, func(x, y int, paths []string) {
				runtime.EventsEmit(ctx, "files:dropped", map[string]any{
					"x": x, "y": y, "paths": paths,
				})
			})
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
