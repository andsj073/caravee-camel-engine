package runlog

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RunStatus values
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusTimeout   = "timeout"
)

// Run represents one execution of an integration.
type Run struct {
	RunID         string `json:"run_id"`
	IntegrationID string `json:"integration_id"`
	EngineID      string `json:"engine_id"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	FinishedAt    string `json:"finished_at,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
	MessageCount  int64  `json:"message_count"`
	ErrorSummary  string `json:"error_summary,omitempty"`
	ErrorStep     string `json:"error_step,omitempty"`
	Revision      int    `json:"revision"`
}

// Store is the run log backed by a JSON file at {dataDir}/runs.json.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore creates a new Store writing to {dataDir}/runs.json.
func NewStore(dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, "runs.json")
	s := &Store{path: path}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := s.save([]Run{}); err != nil {
			return nil, fmt.Errorf("runlog: init: %w", err)
		}
	}
	return s, nil
}

// GenerateRunID returns a unique run identifier.
func GenerateRunID() string {
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck
	return fmt.Sprintf("run-%x-%d", b, time.Now().UnixMilli())
}

func (s *Store) load() ([]Run, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Run{}, nil
		}
		return nil, err
	}
	var runs []Run
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) save(runs []Run) error {
	data, err := json.Marshal(runs)
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

// StartRun records the start of a new run.
func (s *Store) StartRun(run Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.load()
	if err != nil {
		return err
	}
	run.Status = StatusRunning
	if run.StartedAt == "" {
		run.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	runs = append(runs, run)
	return s.save(runs)
}

// CompleteRun marks a run as completed.
func (s *Store) CompleteRun(runID string, messageCount int64, durationMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.load()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, r := range runs {
		if r.RunID == runID {
			runs[i].Status = StatusCompleted
			runs[i].FinishedAt = now
			runs[i].MessageCount = messageCount
			runs[i].DurationMs = durationMs
			break
		}
	}
	return s.save(runs)
}

// FailRun marks a run as failed.
func (s *Store) FailRun(runID string, errorSummary, errorStep string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.load()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, r := range runs {
		if r.RunID == runID {
			runs[i].Status = StatusFailed
			runs[i].FinishedAt = now
			runs[i].ErrorSummary = errorSummary
			runs[i].ErrorStep = errorStep
			break
		}
	}
	return s.save(runs)
}

// UpdateStats updates the message count for a running run without changing its status.
func (s *Store) UpdateStats(runID string, messageCount int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.load()
	if err != nil {
		return err
	}
	for i, r := range runs {
		if r.RunID == runID {
			runs[i].MessageCount = messageCount
			break
		}
	}
	return s.save(runs)
}

// QueryRuns returns runs filtered by integrationID and status (empty = no filter).
// Results are ordered newest-first with pagination applied. Returns (runs, total, error).
func (s *Store) QueryRuns(integrationID string, status string, limit, offset int) ([]Run, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.load()
	if err != nil {
		return nil, 0, err
	}

	// Filter
	filtered := runs[:0:0]
	for _, r := range runs {
		if integrationID != "" && r.IntegrationID != integrationID {
			continue
		}
		if status != "" && r.Status != status {
			continue
		}
		filtered = append(filtered, r)
	}

	total := len(filtered)

	// Reverse: newest first
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}

	// Paginate
	if offset >= len(filtered) {
		return []Run{}, total, nil
	}
	filtered = filtered[offset:]
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, total, nil
}

// Cleanup removes runs older than olderThanDays days.
func (s *Store) Cleanup(olderThanDays int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs, err := s.load()
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -olderThanDays)
	kept := runs[:0]
	for _, r := range runs {
		t, err := time.Parse(time.RFC3339, r.StartedAt)
		if err != nil || t.After(cutoff) {
			kept = append(kept, r)
		}
	}
	return s.save(kept)
}
