package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"aa2hardlinker/internal/config"
	"aa2hardlinker/internal/syncer"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx        context.Context
	syncMu     sync.Mutex
	syncCancel context.CancelFunc
	log        runtimeLogger
}

// Defaults describes the fixed configuration exposed to the UI.
type Defaults struct {
	ApplicationName    string `json:"application_name"`
	ApplicationVersion string `json:"application_version"`
	Destination        string `json:"destination"`
	ManifestURL        string `json:"manifest_url"`
	PathmapURL         string `json:"pathmap_url"`
	TimeoutSeconds     int    `json:"timeout_seconds"`
}

// PrereqStatus reports whether required files/dirs are present.
type PrereqStatus struct {
	OK         bool     `json:"ok"`
	Missing    []string `json:"missing"`
	TargetDir  string   `json:"target_dir"`
	WorkingDir string   `json:"working_dir"`
}

type StepPayload struct {
	Step    string `json:"step"`
	Status  int    `json:"status"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

type ProgressPayload struct {
	Path   string `json:"path"`
	Action int    `json:"action"`
	Index  int    `json:"index"`
	Total  int    `json:"total"`
	Error  string `json:"error,omitempty"`
}

type DonePayload struct {
	Summary string `json:"summary"`
	Error   string `json:"error,omitempty"`
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.log = runtimeLogger{ctx: ctx}
	a.log.Infof("startup")
}

// onSecondInstanceLaunch is called when a second instance of the app is launched while one is already running.
func (a *App) onSecondInstanceLaunch(secondInstanceData options.SecondInstanceData) {
	secondInstanceArgs := secondInstanceData.Args

	runtime.WindowUnminimise(a.ctx)
	runtime.Show(a.ctx)
	go runtime.EventsEmit(a.ctx, "launchArgs", secondInstanceArgs)
	a.log.Infof("second instance launch args=%v", secondInstanceArgs)
}

// GetDefaults returns the preconfigured update values from the legacy CLI.
func (a *App) GetDefaults() Defaults {
	a.log.Debugf("get defaults")
	return Defaults{
		ApplicationName:    config.AppName,
		ApplicationVersion: config.AppVersion,
		Destination:        defaultDestination(),
		ManifestURL:        config.ManifestURL,
		PathmapURL:         config.PathmapURL,
		TimeoutSeconds:     int(config.RequestTimeout.Seconds()),
	}
}

// CheckPrerequisites verifies required paths before running updates.
func (a *App) CheckPrerequisites() PrereqStatus {
	status := PrereqStatus{OK: true}

	wd, err := os.Getwd()
	if err == nil {
		status.WorkingDir = wd
	}

	targetDir := config.TargetDir
	if status.WorkingDir != "" && !filepath.IsAbs(targetDir) {
		targetDir = filepath.Join(status.WorkingDir, targetDir)
	}
	if abs, err := filepath.Abs(targetDir); err == nil {
		targetDir = abs
	}
	status.TargetDir = targetDir

	var missing []string

	if info, err := os.Stat(targetDir); err != nil || !info.IsDir() {
		missing = append(missing, fmt.Sprintf("Missing target directory: %s", targetDir))
	}

	exePath := filepath.Join(status.WorkingDir, "AA2Play.exe")
	if status.WorkingDir == "" {
		exePath = "AA2Play.exe"
	}
	if info, err := os.Stat(exePath); err != nil || info.IsDir() {
		missing = append(missing, fmt.Sprintf("AA2Play.exe not found in %s", status.WorkingDir))
	}

	status.Missing = missing
	status.OK = len(missing) == 0
	if status.OK {
		a.log.Infof("prerequisites ok target=%s", status.TargetDir)
	} else {
		a.log.Warnf("prerequisites missing: %v", status.Missing)
	}
	return status
}

// IsUpdating reports whether a sync job is active.
func (a *App) IsUpdating() bool {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	return a.syncCancel != nil
}

// StartUpdate kicks off the sync workflow and streams events to the frontend.
func (a *App) StartUpdate() (string, error) {
	a.log.Infof("start update requested")

	a.syncMu.Lock()
	if a.syncCancel != nil {
		a.syncMu.Unlock()
		return "", fmt.Errorf("an update is already running")
	}
	a.syncMu.Unlock()

	destination := defaultDestination()
	manifestURL := config.ManifestURL
	pathmapURL := config.PathmapURL
	reqTimeout := config.RequestTimeout

	if err := os.MkdirAll(destination, 0o755); err != nil {
		return "", fmt.Errorf("prepare destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", fmt.Errorf("prepare data dir: %w", err)
	}

	ctx, cancel := context.WithCancel(a.ctx)
	stepCh, progCh, doneCh := syncer.StartWorkflowWithLogger(ctx, destination, manifestURL, "", pathmapURL, reqTimeout, a.log)

	a.syncMu.Lock()
	a.syncCancel = cancel
	a.syncMu.Unlock()

	go a.forwardSteps(stepCh)
	go a.forwardProgress(progCh)
	go a.forwardDone(doneCh)

	a.log.Infof("update started dest=%s manifest=%s pathmap=%s", destination, manifestURL, pathmapURL)
	return "update started", nil
}

// CancelUpdate attempts to stop the running sync.
func (a *App) CancelUpdate() bool {
	a.syncMu.Lock()
	cancel := a.syncCancel
	a.syncMu.Unlock()

	if cancel != nil {
		cancel()
		a.log.Infof("cancel requested")
		return true
	}
	return false
}

func (a *App) forwardSteps(stepCh <-chan syncer.StepEvent) {
	for s := range stepCh {
		msg := s.Message
		if msg == "" {
			msg = statusLabel(s.Status)
		}
		if s.Err != nil {
			a.log.Warnf("step %s %s: %v", s.Step, msg, s.Err)
		} else {
			a.log.Debugf("step %s %s", s.Step, msg)
		}
		runtime.EventsEmit(a.ctx, "syncer:step", StepPayload{
			Step:    string(s.Step),
			Status:  int(s.Status),
			Message: msg,
			Error:   errorToString(s.Err),
		})
	}
}

func (a *App) forwardProgress(progCh <-chan syncer.ProgressEvent) {
	for p := range progCh {
		if p.Err != nil {
			a.log.Warnf("progress error %s: %v", p.Path, p.Err)
		}
		runtime.EventsEmit(a.ctx, "syncer:progress", ProgressPayload{
			Path:   p.Path,
			Action: int(p.Action),
			Index:  p.Index,
			Total:  p.Total,
			Error:  errorToString(p.Err),
		})
	}
}

func (a *App) forwardDone(doneCh <-chan syncer.DoneEvent) {
	for d := range doneCh {
		if d.Err != nil {
			a.log.Errorf("update failed: %v", d.Err)
		} else {
			a.log.Infof("update done: %s", d.Summary)
		}
		runtime.EventsEmit(a.ctx, "syncer:done", DonePayload{
			Summary: d.Summary,
			Error:   errorToString(d.Err),
		})
	}

	a.syncMu.Lock()
	a.syncCancel = nil
	a.syncMu.Unlock()
}

func defaultDestination() string {
	if abs, err := filepath.Abs(config.DownloadsDir); err == nil {
		return abs
	}
	return config.DownloadsDir
}

func errorToString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func statusLabel(status syncer.StepStatus) string {
	switch status {
	case syncer.StatusPending:
		return "Waiting"
	case syncer.StatusRunning:
		return "Running"
	case syncer.StatusDone:
		return "Done"
	case syncer.StatusError:
		return "Error"
	default:
		return ""
	}
}

// runtimeLogger wraps wails runtime logging with timestamps.
type runtimeLogger struct {
	ctx context.Context
}

func (l runtimeLogger) stamp() string {
	return time.Now().Format(time.RFC3339)
}

func (l runtimeLogger) Debugf(format string, args ...interface{}) {
	if l.ctx == nil {
		return
	}
	runtime.LogDebugf(l.ctx, "[%s] "+format, append([]interface{}{l.stamp()}, args...)...)
}

func (l runtimeLogger) Infof(format string, args ...interface{}) {
	if l.ctx == nil {
		return
	}
	runtime.LogInfof(l.ctx, "[%s] "+format, append([]interface{}{l.stamp()}, args...)...)
}

func (l runtimeLogger) Warnf(format string, args ...interface{}) {
	if l.ctx == nil {
		return
	}
	runtime.LogWarningf(l.ctx, "[%s] "+format, append([]interface{}{l.stamp()}, args...)...)
}

func (l runtimeLogger) Errorf(format string, args ...interface{}) {
	if l.ctx == nil {
		return
	}
	runtime.LogErrorf(l.ctx, "[%s] "+format, append([]interface{}{l.stamp()}, args...)...)
}
