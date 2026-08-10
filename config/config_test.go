package config

import (
	"strings"
	"testing"
)

var configEnvironmentKeys = []string{
	"CONCH_PORT",
	"CONCH_HOST",
	"CONCH_API_KEY",
	"CONCH_TIMEOUT",
	"CONCH_MAX_TIMEOUT",
	"CONCH_ALLOW_NO_AUTH",
	"CONCH_JOB_DIR",
	"CONCH_JOB_RETENTION_HOURS",
	"CONCH_MAX_JOB_TIMEOUT_SECONDS",
	"CONCH_MAX_JOB_OUTPUT_BYTES",
	"CONCH_MAX_JOBS",
	"CONCH_TRUSTED_PROXIES",
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range configEnvironmentKeys {
		t.Setenv(key, "")
	}
}

func TestLoadRejectsMalformedInteger(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("CONCH_PORT", "not-a-port")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CONCH_PORT") {
		t.Fatalf("Load error = %v, want CONCH_PORT validation", err)
	}
}

func TestLoadRejectsUnsafeLimits(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("CONCH_MAX_JOB_OUTPUT_BYTES", "0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CONCH_MAX_JOB_OUTPUT_BYTES") {
		t.Fatalf("Load error = %v, want output limit validation", err)
	}
}

func TestLoadRejectsInvalidTrustedProxy(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("CONCH_TRUSTED_PROXIES", "not-an-ip")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CONCH_TRUSTED_PROXIES") {
		t.Fatalf("Load error = %v, want trusted proxy validation", err)
	}
}

func TestLoadParsesTrustedProxyList(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("CONCH_TRUSTED_PROXIES", "127.0.0.1, 10.0.0.0/8")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[1] != "10.0.0.0/8" {
		t.Fatalf("trusted proxies = %#v", cfg.TrustedProxies)
	}
}
