package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFromFile(t *testing.T) {
	t.Setenv(TokenEnvVar, "") // registers cleanup
	os.Unsetenv(TokenEnvVar)

	p := writeEnv(t, "# comment\n\nexport "+TokenEnvVar+"=\"file-token\"\nOTHER=x\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AsanaToken != "file-token" {
		t.Fatalf("token = %q, want file-token", cfg.AsanaToken)
	}
}

func TestEnvOverridesFile(t *testing.T) {
	t.Setenv(TokenEnvVar, "env-token")
	p := writeEnv(t, TokenEnvVar+"=file-token\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AsanaToken != "env-token" {
		t.Fatalf("token = %q, want env-token", cfg.AsanaToken)
	}
}

func TestMissingFileUsesEnv(t *testing.T) {
	t.Setenv(TokenEnvVar, "env-token")
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AsanaToken != "env-token" {
		t.Fatalf("token = %q", cfg.AsanaToken)
	}
}

func TestMissingToken(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	if _, err := Load(filepath.Join(t.TempDir(), "nope.env")); err == nil {
		t.Fatal("expected error when token missing")
	}
}

func TestMalformedLine(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	os.Unsetenv(TokenEnvVar)
	if _, err := Load(writeEnv(t, "NOT_A_PAIR\n")); err == nil {
		t.Fatal("expected parse error")
	}
}
