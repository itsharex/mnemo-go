package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"mnemo-go/internal/app"
	"mnemo-go/internal/logging"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/windows/icon.ico
var trayIcon []byte

//go:embed build/appicon.png
var appIcon []byte

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--mnemo-preview" {
		host, err := app.ReadPreviewHost(os.Stdin)
		if err != nil {
			return
		}
		err = wails.Run(&options.App{
			Title: "Mnemo 预览", Frameless: true,
			Width: 1100, Height: 720, MinWidth: 480, MinHeight: 360,
			AssetServer:      &assetserver.Options{Assets: assets},
			BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 255},
			Bind:             []interface{}{host},
			OnStartup:        host.Startup,
			OnBeforeClose:    host.BeforeClose,
		})
		if err != nil {
			logging.Error("preview window exited with error", "error", err)
		}
		return
	}
	// 单实例：已有实例时激活其窗口后直接退出
	if !app.AcquireSingleInstance("Mnemo") {
		return
	}
	application := app.NewApp()
	application.WatchShowRequests()
	application.SetupTray(trayIcon)

	err := wails.Run(&options.App{
		Title:     "Mnemo",
		Frameless: true,
		Width:     1440,
		Height:    900,
		MinWidth:  1024,
		MinHeight: 680,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: false,
		},
		BackgroundColour: &options.RGBA{R: 20, G: 24, B: 34, A: 1},
		OnStartup:        application.Startup,
		OnShutdown:       application.Shutdown,
		OnBeforeClose:    application.BeforeClose,
		Bind: []interface{}{
			application,
		},
	})
	if err != nil {
		logging.Error("Wails application exited with error", "error", err)
	}
}
