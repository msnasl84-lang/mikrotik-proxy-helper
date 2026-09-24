package xrayconfig

import (
	"encoding/json"
	"testing"

	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
)

func TestProbeBindsOnlyLoopback(t *testing.T) {
	cfg, err := TestConfig(model.Profile{Supported:true, Scheme:"vless", Host:"example.com", Port:443, UUID:"00000000-0000-0000-0000-000000000001", Type:"tcp"}, 12000)
	if err != nil { t.Fatal(err) }
	b, _ := json.Marshal(cfg)
	var decoded struct { Inbounds []struct { Listen string `json:"listen"`; Port int `json:"port"` } `json:"inbounds"` }
	if err := json.Unmarshal(b, &decoded); err != nil { t.Fatal(err) }
	if len(decoded.Inbounds) != 1 || decoded.Inbounds[0].Listen != "127.0.0.1" || decoded.Inbounds[0].Port != 12000 {
		t.Fatalf("unsafe inbound: %+v", decoded.Inbounds)
	}
}
