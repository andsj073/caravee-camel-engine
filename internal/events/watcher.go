// Package events watches the Camel exchange event file and records
// individual runs from per-exchange JSON lines.
package events

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

)

// ExchangeEvent matches the JSON written by ExchangeEventNotifier.java.
type ExchangeEvent struct {
	Timestamp   string `json:"ts"`
	RouteID     string `json:"routeId"`
	ExchangeID  string `json:"exchangeId"`
	Status      string `json:"status"` // "completed" or "failed"
	DurationMs  int64  `json:"durationMs"`
	BodyPreview string `json:"bodyPreview"`
	Error       string `json:"error"`
}

// RunRecorder is called for each exchange event.
type RunRecorder interface {
	RecordExchangeEvent(event ExchangeEvent)
}

// Watcher tails /data/events/exchanges.jsonl and calls the recorder
// for each new line.
type Watcher struct {
	dataDir  string
	recorder RunRecorder
	stop     chan struct{}
	wg       sync.WaitGroup
}

// NewWatcher creates a watcher for the given data directory.
func NewWatcher(dataDir string, recorder RunRecorder) *Watcher {
	return &Watcher{
		dataDir:  dataDir,
		recorder: recorder,
		stop:     make(chan struct{}),
	}
}

// Start begins watching in a background goroutine.
func (w *Watcher) Start() {
	w.wg.Add(1)
	go w.loop()
}

// Stop halts the watcher and waits for it to finish.
func (w *Watcher) Stop() {
	close(w.stop)
	w.wg.Wait()
}

func (w *Watcher) eventsFile() string {
	return filepath.Join(w.dataDir, "events", "exchanges.jsonl")
}

func (w *Watcher) loop() {
	defer w.wg.Done()

	path := w.eventsFile()
	slog.Info("Exchange event watcher starting", "path", path)

	// Wait for file to appear
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Second)
	}

	// Open and seek to end (only process NEW events)
	f, err := os.Open(path)
	if err != nil {
		slog.Error("Cannot open events file", "error", err)
		return
	}
	defer f.Close()

	// Seek to end so we only process events written after agent starts
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		slog.Warn("Seek to end failed, reading from start", "error", err)
	}

	reader := bufio.NewReader(f)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.readLines(reader, path, f)
		}
	}
}

func (w *Watcher) readLines(reader *bufio.Reader, path string, f *os.File) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				slog.Warn("Error reading events file", "error", err)
			}
			// Check if file was rotated (inode changed)
			info, statErr := os.Stat(path)
			if statErr != nil {
				return
			}
			fInfo, _ := f.Stat()
			if fInfo != nil && !os.SameFile(info, fInfo) {
				// File was rotated — reopen
				slog.Info("Events file rotated, reopening")
				newF, openErr := os.Open(path)
				if openErr != nil {
					slog.Warn("Cannot reopen rotated events file", "error", openErr)
					return
				}
				f.Close()
				*f = *newF
				*reader = *bufio.NewReader(newF)
			}
			return
		}

		if len(line) < 2 {
			continue
		}

		var evt ExchangeEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			slog.Warn("Invalid exchange event line", "error", err, "line", line[:min(len(line), 100)])
			continue
		}

		w.recorder.RecordExchangeEvent(evt)
	}
}
