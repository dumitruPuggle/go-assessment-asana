// Package config loads application settings from the environment and an
// optional .env file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// TokenEnvVar is the environment variable holding the Asana personal access token.
const TokenEnvVar = "ASANA_PERSONAL_ACCESS_TOKEN"

// Config holds runtime settings.
type Config struct {
	AsanaToken string
}

// Load reads envFile (if it exists) into the process environment without
// overriding variables that are already set, then builds a Config.
func Load(envFile string) (Config, error) {
	if err := loadDotEnv(envFile); err != nil {
		return Config{}, err
	}
	token := strings.TrimSpace(os.Getenv(TokenEnvVar))
	if token == "" {
		return Config{}, fmt.Errorf("%s is not set (env or %s)", TokenEnvVar, envFile)
	}
	return Config{AsanaToken: token}, nil
}

// loadDotEnv loads path via godotenv, which never overrides variables that
// are already set. A missing file is not an error.
func loadDotEnv(path string) error {
	if err := godotenv.Load(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("load %s: %w", path, err)
	}
	return nil
}
