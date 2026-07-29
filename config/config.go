package config

import (
	"os"
	"path/filepath"
	"strconv"
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
}

func Load() *Config {
	return &Config{
		Port:              envInt("CONCH_PORT", 14216),
		Host:              envStr("CONCH_HOST", "0.0.0.0"),
		APIKey:            os.Getenv("CONCH_API_KEY"),
		Timeout:           time.Duration(envInt("CONCH_TIMEOUT", 30)) * time.Second,
		MaxTimeout:        time.Duration(envInt("CONCH_MAX_TIMEOUT", 120)) * time.Second,
		AllowNoAuth:       os.Getenv("CONCH_ALLOW_NO_AUTH") == "true",
		JobDir:            envStr("CONCH_JOB_DIR", defaultJobDir()),
		JobRetention:      time.Duration(envInt("CONCH_JOB_RETENTION_HOURS", 168)) * time.Hour,
		MaxJobRuntime:     time.Duration(envInt("CONCH_MAX_JOB_TIMEOUT_SECONDS", 86400)) * time.Second,
		MaxJobOutputBytes: envInt("CONCH_MAX_JOB_OUTPUT_BYTES", 256*1024),
		MaxJobs:           envInt("CONCH_MAX_JOBS", 100),
	}
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

func envInt(key string, defaultVal int) int {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return defaultVal
}
