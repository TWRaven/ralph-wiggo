// Package config handles loading settings from .ralph-wiggo.yaml and merging
// with CLI flag values. CLI flags take precedence over file values.
package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the settings from .ralph-wiggo.yaml.
type Config struct {
	Model        string   `yaml:"model"`
	MaxBudget    float64  `yaml:"maxBudget"`
	MaxTurns     int      `yaml:"maxTurns"`
	Parallelism  string   `yaml:"parallelism"`
	AllowedTools []string `yaml:"allowedTools"`
	Port         int      `yaml:"port"`
}

// DefaultConfigFile is the name of the config file looked for in the working directory.
const DefaultConfigFile = ".ralph-wiggo.yaml"

// Load reads the config file from the given directory. If the file does not
// exist, it returns a zero-value Config and no error.
func Load(dir string) (Config, error) {
	path := filepath.Join(dir, DefaultConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
