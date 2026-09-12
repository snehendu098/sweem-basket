package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

func GetEnv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func GetEnvList(key string, def []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func GetEnvInt(key string, def int) int {
	return parse(key, def, strconv.Atoi)
}

func GetEnvFloat(key string, def float64) float64 {
	return parse(key, def, func(s string) (float64, error) { return strconv.ParseFloat(s, 64) })
}

func GetEnvDuration(key string, def time.Duration) time.Duration {
	return parse(key, def, time.ParseDuration)
}

func parse[T any](key string, def T, fn func(string) (T, error)) T {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := fn(raw)
	if err != nil {
		slog.Warn("invalid env value, using default", "key", key, "value", raw, "default", def)
		return def
	}
	return v
}

func LoadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || os.Getenv(key) != "" {
			continue
		}
		os.Setenv(key, strings.Trim(strings.TrimSpace(val), `"'`))
	}
}
