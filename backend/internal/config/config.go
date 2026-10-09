// Package config reads the API's settings from the environment.
package config

import (
	"errors"
	"os"
)

type Config struct {
	Addr        string
	DatabaseURL string
}

func Load() (Config, error) {
	cfg := Config{
		Addr:        getenv("ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
