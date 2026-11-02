// Package config loads process configuration from environment variables.
package config

import (
	"errors"
	"os"
)

// Config holds the settings shared by the API and worker processes.
type Config struct {
	DatabaseURL string
}

// Load reads the configuration from the environment. It has no defaults, so a
// missing value stops the process instead of pointing it at the wrong database.
func Load() (Config, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return Config{}, errors.New("config: DATABASE_URL is required")
	}
	return Config{DatabaseURL: url}, nil
}
