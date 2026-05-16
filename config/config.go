package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port       int
	APIKey     string
	Timeout    time.Duration
	MaxTimeout time.Duration
}

func Load() *Config {
	return &Config{
		Port:       envInt("CONCH_PORT", 8080),
		APIKey:     os.Getenv("CONCH_API_KEY"),
		Timeout:    time.Duration(envInt("CONCH_TIMEOUT", 30)) * time.Second,
		MaxTimeout: time.Duration(envInt("CONCH_MAX_TIMEOUT", 120)) * time.Second,
	}
}

func envInt(key string, defaultVal int) int {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return defaultVal
}
