package results

import (
	"path/filepath"
	"testing"
)

func TestStoreKeepsLastRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-results.json")
	s := New(path, 2)
	for _, id := range []string{"one", "two", "three"} {
		if err := s.Save(Run{ID: id}); err != nil { t.Fatal(err) }
	}
	loaded := New(path, 2)
	if err := loaded.Load(); err != nil { t.Fatal(err) }
	runs := loaded.Snapshot()
	if len(runs) != 2 || runs[0].ID != "two" || runs[1].ID != "three" { t.Fatalf("unexpected runs: %+v", runs) }
}
