package results

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
)

type Run struct {
	ID        string             `json:"id"`
	Status    string             `json:"status"`
	Results   []model.TestResult `json:"results"`
	StartedAt string             `json:"started_at"`
	EndedAt   string             `json:"ended_at,omitempty"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	limit int
	runs  []Run
}

func New(path string, limit int) *Store {
	if limit < 1 { limit = 20 }
	return &Store{path: path, limit: limit}
}

func (s *Store) Load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) { return nil }
	if err != nil { return err }
	var runs []Run
	if err := json.Unmarshal(data, &runs); err != nil { return err }
	if len(runs) > s.limit { runs = runs[len(runs)-s.limit:] }
	s.mu.Lock(); s.runs = runs; s.mu.Unlock()
	return nil
}

func (s *Store) Save(run Run) error {
	s.mu.Lock()
	s.runs = append(s.runs, run)
	if len(s.runs) > s.limit { s.runs = s.runs[len(s.runs)-s.limit:] }
	snapshot := append([]Run(nil), s.runs...)
	s.mu.Unlock()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil { return err }
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil { return err }
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil { return err }
	return os.Rename(tmp, s.path)
}

func (s *Store) Snapshot() []Run {
	s.mu.RLock(); defer s.mu.RUnlock()
	return append([]Run(nil), s.runs...)
}
