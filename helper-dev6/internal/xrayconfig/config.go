package xrayconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
)

func TestConfig(p model.Profile, socksPort int) (map[string]any, error) {
	if !p.Supported || p.Scheme != "vless" || p.Host == "" || p.Port <= 0 || p.UUID == "" {
		return nil, fmt.Errorf("profile is unsupported or incomplete")
	}
	headerType := p.HeaderType
	if headerType == "" { headerType = "none" }
	rawSettings := map[string]any{"header": map[string]any{"type": headerType}}
	if headerType == "http" {
		request := map[string]any{}
		if p.Path != "" { request["path"] = []string{p.Path} }
		rawSettings["header"] = map[string]any{"type": "http", "request": request}
	}
	security := p.Security
	if security == "" { security = "none" }
	return map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag": "probe-socks", "listen": "127.0.0.1", "port": socksPort,
			"protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"tag": "probe-out", "protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{
				"address": p.Host, "port": p.Port,
				"users": []any{map[string]any{"id": p.UUID, "encryption": "none"}},
			}}},
			"streamSettings": map[string]any{"network": "raw", "security": security, "rawSettings": rawSettings},
		}},
		"routing": map[string]any{"domainStrategy": "AsIs", "rules": []any{map[string]any{
			"type": "field", "inboundTag": []string{"probe-socks"}, "outboundTag": "probe-out",
		}}},
	}, nil
}

func WriteTemporary(dir, runID string, cfg map[string]any) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil { return "", err }
	data, err := json.Marshal(cfg)
	if err != nil { return "", err }
	f, err := os.OpenFile(filepath.Join(dir, "probe-"+runID+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil { return "", err }
	path := f.Name()
	if _, err = f.Write(append(data, '\n')); err == nil { err = f.Sync() }
	if closeErr := f.Close(); err == nil { err = closeErr }
	if err != nil { _ = os.Remove(path); return "", err }
	return path, nil
}
