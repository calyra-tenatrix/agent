package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config represents the agent configuration
type Config struct {
	// TargetToken is the signed token for authenticating with backend
	// Format: <uuid>:<hmac_signature>
	TargetToken string `json:"target_token"`

	// DOToken is the DigitalOcean API token
	DOToken string `json:"do_token"`

	// BackendURL is the gRPC server address (host:port)
	BackendURL string `json:"backend_url"`

	// UpdateCheckInterval is how often to check for updates
	UpdateCheckInterval time.Duration `json:"update_check_interval"`

	// HeartbeatInterval is how often to send heartbeat
	HeartbeatInterval time.Duration `json:"heartbeat_interval"`
}

// rawConfig is used for JSON unmarshaling with string durations
type rawConfig struct {
	TargetToken         string `json:"target_token"`
	DOToken             string `json:"do_token"`
	BackendURL          string `json:"backend_url"`
	UpdateCheckInterval string `json:"update_check_interval"`
	HeartbeatInterval   string `json:"heartbeat_interval"`
}

// Load reads and parses the configuration file
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Validate required fields
	if raw.TargetToken == "" {
		return nil, fmt.Errorf("target_token is required")
	}
	if raw.DOToken == "" {
		return nil, fmt.Errorf("do_token is required")
	}
	if raw.BackendURL == "" {
		return nil, fmt.Errorf("backend_url is required")
	}

	// Parse durations
	updateInterval := 5 * time.Minute
	if raw.UpdateCheckInterval != "" {
		updateInterval, err = time.ParseDuration(raw.UpdateCheckInterval)
		if err != nil {
			return nil, fmt.Errorf("invalid update_check_interval: %w", err)
		}
	}

	heartbeatInterval := 30 * time.Second
	if raw.HeartbeatInterval != "" {
		heartbeatInterval, err = time.ParseDuration(raw.HeartbeatInterval)
		if err != nil {
			return nil, fmt.Errorf("invalid heartbeat_interval: %w", err)
		}
	}

	return &Config{
		TargetToken:         raw.TargetToken,
		DOToken:             raw.DOToken,
		BackendURL:          raw.BackendURL,
		UpdateCheckInterval: updateInterval,
		HeartbeatInterval:   heartbeatInterval,
	}, nil
}
