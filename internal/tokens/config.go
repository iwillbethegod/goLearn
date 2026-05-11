package tokens

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// DefaultCapacity / DefaultRatePerMin are the values from the day-4
// brief. Used when no config file is provided AND no env vars are set.
const (
	DefaultCapacity   int64 = 20_000
	DefaultRatePerMin int64 = 10_000
)

// Config carries the per-user token-bucket parameters. Loaded from a
// YAML file (env vars override individual fields).
//
//	capacity:     20000
//	rate_per_min: 10000
type Config struct {
	Capacity   int64 `yaml:"capacity"`
	RatePerMin int64 `yaml:"rate_per_min"`
}

// RatePerSecond returns the refill rate as tokens-per-second, the
// unit Bucket internals use.
func (c Config) RatePerSecond() float64 {
	return float64(c.RatePerMin) / 60.0
}

// Validate ensures both fields are positive. NewBucket already
// clamps but surfacing a clear error early at startup is friendlier.
func (c Config) Validate() error {
	if c.Capacity < 1 {
		return errors.New("tokens.config: capacity must be >= 1")
	}
	if c.RatePerMin < 0 {
		return errors.New("tokens.config: rate_per_min must be >= 0")
	}
	return nil
}

// Defaults returns a Config populated from the day-4 brief.
func Defaults() Config {
	return Config{
		Capacity:   DefaultCapacity,
		RatePerMin: DefaultRatePerMin,
	}
}

// Load reads a YAML file from path and applies env-var overrides
// (TOKENS_CAPACITY, TOKENS_RATE_PER_MIN). A missing file is not an
// error — callers fall back to Defaults() merged with env overrides.
//
// Precedence (lowest → highest): Defaults → file → env.
func Load(path string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// Acceptable — caller will get defaults+env. The CLI
			// flag default is `config/tokens.yaml`, which won't
			// exist in fresh checkouts; that's fine.
		case err != nil:
			return Config{}, fmt.Errorf("read %s: %w", path, err)
		default:
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return Config{}, fmt.Errorf("parse %s: %w", path, err)
			}
		}
	}

	if v := os.Getenv("TOKENS_CAPACITY"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("TOKENS_CAPACITY=%q: %w", v, err)
		}
		cfg.Capacity = n
	}
	if v := os.Getenv("TOKENS_RATE_PER_MIN"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("TOKENS_RATE_PER_MIN=%q: %w", v, err)
		}
		cfg.RatePerMin = n
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
