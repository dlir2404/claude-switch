package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	LLM     LLMConfig     `json:"llm"`
	Handoff HandoffConfig `json:"handoff"`
}

type LLMConfig struct {
	Enabled        bool    `json:"enabled"`
	Provider       string  `json:"provider"`
	BaseURL        string  `json:"base_url"`
	APIKey         string  `json:"api_key"`
	Model          string  `json:"model"`
	Temperature    float64 `json:"temperature"`
	MaxTokens      int     `json:"max_tokens"`
	TimeoutSeconds int     `json:"timeout_seconds"`
}

type HandoffConfig struct {
	IncludeArtifacts         bool          `json:"include_artifacts"`
	MaxRecentMessages        int           `json:"max_recent_messages"`
	MaxInputTokens           int           `json:"max_input_tokens"`
	FilterThinkingBlocks     bool          `json:"filter_thinking_blocks"`
	TruncateToolResultsChars int           `json:"truncate_tool_results_chars"`
	SaveDir                  string        `json:"save_dir"`
	Pruning                  PruningConfig `json:"pruning"`
}

type PruningConfig struct {
	Enabled        bool `json:"enabled"`
	MaxSnapshots   int  `json:"max_snapshots"`
	RetentionDays  int  `json:"retention_days"`
	MaxTotalSizeMB int  `json:"max_total_size_mb"`
	ProtectLatest  bool `json:"protect_latest"`
}

func GetBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude-switch")
}

func GetProfilesDir() string {
	base := GetBaseDir()
	newDir := filepath.Join(base, "profiles")
	// If newDir doesn't exist yet, check legacy ~/.claude-desktop-profiles
	if _, err := os.Stat(newDir); os.IsNotExist(err) {
		home, _ := os.UserHomeDir()
		legacy := filepath.Join(home, ".claude-desktop-profiles")
		if info, err := os.Stat(legacy); err == nil && info.IsDir() {
			return legacy
		}
	}
	return newDir
}

func GetHandoffsDir() string {
	return filepath.Join(GetBaseDir(), "handoffs")
}

func GetConfigPath() string {
	return filepath.Join(GetBaseDir(), "config.json")
}

func DefaultConfig() *Config {
	return &Config{
		LLM: LLMConfig{
			Enabled:        true,
			Provider:       "openai_compatible",
			BaseURL:        "http://127.0.0.1:20128/v1",
			APIKey:         "env:OPENAI_API_KEY",
			Model:          "gpt-4o-mini",
			Temperature:    0.2,
			MaxTokens:      4096,
			TimeoutSeconds: 30,
		},
		Handoff: HandoffConfig{
			IncludeArtifacts:         true,
			MaxRecentMessages:        0,
			MaxInputTokens:           64000,
			FilterThinkingBlocks:     true,
			TruncateToolResultsChars: 800,
			SaveDir:                  GetHandoffsDir(),
			Pruning: PruningConfig{
				Enabled:        true,
				MaxSnapshots:   20,
				RetentionDays:  14,
				MaxTotalSizeMB: 50,
				ProtectLatest:  true,
			},
		},
	}
}

func (c *Config) GetResolvedAPIKey() string {
	key := strings.TrimSpace(c.LLM.APIKey)
	if strings.HasPrefix(key, "env:") {
		envVar := strings.TrimPrefix(key, "env:")
		return os.Getenv(envVar)
	}
	if key != "" {
		return key
	}
	if envKey := os.Getenv("CLAUDE_SWITCH_API_KEY"); envKey != "" {
		return envKey
	}
	return os.Getenv("OPENAI_API_KEY")
}

func LoadConfig() (*Config, error) {
	cfgPath := GetConfigPath()
	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		cfg := DefaultConfig()
		_ = SaveConfig(cfg)
		return cfg, nil
	} else if err != nil {
		return DefaultConfig(), err
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return DefaultConfig(), err
	}
	if cfg.Handoff.SaveDir == "" {
		cfg.Handoff.SaveDir = GetHandoffsDir()
	}
	return cfg, nil
}

func SaveConfig(cfg *Config) error {
	dir := filepath.Dir(GetConfigPath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	cfgPath := GetConfigPath()
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		return err
	}
	return os.Chmod(cfgPath, 0600)
}
