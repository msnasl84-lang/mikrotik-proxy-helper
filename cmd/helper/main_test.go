package main

import (
	"encoding/json"
	"strings"
	"testing"
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
