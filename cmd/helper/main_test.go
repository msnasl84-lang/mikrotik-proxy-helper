package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
	"github.com/OWNER/mikrotik-proxy-helper/internal/results"
)

func TestParseProfileStableIDIgnoresDisplayName(t *testing.T) {
	a := parseProfile("vless://00000000-0000-0000-0000-000000000001@example.com:443?type=tcp&security=none#first")
	b := parseProfile("vless://00000000-0000-0000-0000-000000000001@example.com:443?type=tcp&security=none#renamed")
	if !a.Supported { t.Fatalf("profile unexpectedly unsupported: %s", a.Reason) }
	if a.ID != b.ID { t.Fatalf("display name changed stable ID: %s != %s", a.ID, b.ID) }
}

func TestInvalidUUIDIsUnsupported(t *testing.T) {
	p := parseProfile("vless://not-a-uuid@example.com:443?type=tcp&security=none")
	if p.Supported { t.Fatal("invalid UUID was accepted") }
}

func TestProfileJSONDoesNotExposeUUIDOrRawLink(t *testing.T) {
	p := parseProfile("vless://00000000-0000-0000-0000-000000000001@example.com:443?type=tcp&security=none")
	b, err := json.Marshal(p)
	if err != nil { t.Fatal(err) }
	text := string(b)
	if strings.Contains(text, p.UUID) || strings.Contains(text, "vless://") { t.Fatalf("profile JSON leaked credentials: %s", text) }
}

func TestPageRefreshesSingleTestStatusAndShowsFailureDetails(t *testing.T) {
	if !strings.Contains(page, "finally{await loadStatus()}") {
		t.Fatal("single-profile test does not refresh status")
	}
	if !strings.Contains(page, "details.error_code") || !strings.Contains(page, "details.error_message") {
		t.Fatal("test diagnostics are not rendered")
	}
	if !strings.Contains(page, "Latency (TTFB)") || !strings.Contains(page, " · Best") {
		t.Fatal("per-profile latency ranking is not rendered")
	}
}

func TestRestoreLastTestsUsesNewestResultPerProfile(t *testing.T) {
	runs := []results.Run{
		{Results: []model.TestResult{{ProfileID: "a", Status: "timeout"}, {ProfileID: "b", Status: "healthy", TTFBMS: 300}}},
		{Results: []model.TestResult{{ProfileID: "a", Status: "healthy", TTFBMS: 120}}},
	}
	last := restoreLastTests(runs)
	if last["a"].Status != "healthy" || last["a"].TTFBMS != 120 { t.Fatalf("unexpected profile a: %+v", last["a"]) }
	if last["b"].TTFBMS != 300 { t.Fatalf("unexpected profile b: %+v", last["b"]) }
}
