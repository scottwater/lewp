package suffix

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestValidateCustomPSLCases(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "allows com subdomain", input: "local.todoordie.com", want: "local.todoordie.com"},
		{name: "allows co uk subdomain", input: "local.todoordie.co.uk", want: "local.todoordie.co.uk"},
		{name: "normalizes case and trailing dot", input: "Local.TodoOrDie.Com.", want: "local.todoordie.com"},
		{name: "rejects registrable com", input: "todoordie.com", wantErr: true},
		{name: "rejects registrable co uk", input: "todoordie.co.uk", wantErr: true},
		{name: "rejects leftmost www", input: "www.todoordie.com", wantErr: true},
		{name: "rejects local", input: "local", wantErr: true},
		{name: "rejects local rightmost label", input: "foo.local", wantErr: true},
		{name: "rejects nested local rightmost label", input: "bar.foo.local", wantErr: true},
		{name: "rejects test", input: "test", wantErr: true},
		{name: "rejects public suffix jp", input: "jp", wantErr: true},
		{name: "rejects public suffix com", input: "com", wantErr: true},
		{name: "rejects empty label", input: "local..todoordie.com", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateCustom(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateCustom(%q) accepted invalid suffix", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateCustom(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ValidateCustom(%q)=%q want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestManagedIncludesBuiltInAndSortsCustomSuffixes(t *testing.T) {
	got := Managed([]string{"Z.TodoOrDie.Com.", "a.todoordie.com", "z.todoordie.com", "lewp"})
	want := []string{"lewp", "a.todoordie.com", "z.todoordie.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Managed()=%v want %v", got, want)
	}
}

func TestHostInManagedSuffix(t *testing.T) {
	managed := Managed([]string{"local.todoordie.com"})
	tests := []struct {
		host string
		want bool
	}{
		{host: "feature.local.todoordie.com", want: true},
		{host: "local.todoordie.com.", want: true},
		{host: "feature.root.lewp", want: true},
		{host: "lewp", want: true},
		{host: "todoordie.com", want: false},
		{host: "notlewp", want: false},
	}

	for _, tt := range tests {
		if got := HostInManagedSuffix(tt.host, managed); got != tt.want {
			t.Fatalf("HostInManagedSuffix(%q)=%v want %v", tt.host, got, tt.want)
		}
	}
}

func TestLoadSaveAddRemoveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "suffixes.toml")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	if len(cfg.Suffixes) != 0 {
		t.Fatalf("Load missing file=%v want empty config", cfg.Suffixes)
	}

	cfg, added, err := Add(cfg, "B.TodoOrDie.Com.", "a.todoordie.com", "b.todoordie.com")
	if err != nil {
		t.Fatalf("Add error: %v", err)
	}
	if want := []string{"b.todoordie.com", "a.todoordie.com"}; !reflect.DeepEqual(added, want) {
		t.Fatalf("added=%v want %v", added, want)
	}
	if want := []string{"a.todoordie.com", "b.todoordie.com"}; !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("cfg.Suffixes=%v want %v", cfg.Suffixes, want)
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "suffixes = [") {
		t.Fatalf("Save did not write suffixes array: %s", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%#o want 0600", got)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load saved file: %v", err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("Load()=%v want %v", loaded, cfg)
	}

	loaded, removed, ok, err := Remove(loaded, "B.TodoOrDie.Com.")
	if err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if !ok || removed != "b.todoordie.com" {
		t.Fatalf("Remove removed=%q ok=%v", removed, ok)
	}
	if want := []string{"a.todoordie.com"}; !reflect.DeepEqual(loaded.Suffixes, want) {
		t.Fatalf("after Remove=%v want %v", loaded.Suffixes, want)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suffixes.toml")
	if err := os.WriteFile(path, []byte("suffix = [\"local.todoordie.com\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted unknown key")
	}
	if !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "suffix") {
		t.Fatalf("error should name unknown key: %v", err)
	}
}

func TestSaveEmptyConfigAndTightensExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suffixes.toml")
	if err := os.WriteFile(path, []byte("suffixes = [\"local.todoordie.com\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Save(path, Config{}); err != nil {
		t.Fatalf("Save empty config: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "suffixes = [") {
		t.Fatalf("Save empty config did not write suffixes array: %s", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%#o want 0600", got)
	}
}
