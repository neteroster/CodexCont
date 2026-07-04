package executor

import (
	"strings"
	"time"
)

const EncryptedInclude = "reasoning.encrypted_content"
const DefaultCodexUpstreamModel = "gpt-5.3-codex-spark"

type Config struct {
	Enabled               bool              `yaml:"enabled"`
	RouteEnabled          bool              `yaml:"route_enabled"`
	StateDBPath           string            `yaml:"state_db_path"`
	FailMode              string            `yaml:"fail_mode"`
	UpstreamModel         string            `yaml:"upstream_model"`
	UpstreamModelAliases  map[string]string `yaml:"upstream_model_aliases"`
	TruncationStep        int               `yaml:"truncation_step"`
	MaxContinue           int               `yaml:"max_continue"`
	MinN                  int               `yaml:"min_n"`
	MaxN                  int               `yaml:"max_n"`
	MarkerText            string            `yaml:"marker_text"`
	ForceIncludeEncrypted bool              `yaml:"force_include_encrypted"`
	MaxTotalOutputTokens  int               `yaml:"max_total_output_tokens"`
	PollIntervalMS        int               `yaml:"poll_interval_ms"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:      true,
		RouteEnabled: false,
		StateDBPath:  "cpa-codexcont-executor.sqlite",
		FailMode:     "fallback",
		UpstreamModelAliases: map[string]string{
			"gpt-5.4": DefaultCodexUpstreamModel,
			"gpt-5.5": DefaultCodexUpstreamModel,
		},
		TruncationStep:        518,
		MaxContinue:           8,
		MinN:                  1,
		MarkerText:            "Continue thinking...",
		ForceIncludeEncrypted: true,
		PollIntervalMS:        1500,
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
	c.UpstreamModel = strings.TrimSpace(c.UpstreamModel)
	defaultAliases := DefaultConfig().UpstreamModelAliases
	clean := make(map[string]string, len(defaultAliases)+len(c.UpstreamModelAliases))
	for alias, target := range defaultAliases {
		clean[alias] = target
	}
	for alias, target := range c.UpstreamModelAliases {
		alias = strings.TrimSpace(alias)
		target = strings.TrimSpace(target)
		if alias == "" || target == "" {
			continue
		}
		clean[alias] = target
	}
	c.UpstreamModelAliases = clean
	if c.TruncationStep <= 0 {
		c.TruncationStep = DefaultConfig().TruncationStep
	}
	if c.MaxContinue <= 0 {
		c.MaxContinue = DefaultConfig().MaxContinue
	}
	if c.MinN <= 0 {
		c.MinN = DefaultConfig().MinN
	}
	if strings.TrimSpace(c.MarkerText) == "" {
		c.MarkerText = DefaultConfig().MarkerText
	}
	if c.PollIntervalMS <= 0 {
		c.PollIntervalMS = DefaultConfig().PollIntervalMS
	}
	return c
}

func SessionTTL() time.Duration {
	return 24 * time.Hour
}
