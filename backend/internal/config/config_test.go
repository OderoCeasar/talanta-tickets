package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadAPI(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("APP_ENV", "development")
		cfg, err := LoadAPI()
		if err != nil {
			t.Fatalf("LoadAPI: %v", err)
		}
		if cfg.Addr != ":8080" || cfg.AppEnv != EnvDevelopment || cfg.IsProduction() || !cfg.AllowDevShortcuts() {
			t.Fatalf("unexpected defaults: %+v", cfg)
		}
	})

	t.Run("reports every problem at once", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		t.Setenv("APP_ENV", "staging")
		t.Setenv("LOG_LEVEL", "loud")
		_, err := LoadAPI()
		if err == nil {
			t.Fatal("want an error")
		}
		for _, want := range []string{"DATABASE_URL is required", "APP_ENV must be", "LOG_LEVEL"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("cors origins are split and trimmed", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://x")
		t.Setenv("APP_ENV", "development")
		t.Setenv("CORS_ORIGINS", " https://a.example , https://b.example,")
		cfg, err := LoadAPI()
		if err != nil {
			t.Fatalf("LoadAPI: %v", err)
		}
		if len(cfg.CORSOrigins) != 2 || cfg.CORSOrigins[1] != "https://b.example" {
			t.Fatalf("CORSOrigins = %v", cfg.CORSOrigins)
		}
	})
}

func TestLoadWorker(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")

	t.Run("intervals", func(t *testing.T) {
		t.Setenv("SWEEP_INTERVAL", "5s")
		cfg, err := LoadWorker()
		if err != nil {
			t.Fatalf("LoadWorker: %v", err)
		}
		if cfg.SweepInterval != 5*time.Second || cfg.ReconcileInterval != 30*time.Second {
			t.Fatalf("intervals = %v, %v", cfg.SweepInterval, cfg.ReconcileInterval)
		}
	})

	t.Run("bad interval is refused", func(t *testing.T) {
		t.Setenv("SWEEP_INTERVAL", "-1s")
		if _, err := LoadWorker(); err == nil {
			t.Fatal("want an error for a negative interval")
		}
	})
}
