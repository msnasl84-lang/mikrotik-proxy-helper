package testengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
	"github.com/OWNER/mikrotik-proxy-helper/internal/portpool"
)

func TestAllReportsUnsupportedProfilesWithoutStartingXray(t *testing.T) {
	ports, err := portpool.New(32200, 32201)
	if err != nil { t.Fatal(err) }
	engine := New(Config{RuntimeDir: filepath.Join(t.TempDir(), "runtime"), TotalTimeout: time.Second}, ports)
	profiles := []model.Profile{{ID:"a", Name:"A", Reason:"unsupported transport"}, {ID:"b", Name:"B", Reason:"invalid UUID"}}
	var updates int
	results := engine.TestAll(context.Background(), profiles, func(_, _ int, _ model.TestResult) { updates++ })
	if len(results) != 2 || updates != 2 { t.Fatalf("results=%d updates=%d", len(results), updates) }
	for _, result := range results { if result.Status != "unsupported" { t.Fatalf("unexpected result: %+v", result) } }
}

func TestAllCancellationMarksRemainingProfiles(t *testing.T) {
	ports, err := portpool.New(32210, 32211)
	if err != nil { t.Fatal(err) }
	engine := New(Config{TotalTimeout: time.Second}, ports)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := engine.TestAll(ctx, []model.Profile{{ID:"a"}, {ID:"b"}}, nil)
	if len(results) != 2 { t.Fatalf("got %d results", len(results)) }
	for _, result := range results { if result.Status != "cancelled" { t.Fatalf("unexpected result: %+v", result) } }
}
