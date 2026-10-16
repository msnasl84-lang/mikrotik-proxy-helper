package model

import "time"

type Profile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Scheme       string `json:"scheme"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	UUID         string `json:"-"`
	Type         string `json:"type,omitempty"`
	Security     string `json:"security,omitempty"`
	HeaderType   string `json:"header_type,omitempty"`
	Path         string `json:"path,omitempty"`
	SNI          string `json:"sni,omitempty"`
	RequiredCore string `json:"required_core"`
	Supported    bool   `json:"supported"`
	Reason       string `json:"reason,omitempty"`
	Raw          string `json:"-"`
}

type TestResult struct {
	ServerIPs     []string  `json:"server_ips,omitempty"`
	ServerIPError string    `json:"server_ip_error,omitempty"`
	ExitIP        string    `json:"exit_ip,omitempty"`
	ExitCountry   string    `json:"exit_country,omitempty"`
	IdentityError string    `json:"identity_error,omitempty"`
	RunID         string    `json:"run_id"`
	ProfileID     string    `json:"profile_id"`
	ProfileName   string    `json:"profile_name"`
	Status        string    `json:"status"`
	Stage         string    `json:"stage"`
	EndpointMS    int64     `json:"endpoint_ms,omitempty"`
	TTFBMS        int64     `json:"ttfb_ms,omitempty"`
	TotalMS       int64     `json:"total_ms,omitempty"`
	HTTPStatus    int       `json:"http_status,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	TestedAt      time.Time `json:"tested_at"`
}
