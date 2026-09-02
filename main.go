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
		Title:  "sshore",
		Width:  1100,
		Height: 720,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			app.Init(func(e forward.Event) {
				runtime.EventsEmit(ctx, "log", e)
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
