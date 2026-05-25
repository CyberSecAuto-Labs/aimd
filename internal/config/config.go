// Package config manages the machine-level aimd configuration file at ~/.aimd/config.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotFound is returned by Load when the config file does not exist.
var ErrNotFound = errors.New("config file not found")

// Config holds the machine-level aimd settings.
type Config struct {
	Remote      string `json:"remote"`
	MachineName string `json:"machineName"`
	LinkMode    string `json:"linkMode"`
}

// DefaultPath returns the default config file path: ~/.aimd/config.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	return filepath.Join(home, ".aimd", "config"), nil
}

// Load reads config from the given path.
// Returns ErrNotFound if the file does not exist.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	return &cfg, nil
}

// Save writes config to the given path atomically (write to temp file, then rename).
// Creates parent directories if needed.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	data = append(data, '\n')

	// Write atomically: temp file in same directory, then rename.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".aimd-config-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	// Ensure temp file is cleaned up on any error.
	var writeErr error
	defer func() {
		if writeErr != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		writeErr = fmt.Errorf("writing config: %w", err)
		_ = tmp.Close()
		return writeErr
	}
	if err := tmp.Close(); err != nil {
		writeErr = fmt.Errorf("closing temp file: %w", err)
		return writeErr
	}
	if err := os.Rename(tmpName, path); err != nil {
		writeErr = fmt.Errorf("renaming config file into place: %w", err)
		return writeErr
	}
	return nil
}
