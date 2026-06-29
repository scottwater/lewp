# Domain Mirror Suffixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add explicit domain-mirror suffix mode so Lewp can locally mirror real domain trees such as `localkickofflabs.com`, including apex and `www` hosts.

**Architecture:** Keep safe subtree mode as default. Change suffix config from string-only values to typed entries with `name` and `mode`, then thread mode-aware validation through setup, suffix list/remove, doctor, daemon loading, and docs. Keep DNS/proxy exact-host behavior; only relax route host validation so an exact managed suffix can be registered.

**Tech Stack:** Go 1.25, stdlib `flag`/`os`/`strings`, `github.com/BurntSushi/toml`, `golang.org/x/net/publicsuffix`, existing Lewp CLI/daemon/test patterns.

---

## File Structure

- Modify `internal/suffix/suffix.go`: add `Mode`, `Entry`, mode-aware validation, typed config load/save/add/remove helpers, and name projection helpers.
- Modify `internal/suffix/suffix_test.go`: replace string-config expectations with typed entries and domain-mirror validation coverage.
- Modify `internal/cli/cli.go`: parse `setup --allow-domain-mirror`, pass mode into suffix add, print mirror warnings, list modes, and use suffix names where resolver paths are needed.
- Modify `internal/cli/cli_test.go`: update suffix setup/list/remove/doctor tests for typed config, mirror mode, old config failure, and warning output.
- Modify `internal/cli/doctor.go`: iterate suffix entries where mode should appear in doctor detail, and use suffix names for lookups.
- Modify `internal/identity/identity.go`: allow host equal to a managed suffix while keeping label validation.
- Modify `internal/identity/identity_test.go`: replace the bare suffix rejection test with exact suffix acceptance.
- Modify `internal/dns/dns_test.go`: add explicit custom suffix apex DNS coverage.
- Modify `internal/proxy/proxy_test.go`: add exact route coverage for suffix apex and unknown apex/subdomain debug behavior if missing.
- Modify `internal/daemon/daemon.go`, `internal/control/socket.go`, or other callers only if compile errors reveal string-slice assumptions.
- Modify `internal/cli/help.go`, `README.md`, and `DOCUMENTATION.md`: document `--allow-domain-mirror`, suffix modes, warnings, and KickoffLabs-style examples.

---

### Task 1: Suffix Model Tests

**Files:**
- Modify: `internal/suffix/suffix_test.go`
- Modify: `internal/suffix/suffix.go`

- [ ] **Step 1: Replace validation tests with mode-aware cases**

In `internal/suffix/suffix_test.go`, replace `TestValidateCustomPSLCases` with:

```go
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
		{name: "safe rejects built in", mode: ModeSafeSubtree, input: "lewp", wantErr: true},
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
```

- [ ] **Step 2: Update managed/name projection tests**

Replace `TestManagedIncludesBuiltInAndSortsCustomSuffixes` with:

```go
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
```

Keep `TestHostInManagedSuffix` as-is except ensure `Managed` call uses entries:

```go
managed := Managed([]Entry{{Name: "local.todoordie.com", Mode: ModeSafeSubtree}})
```

- [ ] **Step 3: Update config round-trip test**

Replace `TestLoadSaveAddRemoveRoundTrip` with:

```go
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
	if Names(loaded.Suffixes)[0] != "a.todoordie.com" {
		t.Fatalf("after Remove=%v want first suffix a.todoordie.com", loaded.Suffixes)
	}
}
```

- [ ] **Step 4: Add old config format rejection test**

Replace `TestSaveEmptyConfigAndTightensExistingMode` with:

```go
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
```

- [ ] **Step 5: Run suffix tests and verify failure**

Run:

```bash
go test ./internal/suffix
```

Expected: FAIL because `Mode`, `Entry`, `Names`, and the new `ValidateCustom`/`Add` signatures do not exist.

- [ ] **Step 6: Commit failing tests**

```bash
git add internal/suffix/suffix_test.go
git commit -m "test: specify domain mirror suffix model"
```

---

### Task 2: Suffix Model Implementation

**Files:**
- Modify: `internal/suffix/suffix.go`
- Modify: `internal/suffix/suffix_test.go`

- [ ] **Step 1: Replace suffix types and validation API**

In `internal/suffix/suffix.go`, replace the top-level `Config` type and add mode constants:

```go
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
```

Replace `ValidateCustom` with:

```go
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
		return fmt.Errorf("invalid suffix mode %q", mode)
	}
}
```

- [ ] **Step 2: Replace managed/name helpers**

Replace `Managed` with:

```go
func Managed(custom []Entry) []string {
	seen := map[string]struct{}{BuiltIn: {}}
	normalized := make([]string, 0, len(custom))
	for _, entry := range custom {
		name, err := Normalize(entry.Name)
		if err != nil {
			continue
		}
		if name == BuiltIn {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
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
```

- [ ] **Step 3: Replace config validation helpers**

Replace `Add`, `Remove`, and `validateCustomList` with:

```go
func Add(cfg Config, mode Mode, raw ...string) (Config, []string, error) {
	entries, err := validateCustomList(cfg.Suffixes)
	if err != nil {
		return Config{}, nil, err
	}
	if err := validateMode(mode); err != nil {
		return Config{}, nil, err
	}
	seen := make(map[string]struct{}, len(entries)+len(raw))
	for _, entry := range entries {
		seen[entry.Name] = struct{}{}
	}

	added := make([]string, 0, len(raw))
	for _, input := range raw {
		name, err := ValidateCustom(input, mode)
		if err != nil {
			return Config{}, nil, err
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, Entry{Name: name, Mode: mode})
		added = append(added, name)
	}
	sortEntries(entries)
	return Config{Suffixes: entries}, added, nil
}

func Remove(cfg Config, raw string) (Config, string, bool, error) {
	name, err := Normalize(raw)
	if err != nil {
		return Config{}, "", false, err
	}
	if name == BuiltIn {
		return Config{}, "", false, fmt.Errorf("suffix %q is built in", name)
	}
	entries, err := validateCustomList(cfg.Suffixes)
	if err != nil {
		return Config{}, "", false, err
	}

	for i, entry := range entries {
		if entry.Name == name {
			entries = append(entries[:i], entries[i+1:]...)
			return Config{Suffixes: entries}, name, true, nil
		}
	}
	return Config{Suffixes: entries}, name, false, nil
}

func validateCustomList(raw []Entry) ([]Entry, error) {
	seen := make(map[string]struct{}, len(raw))
	entries := make([]Entry, 0, len(raw))
	for _, input := range raw {
		name, err := ValidateCustom(input.Name, input.Mode)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, Entry{Name: name, Mode: input.Mode})
	}
	sortEntries(entries)
	return entries, nil
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
}
```

- [ ] **Step 4: Update Load old-format handling**

In `Load`, after `toml.DecodeFile`, keep unknown key handling and add an old array format check before `validateCustomList`:

```go
if len(cfg.Suffixes) > 0 {
	for _, entry := range cfg.Suffixes {
		if entry.Name == "" && entry.Mode == "" {
			return Config{}, fmt.Errorf("%s: old suffix config format is no longer supported", path)
		}
	}
}
```

Because BurntSushi TOML may fail decoding `suffixes = ["..."]` into `[]Entry` before this point, also wrap decode errors:

```go
if err != nil {
	if strings.Contains(err.Error(), "cannot load TOML value of type []interface {} into a Go slice containing suffix.Entry") {
		return Config{}, fmt.Errorf("%s: old suffix config format is no longer supported", path)
	}
	return Config{}, err
}
```

- [ ] **Step 5: Run suffix tests**

Run:

```bash
go test ./internal/suffix
```

Expected: PASS.

- [ ] **Step 6: Commit suffix implementation**

```bash
git add internal/suffix/suffix.go internal/suffix/suffix_test.go
git commit -m "feat: add domain mirror suffix model"
```

---

### Task 3: CLI Setup And Suffix Commands

**Files:**
- Modify: `internal/cli/cli_test.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/doctor.go`

- [ ] **Step 1: Update existing CLI tests to typed config**

In `internal/cli/cli_test.go`, replace each `suffix.Config{Suffixes: []string{"local.todoordie.com"}}` with:

```go
suffix.Config{Suffixes: []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}}
```

Replace `suffix.Config{Suffixes: []string{"local.old.com"}}` with:

```go
suffix.Config{Suffixes: []suffix.Entry{{Name: "local.old.com", Mode: suffix.ModeSafeSubtree}}}
```

Update string-slice assertions to typed entries. For `TestRunSetupAddsCustomSuffixResolver`, use:

```go
want := []suffix.Entry{{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree}}
if !reflect.DeepEqual(cfg.Suffixes, want) {
	t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
}
```

For `TestRunSetupAdditiveSuffixConfig`, use:

```go
want := []suffix.Entry{
	{Name: "local.new.com", Mode: suffix.ModeSafeSubtree},
	{Name: "local.old.com", Mode: suffix.ModeSafeSubtree},
}
if !reflect.DeepEqual(cfg.Suffixes, want) {
	t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
}
```

- [ ] **Step 2: Add domain mirror setup test**

Add to `internal/cli/cli_test.go` after `TestRunSetupRejectsApexSuffix`:

```go
func TestRunSetupAllowsDomainMirrorSuffix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "localkickofflabs.com", "--allow-domain-mirror"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/config/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	assertResolverPort(t, dir+"/resolver/lewp")
	assertResolverPort(t, dir+"/resolver/localkickofflabs.com")
	cfg, err := suffix.Load(dir + "/config/suffixes.toml")
	if err != nil {
		t.Fatalf("Load suffix config: %v", err)
	}
	want := []suffix.Entry{{Name: "localkickofflabs.com", Mode: suffix.ModeDomainMirror}}
	if !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
	}
	got := stdout.String()
	for _, want := range []string{
		"SUFFIX=localkickofflabs.com",
		"RESOLVER=" + dir + "/resolver/localkickofflabs.com",
		"warning: domain mirror localkickofflabs.com shadows public DNS",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("setup output missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 3: Add `www` mirror and old config failure tests**

Add:

```go
func TestRunSetupAllowsWWWDomainMirrorSuffix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "www.localkickofflabs.com", "--allow-domain-mirror"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/config/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	cfg, err := suffix.Load(dir + "/config/suffixes.toml")
	if err != nil {
		t.Fatalf("Load suffix config: %v", err)
	}
	want := []suffix.Entry{{Name: "www.localkickofflabs.com", Mode: suffix.ModeDomainMirror}}
	if !reflect.DeepEqual(cfg.Suffixes, want) {
		t.Fatalf("suffixes=%v want %v", cfg.Suffixes, want)
	}
}

func TestRunSetupRejectsOldSuffixConfigFormat(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dir := t.TempDir()
	suffixesPath := dir + "/config/suffixes.toml"
	if err := os.MkdirAll(dir+"/config", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(suffixesPath, []byte("suffixes = [\"local.todoordie.com\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "local.new.com"},
		WorkDir:      dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: suffixesPath,
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		LogDir:       dir + "/Logs/lewp",
		ProgramPath:  "/usr/local/bin/lewp",
		RunCommand:   func(_ context.Context, _ []string) error { return nil },
	})
	if code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "old suffix config format is no longer supported") ||
		!strings.Contains(got, "Next: remove "+suffixesPath) {
		t.Fatalf("stderr missing cleanup guidance:\n%s", got)
	}
}
```

- [ ] **Step 4: Update suffix list expected output**

In `TestRunSuffixListShowsBuiltInAndCustom`, replace expected output:

```go
if got, want := stdout.String(), "SUFFIX\tMODE\nlewp\tbuilt-in\nlocal.todoordie.com\tsafe-subtree\n"; got != want {
	t.Fatalf("stdout=%q want %q", got, want)
}
```

Use two saved entries in `TestRunSuffixListShowsBuiltInAndCustom`:

```go
suffix.Config{Suffixes: []suffix.Entry{
	{Name: "local.todoordie.com", Mode: suffix.ModeSafeSubtree},
	{Name: "localkickofflabs.com", Mode: suffix.ModeDomainMirror},
}}
```

Then expected:

```text
SUFFIX	MODE
lewp	built-in
local.todoordie.com	safe-subtree
localkickofflabs.com	domain-mirror
```

- [ ] **Step 5: Update doctor suffix tests**

In `TestDoctorReportsCustomSuffixResolvers`, assert mode appears:

```go
if got.Status != statusOK || !strings.Contains(got.Detail, customResolverPath) || !strings.Contains(got.Detail, "safe-subtree") {
	t.Fatalf("custom suffix resolver check=%+v", got)
}
```

In `TestDoctorChecksCustomSuffixDNS`, no behavior change except typed config. Keep lookup assertion:

```go
if got := strings.Join(lookedUp, ","); got != "doctor.lewp,doctor.local.todoordie.com" {
	t.Fatalf("lookups=%q", got)
}
```

- [ ] **Step 6: Run CLI tests and verify failure**

Run:

```bash
go test ./internal/cli
```

Expected: FAIL because CLI still calls `suffix.Add` without mode, does not parse `--allow-domain-mirror`, prints old suffix list columns, and doctor iterates entries as strings.

- [ ] **Step 7: Implement setup flag and typed suffix CLI**

In `internal/cli/cli.go`, inside `runSetup`, add:

```go
allowDomainMirror := fs.Bool("allow-domain-mirror", false, "")
```

Change suffix add block:

```go
mode := suffix.ModeSafeSubtree
if *allowDomainMirror {
	mode = suffix.ModeDomainMirror
}
if len(suffixFlags) > 0 {
	updated, added, err := suffix.Add(suffixCfg, mode, suffixFlags...)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "invalid suffix: %v\n", err)
		return 1
	}
	suffixCfg = updated
	addedSuffixes = added
}
resolverPaths := resolverPathsForSuffixes(cfg, suffix.Managed(suffixCfg.Suffixes))
```

Change load error handling:

```go
suffixCfg, err := suffix.Load(cfg.SuffixesPath)
if err != nil {
	fmt.Fprintf(cfg.Stderr, "load suffix config: %v\n", err)
	if strings.Contains(err.Error(), "old suffix config format is no longer supported") {
		fmt.Fprintf(cfg.Stderr, "Next: remove %s, then re-run: lewp setup --suffix ...\n", cfg.SuffixesPath)
	}
	return 1
}
```

After printing `SUFFIX=...`, add mirror warning:

```go
for _, s := range addedSuffixes {
	fmt.Fprintf(cfg.Stdout, "SUFFIX=%s RESOLVER=%s\n", s, resolverPathForSuffix(cfg, s))
	if *allowDomainMirror {
		fmt.Fprintf(cfg.Stdout, "# warning: domain mirror %s shadows public DNS for this suffix and its subdomains on this Mac\n", s)
	}
}
```

- [ ] **Step 8: Update suffix list/remove functions**

In `runSuffixList`, replace output:

```go
fmt.Fprintln(cfg.Stdout, "SUFFIX\tMODE")
fmt.Fprintf(cfg.Stdout, "%s\tbuilt-in\n", suffix.BuiltIn)
for _, entry := range suffixCfg.Suffixes {
	fmt.Fprintf(cfg.Stdout, "%s\t%s\n", entry.Name, entry.Mode)
}
```

`runSuffixRemove` can keep the same call signature after Task 2. Resolver path still uses the returned removed suffix name.

- [ ] **Step 9: Update doctor suffix loops**

In `internal/cli/doctor.go`, update `suffixResolverChecks`:

```go
checks := make([]doctorCheck, 0, len(suffixCfg.Suffixes))
for _, entry := range suffixCfg.Suffixes {
	path := resolverPathForSuffix(cfg, entry.Name)
	c := doctorCheck{Name: "resolver " + entry.Name, Inspect: path}
	body, err := os.ReadFile(path)
	if err != nil {
		c.Status = statusFail
		c.Detail = err.Error()
	} else if port, ok := dns.ResolverPort(string(body)); !ok || port != dns.DefaultPort {
		c.Status = statusFail
		c.Detail = "resolver does not point at Lewp DNS port"
	} else {
		c.Status = statusOK
		c.Detail = fmt.Sprintf("%s -> port %d (%s)", path, port, entry.Mode)
	}
	checks = append(checks, c)
}
```

Update `dnsResolutionChecks`:

```go
for _, entry := range suffixCfg.Suffixes {
	host := "doctor." + entry.Name
	checks = append(checks, dnsResolutionCheck("DNS "+entry.Name, host, host, cfg))
}
```

- [ ] **Step 10: Update other suffix name loops in CLI**

Run:

```bash
rg -n "Suffixes|suffix\\.Managed|resolverPathForSuffix|for _, s := range suffixCfg\\.Suffixes" internal/cli internal/daemon internal/control
```

For any loop over `suffixCfg.Suffixes` that passes an entry to a function expecting string, use `entry.Name`. For any call to `suffix.Managed`, pass entries directly. Common replacements:

```go
for _, entry := range suffixCfg.Suffixes {
	path := resolverPathForSuffix(cfg, entry.Name)
}
```

and:

```go
managedSuffixes := suffix.Managed(suffixCfg.Suffixes)
```

- [ ] **Step 11: Run CLI tests**

Run:

```bash
go test ./internal/cli
```

Expected: PASS.

- [ ] **Step 12: Commit CLI changes**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go internal/cli/doctor.go
git commit -m "feat: add domain mirror setup mode"
```

---

### Task 4: Route Host Apex Support

**Files:**
- Modify: `internal/identity/identity_test.go`
- Modify: `internal/identity/identity.go`
- Modify: `internal/dns/dns_test.go`
- Modify: `internal/proxy/proxy_test.go`

- [ ] **Step 1: Update identity host validation test**

In `internal/identity/identity_test.go`, replace `TestValidateHostRejectsBareManagedSuffix` with:

```go
func TestValidateHostAllowsExactManagedSuffix(t *testing.T) {
	managed := []string{"lewp", "local.todoordie.com", "localkickofflabs.com"}
	if err := ValidateHostForSuffixes("local.todoordie.com", managed); err != nil {
		t.Fatalf("safe subtree apex host rejected: %v", err)
	}
	if err := ValidateHostForSuffixes("localkickofflabs.com", managed); err != nil {
		t.Fatalf("domain mirror apex host rejected: %v", err)
	}
	if err := ValidateHostForSuffixes("feature.local.todoordie.com", managed); err != nil {
		t.Fatalf("host below managed suffix rejected: %v", err)
	}
}
```

- [ ] **Step 2: Run identity tests and verify failure**

Run:

```bash
go test ./internal/identity
```

Expected: FAIL because `ValidateHostForSuffixes` still rejects exact managed suffix hosts.

- [ ] **Step 3: Remove label-before-suffix requirement**

In `internal/identity/identity.go`, remove this block from `ValidateHostForSuffixes`:

```go
if !hasLabelBeforeManagedSuffix(host, managed) {
	return fmt.Errorf("host %q must include at least one label before its managed suffix", host)
}
```

Delete `hasLabelBeforeManagedSuffix` entirely.

- [ ] **Step 4: Run identity tests**

Run:

```bash
go test ./internal/identity
```

Expected: PASS.

- [ ] **Step 5: Add DNS apex test**

In `internal/dns/dns_test.go`, add after `TestHandleWithSuffixesAnswersCustomManagedSuffix`:

```go
func TestHandleWithSuffixesAnswersCustomManagedSuffixApex(t *testing.T) {
	query := mustQuery(t, "localkickofflabs.com.", dnsmessage.TypeA)

	reply, err := HandleWithSuffixes(query, []string{"lewp", "localkickofflabs.com"})
	if err != nil {
		t.Fatal(err)
	}
	answers := parseAnswers(t, reply)
	if len(answers) != 1 {
		t.Fatalf("answers=%d", len(answers))
	}
	a, ok := answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer type=%T", answers[0].Body)
	}
	if got := netip.AddrFrom4(a.A); got.String() != "127.0.0.1" {
		t.Fatalf("A=%s", got)
	}
}
```

- [ ] **Step 6: Run DNS tests**

Run:

```bash
go test ./internal/dns
```

Expected: PASS. DNS already supports apex; this test locks it.

- [ ] **Step 7: Add proxy exact apex route test**

In `internal/proxy/proxy_test.go`, add:

```go
func TestProxyRoutesExactManagedSuffixApex(t *testing.T) {
	store := openProxyStore(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "localkickofflabs.com" {
			t.Fatalf("upstream host=%q", r.Host)
		}
		fmt.Fprint(w, "apex ok")
	}))
	defer backend.Close()
	port := backend.Listener.Addr().(*net.TCPAddr).Port
	registerRoute(t, store, "localkickofflabs.com", port)

	handler := NewWithSuffixes(store, []string{"lewp", "localkickofflabs.com"})
	req := httptest.NewRequest(http.MethodGet, "http://localkickofflabs.com/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "apex ok" {
		t.Fatalf("body=%q", rr.Body.String())
	}
}
```

- [ ] **Step 8: Run proxy tests**

Run:

```bash
go test ./internal/proxy
```

Expected: PASS.

- [ ] **Step 9: Commit route apex support**

```bash
git add internal/identity/identity.go internal/identity/identity_test.go internal/dns/dns_test.go internal/proxy/proxy_test.go
git commit -m "feat: allow exact managed suffix routes"
```

---

### Task 5: Daemon And Control Compile Fixes

**Files:**
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/control/socket.go`
- Modify: `internal/control/service.go`
- Modify: any compile-failing file that passes suffix entries/names incorrectly

- [ ] **Step 1: Run package tests to find string-slice assumptions**

Run:

```bash
go test ./internal/...
```

Expected: may FAIL with compile errors where `[]suffix.Entry` is passed to APIs that still expect `[]string`, or where code ranges over entries as strings.

- [ ] **Step 2: Fix managed suffix projection at daemon startup**

Search:

```bash
rg -n "suffix\\.Load|suffix\\.Managed|ManagedSuffixes|Suffixes" internal cmd
```

Apply these rules:

- `suffix.Managed(suffixCfg.Suffixes)` remains correct after Task 2 because `Managed` accepts `[]Entry`.
- Any place that needs raw suffix names uses `suffix.Names(suffixCfg.Suffixes)`.
- Any resolver path, doctor check name, DNS host, or printed suffix uses `entry.Name`.

The daemon/control public field `ManagedSuffixes []string` should stay string-based. It represents runtime names only, not storage metadata.

- [ ] **Step 3: Run internal tests**

Run:

```bash
go test ./internal/...
```

Expected: PASS.

- [ ] **Step 4: Commit compile fixes if any files changed**

If Step 2 changed files, commit them:

```bash
git add internal/daemon/daemon.go internal/control/socket.go internal/control/service.go internal/cli/cli.go internal/cli/doctor.go
git commit -m "refactor: pass suffix names to runtime services"
```

If no files changed, skip this commit.

---

### Task 6: Help And User Documentation

**Files:**
- Modify: `internal/cli/help.go`
- Modify: `README.md`
- Modify: `DOCUMENTATION.md`

- [ ] **Step 1: Update setup help**

In `internal/cli/help.go`, replace setup usage with:

```text
Usage:
  lewp setup [--suffix <suffix>] [--allow-domain-mirror] [--start]
```

Add to setup help:

```text
Custom suffixes:
  lewp setup --suffix local.todoordie.com
  lewp setup --suffix localkickofflabs.com --allow-domain-mirror

By default, custom suffixes must be below a registrable domain. Use
--allow-domain-mirror only when you want this Mac to shadow public DNS for a
real domain tree locally, including the suffix apex and www hosts.
```

- [ ] **Step 2: Update README custom suffix section**

In `README.md`, update `### Custom public dev suffixes` to include:

```markdown
Safe custom suffixes are subtrees:

```sh
lewp setup --suffix local.todoordie.com
```

Lewp installs a resolver only for `local.todoordie.com`, so `todoordie.com` and
`www.todoordie.com` continue to use normal DNS.

For local replicas of a real domain tree, opt into domain mirror mode:

```sh
lewp setup --suffix localkickofflabs.com --allow-domain-mirror
lewp add --host localkickofflabs.com
lewp add --host app.localkickofflabs.com
lewp add --host leads.localkickofflabs.com
```

Domain mirror mode shadows public DNS for that suffix and its subdomains on this
Mac while the resolver file exists. Proxy routing is still exact-host based, so
each hostname needs its own `lewp add --host ...` route.
```

- [ ] **Step 3: Update DOCUMENTATION setup/suffix sections**

In `DOCUMENTATION.md`, update the command summary:

```text
lewp setup [--suffix S] [--allow-domain-mirror]
```

In `## lewp setup`, document:

```markdown
`--suffix S` has two modes:

- default `safe-subtree`: accepts owned subtrees such as
  `local.todoordie.com`; rejects registrable apexes and `www.*`.
- `--allow-domain-mirror`: accepts real-domain mirror suffixes such as
  `localkickofflabs.com` and `www.localkickofflabs.com`.

Domain mirror mode shadows public DNS for that suffix and all subdomains on this
Mac until the resolver file is removed with `lewp suffix remove S` or
`lewp system uninstall`.
```

Update suffix list docs to show:

```text
SUFFIX                 MODE
lewp                   built-in
local.todoordie.com    safe-subtree
localkickofflabs.com   domain-mirror
```

- [ ] **Step 4: Run docs/help-related tests**

Run:

```bash
go test ./internal/cli
```

Expected: PASS. If completion/help tests assert old setup usage, update expected strings to include `--allow-domain-mirror`.

- [ ] **Step 5: Commit docs/help**

```bash
git add internal/cli/help.go README.md DOCUMENTATION.md internal/cli/cli_test.go
git commit -m "docs: document domain mirror suffixes"
```

---

### Task 7: End-To-End Verification

**Files:**
- No planned source edits

- [ ] **Step 1: Run full Go test suite**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 2: Build binary**

Run:

```bash
bin/build
```

Expected: prints the built binary path ending in `/lewp`.

- [ ] **Step 3: Inspect help output**

Run:

```bash
go run ./cmd/lewp setup --help
```

Expected: output includes `--allow-domain-mirror`, `local.todoordie.com`, and `localkickofflabs.com`.

- [ ] **Step 4: Run a no-sudo setup simulation through tests only**

Do not run real `lewp setup --suffix localkickofflabs.com --allow-domain-mirror` against `/etc/resolver` during automated verification. The CLI tests already cover temp resolver paths. Manual system setup is a user decision because it changes local DNS.

- [ ] **Step 5: Check git status**

Run:

```bash
git status --short
```

Expected: clean.
