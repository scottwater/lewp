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

type Mode string

const (
	ModeSafeSubtree  Mode = "safe-subtree"
	ModeDomainMirror Mode = "domain-mirror"
)

type Entry struct {
	Name string `toml:"name"`
	Mode Mode   `toml:"mode"`
}

type Config struct {
	Suffixes []Entry `toml:"suffixes"`
}

var reservedSuffixes = map[string]struct{}{
	"invalid":   {},
	"local":     {},
	"localhost": {},
	"test":      {},
}

func ValidateCustom(input string, mode Mode) (string, error) {
	normalized, err := Normalize(input)
	if err != nil {
		return "", err
	}
	if normalized == BuiltIn {
		return "", fmt.Errorf("suffix %q is built in", normalized)
	}
	if err := validateMode(mode); err != nil {
		return "", err
	}
	labels := strings.Split(normalized, ".")
	if _, ok := reservedSuffixes[labels[len(labels)-1]]; ok {
		return "", fmt.Errorf("suffix %q is reserved", normalized)
	}
	if mode == ModeSafeSubtree && labels[0] == "www" {
		return "", fmt.Errorf("suffix %q cannot start with www", normalized)
	}

	registrable, err := publicsuffix.EffectiveTLDPlusOne(normalized)
	if err != nil {
		return "", fmt.Errorf("suffix %q must be below a registrable domain: %w", normalized, err)
	}
	if mode == ModeSafeSubtree && normalized == registrable {
		return "", fmt.Errorf("suffix %q must be below registrable domain %q", normalized, registrable)
	}
	return normalized, nil
}

func validateMode(mode Mode) error {
	switch mode {
	case ModeSafeSubtree, ModeDomainMirror:
		return nil
	default:
		return fmt.Errorf("unknown suffix mode %q", mode)
	}
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

func Managed(custom []Entry) []string {
	seen := map[string]struct{}{BuiltIn: {}}
	normalized := make([]string, 0, len(custom))
	for _, entry := range custom {
		suffix, err := ValidateCustom(entry.Name, entry.Mode)
		if err != nil {
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

func Names(entries []Entry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
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
	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if isOldStringArrayConfig(path) {
			return Config{}, fmt.Errorf("%s: old suffix config format is no longer supported; remove and re-add suffixes", path)
		}
		return Config{}, err
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return Config{}, fmt.Errorf("%s: unknown key(s): %s (allowed keys: suffixes)",
			path, strings.Join(keys, ", "))
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
	if len(cfg.Suffixes) > 0 {
		if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func Add(cfg Config, mode Mode, raw ...string) (Config, []string, error) {
	if err := validateMode(mode); err != nil {
		return Config{}, nil, err
	}
	suffixes, err := validateCustomList(cfg.Suffixes)
	if err != nil {
		return Config{}, nil, err
	}
	seen := make(map[string]struct{}, len(suffixes)+len(raw))
	for _, entry := range suffixes {
		seen[entry.Name] = struct{}{}
	}

	added := make([]string, 0, len(raw))
	for _, input := range raw {
		suffix, err := ValidateCustom(input, mode)
		if err != nil {
			return Config{}, nil, err
		}
		if _, ok := seen[suffix]; ok {
			continue
		}
		seen[suffix] = struct{}{}
		suffixes = append(suffixes, Entry{Name: suffix, Mode: mode})
		added = append(added, suffix)
	}
	sortEntries(suffixes)
	return Config{Suffixes: suffixes}, added, nil
}

func Remove(cfg Config, raw string) (Config, string, bool, error) {
	suffix, err := Normalize(raw)
	if err != nil {
		return Config{}, "", false, err
	}
	if suffix == BuiltIn {
		return Config{}, "", false, fmt.Errorf("suffix %q is built in", suffix)
	}
	suffixes, err := validateCustomList(cfg.Suffixes)
	if err != nil {
		return Config{}, "", false, err
	}

	for i, existing := range suffixes {
		if existing.Name == suffix {
			suffixes = append(suffixes[:i], suffixes[i+1:]...)
			return Config{Suffixes: suffixes}, suffix, true, nil
		}
	}
	return Config{Suffixes: suffixes}, suffix, false, nil
}

func validateCustomList(raw []Entry) ([]Entry, error) {
	seen := make(map[string]struct{}, len(raw))
	suffixes := make([]Entry, 0, len(raw))
	for _, input := range raw {
		suffix, err := ValidateCustom(input.Name, input.Mode)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[suffix]; ok {
			continue
		}
		seen[suffix] = struct{}{}
		suffixes = append(suffixes, Entry{Name: suffix, Mode: input.Mode})
	}
	sortEntries(suffixes)
	return suffixes, nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
}

func isOldStringArrayConfig(path string) bool {
	var old struct {
		Suffixes []string `toml:"suffixes"`
	}
	meta, err := toml.DecodeFile(path, &old)
	return err == nil && meta.IsDefined("suffixes")
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
