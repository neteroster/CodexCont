package governor

import (
	"strings"
	"time"
)

type Config struct {
	Enabled            bool   `yaml:"enabled"`
	ExclusiveAuth      bool   `yaml:"exclusive_auth"`
	StateDBPath        string `yaml:"state_db_path"`
	KeyPolicyStatePath string `yaml:"key_policy_state_path"`
	SessionSecret      string `yaml:"session_secret"`
	CodexContEnabled   bool   `yaml:"codexcont_enabled"`
	CodexContRoute     bool   `yaml:"codexcont_route"`
	CodexContURL       string `yaml:"codexcont_url"`
	FailMode           string `yaml:"fail_mode"`
	PollIntervalMS     int    `yaml:"poll_interval_ms"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		ExclusiveAuth:    false,
		StateDBPath:      "cpa-governor.sqlite",
		SessionSecret:    "change-me",
		CodexContEnabled: false,
		CodexContURL:     "http://codexcont:8787",
		FailMode:         "fallback",
		PollIntervalMS:   1500,
	}
}

func (c Config) Normalize() Config {
	if strings.TrimSpace(c.StateDBPath) == "" {
		c.StateDBPath = DefaultConfig().StateDBPath
	}
	c.FailMode = strings.ToLower(strings.TrimSpace(c.FailMode))
	if c.FailMode == "" {
		c.FailMode = "fallback"
	}
	c.CodexContURL = strings.TrimRight(strings.TrimSpace(c.CodexContURL), "/")
	if c.CodexContURL == "" {
		c.CodexContURL = DefaultConfig().CodexContURL
	}
	if c.PollIntervalMS <= 0 {
		c.PollIntervalMS = 1500
	}
	return c
}

func SessionTTL() time.Duration {
	return 24 * time.Hour
}
