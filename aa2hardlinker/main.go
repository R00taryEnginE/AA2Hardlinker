package main

import (
	"aa2hardlinker/internal/config"
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	logPath := "aa2hardlinker.log"
	_ = os.Remove(logPath) // start fresh each run
	log := logger.NewFileLogger(logPath)
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:              config.AppName,
		Width:              1200,
		Height:             840,
		Logger:             log,
		LogLevelProduction: logger.DEBUG,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "c1ee1577-e028-4736-bbd2-eea90499b751",
			OnSecondInstanceLaunch: app.onSecondInstanceLaunch,
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Error("Error: " + err.Error())
	}
}
