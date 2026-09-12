package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
)

func LoadRedisConfig() homerun.RedisConfig {
	return homerun.RedisConfig{
		Addr:     homerun.GetEnv("REDIS_ADDR", "localhost"),
		Port:     homerun.GetEnv("REDIS_PORT", "6379"),
		Password: homerun.GetEnv("REDIS_PASSWORD", ""),
		Stream:   homerun.GetEnv("REDIS_STREAM", "homerun"),
	}
}

// PitcherStartupTimeoutEnv bounds how long HTTP mode waits for omni-pitcher at
// startup. Redis mode reads homerun-library's REDIS_STARTUP_TIMEOUT instead;
// both have the same default and the same validation.
const PitcherStartupTimeoutEnv = "PITCHER_STARTUP_TIMEOUT"

// DefaultStartupTimeout is homerun-library's default for REDIS_STARTUP_TIMEOUT.
const DefaultStartupTimeout = homerun.DefaultRedisStartupTimeout

// LoadStartupTimeout reads a startup budget from the environment variable name
// (a Go duration, e.g. "90s" or "2m"). Unset means DefaultStartupTimeout.
func LoadStartupTimeout(name string) (time.Duration, error) {
	return ParseStartupTimeout(name, os.Getenv(name))
}

// ParseStartupTimeout returns an error naming the variable for an unparsable or
// non-positive value rather than falling back: a typo should fail startup, not
// quietly restore a budget nobody chose.
func ParseStartupTimeout(name, v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultStartupTimeout, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", name, v, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s %q: must be positive", name, v)
	}
	return d, nil
}

// SetupLogging configures slog as the default logger based on LOG_FORMAT and LOG_LEVEL env vars.
func SetupLogging() {
	format := strings.ToLower(homerun.GetEnv("LOG_FORMAT", "json"))
	levelStr := strings.ToLower(homerun.GetEnv("LOG_LEVEL", "info"))

	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))

	// homerun-library is silent by default as of v3.2.0. Routing it through the
	// same logger means its records arrive in this service's format and at this
	// service's level, instead of the pterm-decorated stdout writes it used to
	// interleave into the log stream.
	homerun.SetLogger(slog.Default())
}
