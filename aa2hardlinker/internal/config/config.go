package config

import (
	"path/filepath"
	"time"
)

const (
	AppName     = "AA2 Hardlinker Updater"
	AppVersion  = "0.1.0"
	ManifestURL = "https://cdn-hardlinker.aa2d.net/filelist.json"
	PathmapURL  = "https://cdn-hardlinker.aa2d.net/pathmap.json"
)

var (
	TargetDir      = filepath.Join("data", "texture")
	DownloadsDir   = filepath.Join(TargetDir, "harderlinker")
	RequestTimeout = 2 * time.Minute
)
