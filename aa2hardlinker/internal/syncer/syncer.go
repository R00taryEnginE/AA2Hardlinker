package syncer

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aa2hardlinker/internal/config"
)

// FileList mirrors the manifest format served by the CDN.
type FileList struct {
	Generated time.Time   `json:"generated"`
	BaseURL   string      `json:"base_url"`
	Files     []FileEntry `json:"files"`
}

// FileEntry describes a single asset.
type FileEntry struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	MD5      string    `json:"md5"`
	Modified time.Time `json:"modified"`
}

// ProgressEvent streams per-file state to the UI.
type ProgressCode int

const (
	ProgressUnknown ProgressCode = iota
	ProgressPresent
	ProgressDownloaded
	ProgressVerified
	ProgressLinked
	ProgressError
)

type ProgressEvent struct {
	Path   string
	Action ProgressCode
	Index  int
	Total  int
	Err    error
}

// DoneEvent finalizes the workflow.
type DoneEvent struct {
	Summary string
	Err     error
}

// Logger captures structured logs; implementations may be no-op.
type Logger interface {
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type StepName string

type StepStatus int

type StepEvent struct {
	Step    StepName
	Status  StepStatus
	Message string
	Err     error
}

const (
	StepFetchManifest StepName = "fetch_manifest"
	StepCheckLocal    StepName = "check_local"
	StepDownload      StepName = "download"
	StepIntegrity     StepName = "integrity"
	StepSymlink       StepName = "symlink"
)

const (
	StatusPending StepStatus = iota
	StatusRunning
	StatusDone
	StatusError
)

type PathMapEntry struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// pathLocks serializes operations per destination path to avoid concurrent writes on Windows.
var pathLocks sync.Map

func lockFor(path string) *sync.Mutex {
	if v, ok := pathLocks.Load(path); ok {
		return v.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := pathLocks.LoadOrStore(path, mu)
	return actual.(*sync.Mutex)
}

func (r *workflowRunner) emitProgress(evt ProgressEvent) {
	r.progCh <- evt
}

func (r *workflowRunner) logDebug(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Debugf(format, args...)
	}
}

func (r *workflowRunner) logInfo(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Infof(format, args...)
	}
}

func (r *workflowRunner) logWarn(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Warnf(format, args...)
	}
}

func (r *workflowRunner) logError(format string, args ...interface{}) {
	if r.logger != nil {
		r.logger.Errorf(format, args...)
	}
}

// StartWorkflow runs the full update flow and emits step/progress events.
// The provided context controls cancellation; requestTimeout applies per network request.
func StartWorkflow(ctx context.Context, dest, manifestURL, baseURL, pathmapURL string, requestTimeout time.Duration) (<-chan StepEvent, <-chan ProgressEvent, <-chan DoneEvent) {
	return StartWorkflowWithLogger(ctx, dest, manifestURL, baseURL, pathmapURL, requestTimeout, nil)
}

// StartWorkflowWithLogger runs the full update flow with optional logging.
func StartWorkflowWithLogger(ctx context.Context, dest, manifestURL, baseURL, pathmapURL string, requestTimeout time.Duration, logger Logger) (<-chan StepEvent, <-chan ProgressEvent, <-chan DoneEvent) {
	stepCh := make(chan StepEvent, 16)
	progCh := make(chan ProgressEvent, 256)
	doneCh := make(chan DoneEvent, 1)

	go func() {
		defer close(stepCh)
		defer close(progCh)
		defer close(doneCh)

		runner := &workflowRunner{
			dest:           dest,
			manifestURL:    manifestURL,
			baseURL:        baseURL,
			pathmapURL:     pathmapURL,
			requestTimeout: requestTimeout,
			parallel:       runtime.NumCPU(),
			stepCh:         stepCh,
			progCh:         progCh,
			logger:         logger,
		}

		summary, err := runner.run(ctx)
		doneCh <- DoneEvent{Summary: summary, Err: err}
	}()

	return stepCh, progCh, doneCh
}

// StartWithTimeout keeps backward compatibility for callers that only need progress + done.
func StartWithTimeout(dest, manifestURL, baseURL string, timeout time.Duration) (<-chan ProgressEvent, <-chan DoneEvent) {
	stepCh, progCh, doneCh := StartWorkflowWithLogger(context.Background(), dest, manifestURL, baseURL, config.PathmapURL, timeout, nil)
	// Drain step events to avoid blocking.
	go func() {
		for range stepCh {
		}
	}()
	return progCh, doneCh
}

type workflowRunner struct {
	dest           string
	manifestURL    string
	baseURL        string
	pathmapURL     string
	requestTimeout time.Duration
	parallel       int

	stepCh chan<- StepEvent
	progCh chan<- ProgressEvent
	logger Logger
}

func (r *workflowRunner) run(parent context.Context) (string, error) {
	ctx := parent
	r.logInfo("start workflow")

	// Step: manifest
	r.emitStep(StepFetchManifest, StatusRunning, "", nil)
	manifest, err := fetchFileList(ctx, r.manifestURL, r.requestTimeout)
	if err != nil {
		r.emitStep(StepFetchManifest, StatusError, "", err)
		r.logError("fetch manifest: %v", err)
		return "", fmt.Errorf("fetch manifest: %w", err)
	}
	baseURL := manifest.BaseURL
	if baseURL == "" {
		baseURL = r.baseURL
	}
	if baseURL == "" {
		r.emitStep(StepFetchManifest, StatusError, "", fmt.Errorf("manifest base_url required"))
		r.logError("manifest base_url required")
		return "", fmt.Errorf("manifest base_url required")
	}
	r.emitStep(StepFetchManifest, StatusDone, "", nil)
	r.logDebug("manifest files=%d base=%s", len(manifest.Files), baseURL)

	// Step: local check
	r.emitStep(StepCheckLocal, StatusRunning, "", nil)
	missing, present, err := r.checkLocal(ctx, manifest.Files)
	if err != nil {
		r.emitStep(StepCheckLocal, StatusError, "", err)
		r.logError("check local: %v", err)
		return "", err
	}
	r.emitStep(StepCheckLocal, StatusDone, "", nil)

	// Step: download
	r.emitStep(StepDownload, StatusRunning, "", nil)
	downloaded, err := r.downloadMissing(ctx, baseURL, missing)
	if err != nil {
		r.emitStep(StepDownload, StatusError, "", err)
		r.logError("download: %v", err)
		return "", err
	}
	r.emitStep(StepDownload, StatusDone, "", nil)

	// Step: integrity
	r.emitStep(StepIntegrity, StatusRunning, "", nil)
	if err := r.verifyAll(ctx, manifest.Files); err != nil {
		r.emitStep(StepIntegrity, StatusError, "", err)
		r.logError("integrity: %v", err)
		return "", err
	}
	r.emitStep(StepIntegrity, StatusDone, "", nil)

	// Step: symlink
	r.emitStep(StepSymlink, StatusRunning, "", nil)
	if err := r.applyPathmap(ctx); err != nil {
		r.emitStep(StepSymlink, StatusError, "", err)
		r.logError("symlink: %v", err)
		return "", err
	}
	r.emitStep(StepSymlink, StatusDone, "", nil)

	summary := fmt.Sprintf("Checked %d files (%d present, %d downloaded)", len(manifest.Files), present, downloaded)
	r.logInfo("workflow done: %s", summary)
	return summary, nil
}

func (r *workflowRunner) emitStep(step StepName, status StepStatus, msg string, err error) {
	r.stepCh <- StepEvent{Step: step, Status: status, Message: msg, Err: err}
}

func (r *workflowRunner) checkLocal(ctx context.Context, files []FileEntry) ([]FileEntry, int, error) {
	missing := make([]FileEntry, 0, len(files))
	present := 0

	for i, f := range files {
		select {
		case <-ctx.Done():
			return missing, present, ctx.Err()
		default:
		}

		localPath := filepath.Join(r.dest, filepath.FromSlash(f.Path))
		info, err := os.Stat(localPath)
		if err == nil && info.Size() == f.Size {
			ok, md5Err := verifyMD5(localPath, f.MD5)
			if md5Err == nil && ok {
				present++
				r.emitProgress(ProgressEvent{Path: f.Path, Action: ProgressPresent, Index: i + 1, Total: len(files)})
				continue
			}
		}
		missing = append(missing, f)
	}

	return missing, present, nil
}

func (r *workflowRunner) downloadMissing(ctx context.Context, baseURL string, files []FileEntry) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}

	workerCount := r.parallel
	if workerCount < 2 {
		workerCount = 2
	}
	if workerCount > len(files) {
		workerCount = len(files)
	}

	jobs := make(chan FileEntry)
	var doneCount int32
	var downloaded int32
	var firstErr atomic.Value
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for f := range jobs {
			localPath := filepath.Join(r.dest, filepath.FromSlash(f.Path))
			action, err := ensurePresent(ctx, baseURL, f, localPath, r.requestTimeout)
			completed := int(atomic.AddInt32(&doneCount, 1))

			if err != nil {
				if firstErr.Load() == nil {
					firstErr.Store(err)
				}
				r.emitProgress(ProgressEvent{Path: f.Path, Action: ProgressError, Index: completed, Total: len(files), Err: err})
				r.logWarn("download error %s: %v", f.Path, err)
				continue
			}

			if action != ProgressPresent {
				atomic.AddInt32(&downloaded, 1)
			}
			r.emitProgress(ProgressEvent{Path: f.Path, Action: action, Index: completed, Total: len(files)})
		}
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}

	for _, f := range files {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return int(downloaded), ctx.Err()
		case jobs <- f:
		}
	}
	close(jobs)

	wg.Wait()

	if errVal := firstErr.Load(); errVal != nil {
		return int(downloaded), errVal.(error)
	}

	return int(downloaded), nil
}

func (r *workflowRunner) verifyAll(ctx context.Context, files []FileEntry) error {
	for i, f := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		localPath := filepath.Join(r.dest, filepath.FromSlash(f.Path))
		ok, err := verifyMD5(localPath, f.MD5)
		if err != nil {
			r.logError("verify %s: %v", f.Path, err)
			return fmt.Errorf("verify %s: %w", f.Path, err)
		}
		if !ok {
			err := fmt.Errorf("checksum mismatch: %s", f.Path)
			r.logError(err.Error())
			return fmt.Errorf("checksum mismatch: %s", f.Path)
		}
		info, err := os.Stat(localPath)
		if err != nil {
			r.logError("stat %s: %v", f.Path, err)
			return fmt.Errorf("stat %s: %w", f.Path, err)
		}
		if info.Size() != f.Size {
			err := fmt.Errorf("size mismatch %s: got %d want %d", f.Path, info.Size(), f.Size)
			r.logError(err.Error())
			return fmt.Errorf("size mismatch %s: got %d want %d", f.Path, info.Size(), f.Size)
		}

		r.emitProgress(ProgressEvent{Path: f.Path, Action: ProgressVerified, Index: i + 1, Total: len(files)})
	}
	return nil
}

func (r *workflowRunner) applyPathmap(ctx context.Context) error {
	pathmapURL := r.pathmapURL
	if pathmapURL == "" {
		pathmapURL = config.PathmapURL
	}

	entries, err := fetchPathmap(ctx, pathmapURL, r.requestTimeout)
	if err != nil {
		r.logError("fetch pathmap: %v", err)
		return err
	}

	dataDir := filepath.Dir(r.dest)

	for i, e := range entries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		src := filepath.FromSlash(e.Source)
		dst := filepath.Join(dataDir, filepath.FromSlash(e.Target))

		if err := os.RemoveAll(dst); err != nil && !os.IsNotExist(err) {
			r.logWarn("remove old link %s: %v", dst, err)
			return fmt.Errorf("remove old link %s: %w", dst, err)
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			r.logError("mkdir for link %s: %v", dst, err)
			return fmt.Errorf("mkdir for link %s: %w", dst, err)
		}

		if err := os.Symlink(src, dst); err != nil {
			r.logError("create symlink %s -> %s: %v", dst, src, err)
			return fmt.Errorf("create symlink %s -> %s: %w", dst, src, err)
		}

		r.emitProgress(ProgressEvent{Path: e.Target, Action: ProgressLinked, Index: i + 1, Total: len(entries)})
	}

	return nil
}

func fetchFileList(ctx context.Context, manifestURL string, requestTimeout time.Duration) (FileList, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return FileList{}, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return FileList{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return FileList{}, fmt.Errorf("manifest fetch status %s", resp.Status)
	}

	var manifest FileList
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&manifest); err != nil {
		return FileList{}, fmt.Errorf("decode manifest: %w", err)
	}

	return manifest, nil
}

func ensurePresent(ctx context.Context, baseURL string, entry FileEntry, dest string, requestTimeout time.Duration) (ProgressCode, error) {
	lock := lockFor(dest)
	lock.Lock()
	defer lock.Unlock()

	info, err := os.Stat(dest)
	if err == nil {
		if info.Size() == entry.Size {
			ok, err := verifyMD5(dest, entry.MD5)
			if err == nil && ok {
				return ProgressPresent, nil
			}
		}
	}

	return downloadFile(ctx, baseURL, entry, dest, requestTimeout)
}

func downloadFile(ctx context.Context, baseURL string, entry FileEntry, dest string, requestTimeout time.Duration) (ProgressCode, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return ProgressError, fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}

	fullURL, err := url.JoinPath(baseURL, entry.Path)
	if err != nil {
		return ProgressError, fmt.Errorf("build url: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fullURL, nil)
	if err != nil {
		return ProgressError, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ProgressError, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ProgressError, fmt.Errorf("status %s", resp.Status)
	}

	file, err := os.Create(dest)
	if err != nil {
		return ProgressError, err
	}
	defer file.Close()

	n, err := io.Copy(file, resp.Body)
	if err != nil {
		return ProgressError, err
	}

	if n != entry.Size {
		return ProgressError, fmt.Errorf("downloaded size mismatch: got %d want %d", n, entry.Size)
	}

	return ProgressDownloaded, nil
}

func fetchPathmap(ctx context.Context, pathmapURL string, requestTimeout time.Duration) ([]PathMapEntry, error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, pathmapURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pathmap fetch status %s", resp.Status)
	}

	var entries []PathMapEntry
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode pathmap: %w", err)
	}

	return entries, nil
}

func verifyMD5(path string, expected string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, f); err != nil {
		return false, err
	}

	sum := hex.EncodeToString(hash.Sum(nil))
	return strings.EqualFold(sum, expected), nil
}
