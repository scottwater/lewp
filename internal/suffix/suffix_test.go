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
		mode    Mode
		input   string
		want    string
		wantErr bool
	}{
		{name: "safe allows com subdomain", mode: ModeSafeSubtree, input: "local.todoordie.com", want: "local.todoordie.com"},
		{name: "safe allows co uk subdomain", mode: ModeSafeSubtree, input: "local.todoordie.co.uk", want: "local.todoordie.co.uk"},
		{name: "safe normalizes case and trailing dot", mode: ModeSafeSubtree, input: "Local.TodoOrDie.Com.", want: "local.todoordie.com"},
		{name: "safe rejects registrable com", mode: ModeSafeSubtree, input: "todoordie.com", wantErr: true},
		{name: "safe rejects registrable co uk", mode: ModeSafeSubtree, input: "todoordie.co.uk", wantErr: true},
		{name: "safe rejects leftmost www", mode: ModeSafeSubtree, input: "www.todoordie.com", wantErr: true},
		{name: "safe rejects local", mode: ModeSafeSubtree, input: "local", wantErr: true},
		{name: "safe rejects local rightmost label", mode: ModeSafeSubtree, input: "foo.local", wantErr: true},
		{name: "safe rejects nested local rightmost label", mode: ModeSafeSubtree, input: "bar.foo.local", wantErr: true},
		{name: "safe rejects test", mode: ModeSafeSubtree, input: "test", wantErr: true},
		{name: "safe rejects public suffix jp", mode: ModeSafeSubtree, input: "jp", wantErr: true},
		{name: "safe rejects public suffix com", mode: ModeSafeSubtree, input: "com", wantErr: true},
		{name: "safe rejects empty label", mode: ModeSafeSubtree, input: "local..todoordie.com", wantErr: true},
		{name: "safe rejects built in", mode: ModeSafeSubtree, input: "lewp", wantErr: true},
		{name: "mirror allows registrable com", mode: ModeDomainMirror, input: "todoordie.com", want: "todoordie.com"},
		{name: "mirror allows registrable co uk", mode: ModeDomainMirror, input: "todoordie.co.uk", want: "todoordie.co.uk"},
		{name: "mirror allows leftmost www", mode: ModeDomainMirror, input: "www.todoordie.com", want: "www.todoordie.com"},
		{name: "mirror allows normal subtree", mode: ModeDomainMirror, input: "local.todoordie.com", want: "local.todoordie.com"},
		{name: "mirror rejects local", mode: ModeDomainMirror, input: "local", wantErr: true},
		{name: "mirror rejects local rightmost label", mode: ModeDomainMirror, input: "foo.local", wantErr: true},
		{name: "mirror rejects nested local rightmost label", mode: ModeDomainMirror, input: "bar.foo.local", wantErr: true},
		{name: "mirror rejects test", mode: ModeDomainMirror, input: "test", wantErr: true},
		{name: "mirror rejects public suffix jp", mode: ModeDomainMirror, input: "jp", wantErr: true},
		{name: "mirror rejects public suffix com", mode: ModeDomainMirror, input: "com", wantErr: true},
		{name: "mirror rejects empty label", mode: ModeDomainMirror, input: "local..todoordie.com", wantErr: true},
		{name: "mirror rejects built in", mode: ModeDomainMirror, input: "lewp", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateCustom(tt.input, tt.mode)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ValidateCustom(%q, %q) accepted invalid suffix", tt.input, tt.mode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateCustom(%q, %q) error: %v", tt.input, tt.mode, err)
			}
			if got != tt.want {
				t.Fatalf("ValidateCustom(%q, %q)=%q want %q", tt.input, tt.mode, got, tt.want)
			}
		})
	}
}

func TestManagedIncludesBuiltInAndSortsCustomSuffixes(t *testing.T) {
	got := Managed([]Entry{
		{Name: "Z.TodoOrDie.Com.", Mode: ModeSafeSubtree},
		{Name: "a.todoordie.com", Mode: ModeSafeSubtree},
		{Name: "z.todoordie.com", Mode: ModeDomainMirror},
	})
	want := []string{"lewp", "a.todoordie.com", "z.todoordie.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Managed()=%v want %v", got, want)
	}
}

func TestHostInManagedSuffix(t *testing.T) {
	managed := Managed([]Entry{{Name: "local.todoordie.com", Mode: ModeSafeSubtree}})
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

func TestTLSEligibleFiltersDomainMirrors(t *testing.T) {
	entries := []Entry{
		{Name: "localkickofflabs.com", Mode: ModeDomainMirror},
		{Name: "local.todoordie.com", Mode: ModeSafeSubtree},
	}
	got := TLSEligible(entries)
	want := []string{"lewp", "local.todoordie.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TLSEligible=%v want %v", got, want)
	}
}

func TestTLSEligibleEmptyConfig(t *testing.T) {
	if got := TLSEligible(nil); !reflect.DeepEqual(got, []string{"lewp"}) {
		t.Fatalf("TLSEligible(nil)=%v", got)
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

	cfg, added, err := Add(cfg, ModeSafeSubtree, "B.TodoOrDie.Com.", "a.todoordie.com", "b.todoordie.com")
	if err != nil {
		t.Fatalf("Add safe error: %v", err)
	}
	if want := []string{"b.todoordie.com", "a.todoordie.com"}; !reflect.DeepEqual(added, want) {
		t.Fatalf("added=%v want %v", added, want)
	}

	cfg, added, err = Add(cfg, ModeDomainMirror, "todoordie.com", "www.todoordie.com")
	if err != nil {
		t.Fatalf("Add mirror error: %v", err)
	}
	if want := []string{"todoordie.com", "www.todoordie.com"}; !reflect.DeepEqual(added, want) {
		t.Fatalf("mirror added=%v want %v", added, want)
	}
	if want := []Entry{
		{Name: "a.todoordie.com", Mode: ModeSafeSubtree},
		{Name: "b.todoordie.com", Mode: ModeSafeSubtree},
		{Name: "todoordie.com", Mode: ModeDomainMirror},
		{Name: "www.todoordie.com", Mode: ModeDomainMirror},
	}; !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("cfg.Suffixes=%v want %v", cfg.Suffixes, want)
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, want := range []string{
		"[[suffixes]]",
		`name = "todoordie.com"`,
		`mode = "domain-mirror"`,
		`name = "a.todoordie.com"`,
		`mode = "safe-subtree"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Save missing %q:\n%s", want, text)
		}
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
	if got, want := Names(loaded.Suffixes), []string{"a.todoordie.com", "todoordie.com", "www.todoordie.com"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after Remove names=%v want %v", got, want)
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

func TestLoadRejectsOldStringArrayConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suffixes.toml")
	if err := os.WriteFile(path, []byte("suffixes = [\"local.todoordie.com\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted old suffixes array format")
	}
	if !strings.Contains(err.Error(), "old suffix config format is no longer supported") {
		t.Fatalf("error should explain old format cleanup: %v", err)
	}
}

func TestLoadRejectsEmptyOldStringArrayConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suffixes.toml")
	if err := os.WriteFile(path, []byte("suffixes = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted empty old suffixes array format")
	}
	if !strings.Contains(err.Error(), "old suffix config format is no longer supported") {
		t.Fatalf("error should explain old format cleanup: %v", err)
	}
}

func TestSaveEmptyConfigAndTightensExistingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suffixes.toml")
	if err := os.WriteFile(path, []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Save(path, Config{}); err != nil {
		t.Fatalf("Save empty config: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "suffixes = [") {
		t.Fatalf("Save empty config used old array format: %s", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%#o want 0600", got)
	}
}
