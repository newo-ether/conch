package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port              int
	Host              string
	APIKey            string
	Timeout           time.Duration
	MaxTimeout        time.Duration
	AllowNoAuth       bool
	JobDir            string
	JobRetention      time.Duration
	MaxJobRuntime     time.Duration
	MaxJobOutputBytes int
	MaxJobs           int
	TrustedProxies    []string
}

func Load() (*Config, error) {
	port, err := envInt("CONCH_PORT", 14216)
	if err != nil {
		return nil, err
	}
	timeoutSeconds, err := envInt("CONCH_TIMEOUT", 30)
	if err != nil {
		return nil, err
	}
	maxTimeoutSeconds, err := envInt("CONCH_MAX_TIMEOUT", 120)
	if err != nil {
		return nil, err
	}
	retentionHours, err := envInt("CONCH_JOB_RETENTION_HOURS", 168)
	if err != nil {
		return nil, err
	}
	maxJobRuntimeSeconds, err := envInt("CONCH_MAX_JOB_TIMEOUT_SECONDS", 86400)
	if err != nil {
		return nil, err
	}
	maxJobOutputBytes, err := envInt("CONCH_MAX_JOB_OUTPUT_BYTES", 256*1024)
	if err != nil {
		return nil, err
	}
	maxJobs, err := envInt("CONCH_MAX_JOBS", 100)
	if err != nil {
		return nil, err
	}
	allowNoAuth, err := envBool("CONCH_ALLOW_NO_AUTH", false)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Port:              port,
		Host:              envStr("CONCH_HOST", "0.0.0.0"),
		APIKey:            os.Getenv("CONCH_API_KEY"),
		Timeout:           time.Duration(timeoutSeconds) * time.Second,
		MaxTimeout:        time.Duration(maxTimeoutSeconds) * time.Second,
		AllowNoAuth:       allowNoAuth,
		JobDir:            envStr("CONCH_JOB_DIR", defaultJobDir()),
		JobRetention:      time.Duration(retentionHours) * time.Hour,
		MaxJobRuntime:     time.Duration(maxJobRuntimeSeconds) * time.Second,
		MaxJobOutputBytes: maxJobOutputBytes,
		MaxJobs:           maxJobs,
		TrustedProxies:    envList("CONCH_TRUSTED_PROXIES"),
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	switch {
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("CONCH_PORT must be between 1 and 65535")
	case strings.TrimSpace(c.Host) == "":
		return fmt.Errorf("CONCH_HOST must not be empty")
	case c.Timeout <= 0:
		return fmt.Errorf("CONCH_TIMEOUT must be positive")
	case c.MaxTimeout <= 0 || c.MaxTimeout > 7*24*time.Hour:
		return fmt.Errorf("CONCH_MAX_TIMEOUT must be between 1 and 604800")
	case c.Timeout > c.MaxTimeout:
		return fmt.Errorf("CONCH_TIMEOUT must not exceed CONCH_MAX_TIMEOUT")
	case strings.TrimSpace(c.JobDir) == "":
		return fmt.Errorf("CONCH_JOB_DIR must not be empty")
	case c.JobRetention < 0:
		return fmt.Errorf("CONCH_JOB_RETENTION_HOURS must not be negative")
	case c.MaxJobRuntime <= 0 || c.MaxJobRuntime > 7*24*time.Hour:
		return fmt.Errorf("CONCH_MAX_JOB_TIMEOUT_SECONDS must be between 1 and 604800")
	case c.MaxJobOutputBytes <= 0 || c.MaxJobOutputBytes > 64<<20:
		return fmt.Errorf("CONCH_MAX_JOB_OUTPUT_BYTES must be between 1 and 67108864")
	case c.MaxJobs <= 0 || c.MaxJobs > 10000:
		return fmt.Errorf("CONCH_MAX_JOBS must be between 1 and 10000")
	}
	for _, proxy := range c.TrustedProxies {
		if net.ParseIP(proxy) != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(proxy); err != nil {
			return fmt.Errorf("CONCH_TRUSTED_PROXIES contains invalid IP or CIDR %q", proxy)
		}
	}
	return nil
}

func defaultJobDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "conch", "jobs")
	}
	return filepath.Join(".", ".conch", "jobs")
}

func envStr(key, defaultVal string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return defaultVal
}

func envInt(key string, defaultVal int) (int, error) {
	s := os.Getenv(key)
	if s == "" {
		return defaultVal, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return v, nil
}

func envBool(key string, defaultVal bool) (bool, error) {
	s := os.Getenv(key)
	if s == "" {
		return defaultVal, nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false: %w", key, err)
	}
	return v, nil
}

func envList(key string) []string {
	raw := os.Getenv(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	values := strings.Split(raw, ",")
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
