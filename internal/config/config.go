// Package config loads application settings from the environment and an
// optional .env file.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
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

// loadDotEnv parses simple KEY=VALUE lines. Blank lines and # comments are
// ignored; an optional "export " prefix and surrounding quotes are stripped.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		key, val, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if _, set := os.LookupEnv(key); !set {
			if err := os.Setenv(key, val); err != nil {
				return fmt.Errorf("set %s: %w", key, err)
			}
		}
	}
	return sc.Err()
}
