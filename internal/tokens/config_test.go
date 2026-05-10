package tokens_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashishsinghbhadoria/goLearn/internal/tokens"
)

func TestDefaults(t *testing.T) {
	d := tokens.Defaults()
	if d.Capacity != tokens.DefaultCapacity {
		t.Errorf("Capacity = %d, want %d", d.Capacity, tokens.DefaultCapacity)
	}
	if d.RatePerMin != tokens.DefaultRatePerMin {
		t.Errorf("RatePerMin = %d, want %d", d.RatePerMin, tokens.DefaultRatePerMin)
	}
}

func TestRatePerSecond(t *testing.T) {
	c := tokens.Config{RatePerMin: 600}
	if got := c.RatePerSecond(); got != 10.0 {
		t.Fatalf("RatePerSecond = %v, want 10.0", got)
	}
}

func TestValidate_RejectsBadCapacity(t *testing.T) {
	c := tokens.Config{Capacity: 0, RatePerMin: 10}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate(cap=0) must error")
	}
}

func TestValidate_RejectsNegativeRate(t *testing.T) {
	c := tokens.Config{Capacity: 10, RatePerMin: -1}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate(rate<0) must error")
	}
}

func TestValidate_AcceptsZeroRate(t *testing.T) {
	c := tokens.Config{Capacity: 10, RatePerMin: 0}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate(rate=0) should be ok, got %v", err)
	}
}

func TestLoad_MissingFileFallsBackToDefaults(t *testing.T) {
	t.Setenv("TOKENS_CAPACITY", "")
	t.Setenv("TOKENS_RATE_PER_MIN", "")

	cfg, err := tokens.Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if cfg.Capacity != tokens.DefaultCapacity {
		t.Fatalf("Capacity = %d, want default %d", cfg.Capacity, tokens.DefaultCapacity)
	}
}

func TestLoad_FileOverridesDefaults(t *testing.T) {
	t.Setenv("TOKENS_CAPACITY", "")
	t.Setenv("TOKENS_RATE_PER_MIN", "")

	path := filepath.Join(t.TempDir(), "tokens.yaml")
	body := "capacity: 100\nrate_per_min: 60\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := tokens.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Capacity != 100 || cfg.RatePerMin != 60 {
		t.Fatalf("got %+v", cfg)
	}
}

func TestLoad_EnvOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	_ = os.WriteFile(path, []byte("capacity: 100\nrate_per_min: 60\n"), 0o644)
	t.Setenv("TOKENS_CAPACITY", "555")
	t.Setenv("TOKENS_RATE_PER_MIN", "777")

	cfg, err := tokens.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Capacity != 555 || cfg.RatePerMin != 777 {
		t.Fatalf("got %+v, want {555, 777}", cfg)
	}
}

func TestLoad_BadYAMLErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	_ = os.WriteFile(path, []byte("capacity: not-a-number\n"), 0o644)
	if _, err := tokens.Load(path); err == nil {
		t.Fatal("expected YAML parse error")
	}
}

func TestLoad_BadEnvCapacityErrors(t *testing.T) {
	t.Setenv("TOKENS_CAPACITY", "abc")
	if _, err := tokens.Load(""); err == nil {
		t.Fatal("expected error for non-numeric env")
	}
}

func TestLoad_BadEnvRateErrors(t *testing.T) {
	t.Setenv("TOKENS_CAPACITY", "")
	t.Setenv("TOKENS_RATE_PER_MIN", "abc")
	if _, err := tokens.Load(""); err == nil {
		t.Fatal("expected error for non-numeric rate env")
	}
}

func TestLoad_NegativeCapacityFromFileFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	_ = os.WriteFile(path, []byte("capacity: 0\nrate_per_min: 10\n"), 0o644)
	if _, err := tokens.Load(path); err == nil || !errMatches(err, "capacity") {
		t.Fatalf("expected capacity error, got %v", err)
	}
}

func errMatches(err error, msg string) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, err) && contains(err.Error(), msg)
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
