package suffix

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/net/publicsuffix"
)

const BuiltIn = "lewp"

type Config struct {
	Suffixes []string `toml:"suffixes"`
}

var reservedSuffixes = map[string]struct{}{
	"invalid":   {},
	"local":     {},
	"localhost": {},
	"test":      {},
}

func ValidateCustom(input string) (string, error) {
	normalized, err := Normalize(input)
	if err != nil {
		return "", err
	}
	if normalized == BuiltIn {
		return "", fmt.Errorf("suffix %q is built in", normalized)
	}
	if _, ok := reservedSuffixes[normalized]; ok {
		return "", fmt.Errorf("suffix %q is reserved", normalized)
	}
	if strings.Split(normalized, ".")[0] == "www" {
		return "", fmt.Errorf("suffix %q cannot start with www", normalized)
	}

	registrable, err := publicsuffix.EffectiveTLDPlusOne(normalized)
	if err != nil {
		return "", fmt.Errorf("suffix %q must be below a registrable domain: %w", normalized, err)
	}
	if normalized == registrable {
		return "", fmt.Errorf("suffix %q must be below registrable domain %q", normalized, registrable)
	}
	return normalized, nil
}

func Normalize(input string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(input))
	normalized = strings.TrimSuffix(normalized, ".")
	if normalized == "" {
		return "", errors.New("suffix is empty")
	}
	if len(normalized) > 253 {
		return "", fmt.Errorf("suffix %q is too long", normalized)
	}
	for _, label := range strings.Split(normalized, ".") {
		if err := validateLabel(label); err != nil {
			return "", fmt.Errorf("invalid suffix %q: %w", normalized, err)
		}
	}
	return normalized, nil
}

func Managed(custom []string) []string {
	seen := map[string]struct{}{BuiltIn: {}}
	normalized := make([]string, 0, len(custom))
	for _, raw := range custom {
		suffix, err := Normalize(raw)
		if err != nil {
			continue
		}
		if suffix == BuiltIn {
			continue
		}
		if _, ok := seen[suffix]; ok {
			continue
		}
		seen[suffix] = struct{}{}
		normalized = append(normalized, suffix)
	}
	sort.Strings(normalized)
	return append([]string{BuiltIn}, normalized...)
}

func HostInManagedSuffix(host string, managed []string) bool {
	normalizedHost, err := Normalize(host)
	if err != nil {
		return false
	}
	for _, raw := range managed {
		managedSuffix, err := Normalize(raw)
		if err != nil {
			continue
		}
		if normalizedHost == managedSuffix || strings.HasSuffix(normalizedHost, "."+managedSuffix) {
			return true
		}
	}
	return false
}

func Load(path string) (Config, error) {
	var cfg Config
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, err
	}
	suffixes, err := validateCustomList(cfg.Suffixes)
	if err != nil {
		return Config{}, err
	}
	cfg.Suffixes = suffixes
	return cfg, nil
}

func Save(path string, cfg Config) error {
	suffixes, err := validateCustomList(cfg.Suffixes)
	if err != nil {
		return err
	}
	cfg.Suffixes = suffixes

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func Add(cfg Config, raw ...string) (Config, []string, error) {
	suffixes, err := validateCustomList(cfg.Suffixes)
	if err != nil {
		return Config{}, nil, err
	}
	seen := make(map[string]struct{}, len(suffixes)+len(raw))
	for _, suffix := range suffixes {
		seen[suffix] = struct{}{}
	}

	added := make([]string, 0, len(raw))
	for _, input := range raw {
		suffix, err := ValidateCustom(input)
		if err != nil {
			return Config{}, nil, err
		}
		if _, ok := seen[suffix]; ok {
			continue
		}
		seen[suffix] = struct{}{}
		suffixes = append(suffixes, suffix)
		added = append(added, suffix)
	}
	sort.Strings(suffixes)
	return Config{Suffixes: suffixes}, added, nil
}

func Remove(cfg Config, raw string) (Config, string, bool, error) {
	suffix, err := ValidateCustom(raw)
	if err != nil {
		return Config{}, "", false, err
	}
	suffixes, err := validateCustomList(cfg.Suffixes)
	if err != nil {
		return Config{}, "", false, err
	}

	for i, existing := range suffixes {
		if existing == suffix {
			suffixes = append(suffixes[:i], suffixes[i+1:]...)
			return Config{Suffixes: suffixes}, suffix, true, nil
		}
	}
	return Config{Suffixes: suffixes}, suffix, false, nil
}

func validateCustomList(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	suffixes := make([]string, 0, len(raw))
	for _, input := range raw {
		suffix, err := ValidateCustom(input)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[suffix]; ok {
			continue
		}
		seen[suffix] = struct{}{}
		suffixes = append(suffixes, suffix)
	}
	sort.Strings(suffixes)
	return suffixes, nil
}

func validateLabel(label string) error {
	if label == "" {
		return errors.New("empty label")
	}
	if len(label) > 63 {
		return fmt.Errorf("label %q is too long", label)
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return fmt.Errorf("label %q starts or ends with hyphen", label)
	}
	for _, r := range label {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' {
			continue
		}
		return fmt.Errorf("label %q contains invalid character %q", label, r)
	}
	return nil
}
