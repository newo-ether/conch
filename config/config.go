package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port        int
	Host        string
	APIKey      string
	Timeout     time.Duration
	MaxTimeout  time.Duration
	AllowNoAuth bool
}

func Load() *Config {
	return &Config{
		Port:        envInt("CONCH_PORT", 14216),
		Host:        envStr("CONCH_HOST", "0.0.0.0"),
		APIKey:      os.Getenv("CONCH_API_KEY"),
		Timeout:     time.Duration(envInt("CONCH_TIMEOUT", 30)) * time.Second,
		MaxTimeout:  time.Duration(envInt("CONCH_MAX_TIMEOUT", 120)) * time.Second,
		AllowNoAuth: os.Getenv("CONCH_ALLOW_NO_AUTH") == "true",
	}
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
