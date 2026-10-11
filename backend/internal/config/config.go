// Package config reads settings from the environment and refuses to start on a bad value.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// Base is what every binary (api, worker, seed) needs.
type Base struct {
	AppEnv      string
	DatabaseURL string
	LogLevel    slog.Level
}

// IsProduction gates every dev-only shortcut (OTP in the response, mock payment outcomes).
func (b Base) IsProduction() bool { return b.AppEnv == EnvProduction }

type API struct {
	Base
	Addr string
	// CORSOrigins lists the admin site origins allowed to call the API from a browser.
	CORSOrigins []string
}

type Worker struct {
	Base
	SweepInterval     time.Duration
	ReconcileInterval time.Duration
}

func LoadAPI() (API, error) {
	l := newLoader()
	cfg := API{
		Base:        l.base(),
		Addr:        l.str("ADDR", ":8080"),
		CORSOrigins: l.list("CORS_ORIGINS", "http://localhost:5173"),
	}
	return cfg, l.err()
}

func LoadWorker() (Worker, error) {
	l := newLoader()
	cfg := Worker{
		Base:              l.base(),
		SweepInterval:     l.duration("SWEEP_INTERVAL", 30*time.Second),
		ReconcileInterval: l.duration("RECONCILE_INTERVAL", 30*time.Second),
	}
	return cfg, l.err()
}

// LoadBase is for one-shot tools such as the seeder.
func LoadBase() (Base, error) {
	l := newLoader()
	b := l.base()
	return b, l.err()
}

// Loader collects every problem so a misconfigured deploy reports all of them at once
// instead of one per restart. Other packages use it to read their own settings.
type Loader struct {
	problems []string
}

func NewLoader() *Loader { return newLoader() }

func newLoader() *Loader { return &Loader{} }

func (l *Loader) base() Base {
	b := Base{
		AppEnv:      l.str("APP_ENV", EnvDevelopment),
		DatabaseURL: l.Required("DATABASE_URL"),
	}
	switch b.AppEnv {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		l.fail("APP_ENV must be development, test or production, got %q", b.AppEnv)
	}
	if err := b.LogLevel.UnmarshalText([]byte(l.str("LOG_LEVEL", "info"))); err != nil {
		l.fail("LOG_LEVEL: %v", err)
	}
	return b
}

func (l *Loader) Err() error { return l.err() }

func (l *Loader) err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return errors.New("config: " + strings.Join(l.problems, "; "))
}

func (l *Loader) fail(format string, args ...any) {
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

// Fail records a problem found by a caller's own validation.
func (l *Loader) Fail(format string, args ...any) { l.fail(format, args...) }

func (l *Loader) Str(key, fallback string) string { return l.str(key, fallback) }

func (l *Loader) str(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func (l *Loader) Required(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		l.fail("%s is required", key)
	}
	return v
}

func (l *Loader) List(key, fallback string) []string { return l.list(key, fallback) }

func (l *Loader) list(key, fallback string) []string {
	var out []string
	for _, part := range strings.Split(l.str(key, fallback), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (l *Loader) Duration(key string, fallback time.Duration) time.Duration {
	return l.duration(key, fallback)
}

func (l *Loader) duration(key string, fallback time.Duration) time.Duration {
	raw := l.str(key, "")
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		l.fail("%s must be a positive duration such as 30s, got %q", key, raw)
		return fallback
	}
	return d
}

func (l *Loader) Int(key string, fallback int) int {
	raw := l.str(key, "")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.fail("%s must be a whole number, got %q", key, raw)
		return fallback
	}
	return n
}

func (l *Loader) Bool(key string, fallback bool) bool {
	raw := l.str(key, "")
	if raw == "" {
		return fallback
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		l.fail("%s must be true or false, got %q", key, raw)
		return fallback
	}
	return b
}

// NewLogger builds the JSON logger every binary uses.
func NewLogger(b Base, service string) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: b.LogLevel})
	return slog.New(h).With("service", service, "env", b.AppEnv)
}
