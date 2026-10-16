package results

import (
	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
	"path/filepath"
	"testing"
)

func TestStoreKeepsLastRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-results.json")
	s := New(path, 2)
	for _, id := range []string{"one", "two", "three"} {
		if err := s.Save(Run{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	loaded := New(path, 2)
	if err := loaded.Load(); err != nil {
		t.Fatal(err)
	}
	runs := loaded.Snapshot()
	if len(runs) != 2 || runs[0].ID != "two" || runs[1].ID != "three" {
		t.Fatalf("unexpected runs: %+v", runs)
	}
}

func TestStoreRestoresProfileIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-results.json")
	store := New(path, 20)
	result := model.TestResult{ProfileID: "profile-a", ServerIPs: []string{"192.0.2.1", "2001:db8::1"}, ExitIP: "203.0.113.2", ExitCountry: "NL", IdentityError: ""}
	if err := store.Save(Run{ID: "identity", Results: []model.TestResult{result}}); err != nil {
		t.Fatal(err)
	}
	restored := New(path, 20)
	if err := restored.Load(); err != nil {
		t.Fatal(err)
	}
	got := restored.Snapshot()[0].Results[0]
	if len(got.ServerIPs) != 2 || got.ServerIPs[1] != "2001:db8::1" || got.ExitIP != result.ExitIP || got.ExitCountry != "NL" {
		t.Fatalf("identity lost: %+v", got)
	}
}
