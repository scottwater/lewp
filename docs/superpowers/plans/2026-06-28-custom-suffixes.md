# Custom Suffixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add explicit, safe custom suffix support for owned public-domain subtrees while keeping `.lewp` as the default.

**Architecture:** Introduce a focused `internal/suffix` package for validation, storage, and membership checks. Wire the suffix list into setup/resolver management, daemon DNS matching, host validation, proxy unknown-host handling, and doctor reporting. Keep default host inference on `.lewp`; custom suffixes are explicit only.

**Tech Stack:** Go 1.25, `golang.org/x/net/publicsuffix`, stdlib DNS/file APIs, existing CLI/control/registry/proxy patterns.

---

## File Map

- Create `internal/suffix/suffix.go`: validates custom suffixes, normalizes hosts, loads/saves `suffixes.toml`, computes managed suffix lists, checks host membership.
- Create `internal/suffix/suffix_test.go`: PSL validation, config load/save, host membership.
- Modify `internal/dns/dns.go`: DNS handler accepts configured managed suffixes instead of hard-coded `.lewp`.
- Modify `internal/dns/dns_test.go`: configured suffix matching tests.
- Modify `internal/cli/cli.go`: add `SuffixesPath` config, parse `setup --suffix`, write all resolver files, add `suffix` command, remove custom suffix resolver files during uninstall.
- Modify `internal/cli/help.go`: document `setup --suffix` and `suffix` command.
- Modify `internal/cli/cli_test.go`: setup/list/remove/uninstall tests.
- Modify `internal/control/service.go`: pass configured suffixes into identity host validation.
- Modify `internal/control/service_test.go`: custom host acceptance/rejection.
- Modify `internal/identity/identity.go`: add managed-suffix-aware host validation without changing default `.lewp` inference.
- Modify `internal/identity/identity_test.go`: host validation tests with configured suffixes.
- Modify `internal/proxy/proxy.go`: unknown configured-suffix hosts get Lewp debug page.
- Modify `internal/proxy/proxy_test.go`: custom suffix unknown-host debug page.
- Modify `internal/cli/doctor.go`: report configured suffix resolver files and lookup checks.
- Modify `README.md` and `DOCUMENTATION.md`: user-facing custom suffix docs.

## Task 1: Suffix Validation And Storage

**Files:**
- Create: `internal/suffix/suffix.go`
- Create: `internal/suffix/suffix_test.go`

- [ ] **Step 1: Write failing validation tests**

Add `internal/suffix/suffix_test.go`:

```go
package suffix

import "testing"

func TestValidateCustomSuffixUsesPublicSuffixList(t *testing.T) {
	tests := []struct {
		name    string
		suffix  string
		want    string
		wantErr string
	}{
		{name: "owned subtree", suffix: "local.todoordie.com", want: "local.todoordie.com"},
		{name: "co uk subtree", suffix: "local.todoordie.co.uk", want: "local.todoordie.co.uk"},
		{name: "trailing dot", suffix: "Local.TodoOrDie.Com.", want: "local.todoordie.com"},
		{name: "registrable apex", suffix: "todoordie.com", wantErr: "must be below registrable domain"},
		{name: "co uk apex", suffix: "todoordie.co.uk", wantErr: "must be below registrable domain"},
		{name: "www", suffix: "www.todoordie.com", wantErr: "must not start with www"},
		{name: "reserved local", suffix: "local", wantErr: "reserved"},
		{name: "reserved test", suffix: "test", wantErr: "reserved"},
		{name: "bare tld", suffix: "jp", wantErr: "registrable domain"},
		{name: "empty label", suffix: "local..todoordie.com", wantErr: "invalid DNS label"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateCustom(tt.suffix)
			if tt.wantErr != "" {
				if err == nil || !contains(err.Error(), tt.wantErr) {
					t.Fatalf("ValidateCustom(%q) err=%v want containing %q", tt.suffix, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateCustom(%q): %v", tt.suffix, err)
			}
			if got != tt.want {
				t.Fatalf("ValidateCustom(%q)=%q want %q", tt.suffix, got, tt.want)
			}
		})
	}
}

func TestManagedSuffixesIncludeBuiltInAndSortCustom(t *testing.T) {
	got := Managed([]string{"local.zed.com", "local.alpha.com"})
	want := []string{"lewp", "local.alpha.com", "local.zed.com"}
	if !equal(got, want) {
		t.Fatalf("Managed=%v want %v", got, want)
	}
}

func TestHostInManagedSuffix(t *testing.T) {
	managed := Managed([]string{"local.todoordie.com"})
	tests := []struct {
		host string
		want bool
	}{
		{"feature.local.todoordie.com", true},
		{"local.todoordie.com", true},
		{"feature.todoordie.com", false},
		{"feature.audit.lewp", true},
		{"example.com", false},
	}
	for _, tt := range tests {
		if got := HostInManagedSuffix(tt.host, managed); got != tt.want {
			t.Fatalf("HostInManagedSuffix(%q)=%v want %v", tt.host, got, tt.want)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
func equal(a, b []string) bool { return slices.Equal(a, b) }
```

Add imports in that test file:

```go
import (
	"slices"
	"strings"
	"testing"
)
```

- [ ] **Step 2: Run validation tests and verify failure**

Run:

```sh
go test ./internal/suffix
```

Expected: FAIL because `internal/suffix` does not exist.

- [ ] **Step 3: Implement suffix validation API**

Create `internal/suffix/suffix.go`:

```go
package suffix

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/net/publicsuffix"
)

const BuiltIn = "lewp"

type Config struct {
	Suffixes []string `toml:"suffixes"`
}

func ValidateCustom(input string) (string, error) {
	s, err := Normalize(input)
	if err != nil {
		return "", err
	}
	if s == BuiltIn {
		return "", fmt.Errorf("%q is built in", s)
	}
	labels := strings.Split(s, ".")
	if labels[0] == "www" {
		return "", fmt.Errorf("suffix %q must not start with www", s)
	}
	for _, reserved := range []string{"local", "localhost", "test", "invalid"} {
		if s == reserved || strings.HasSuffix(s, "."+reserved) {
			return "", fmt.Errorf("suffix %q uses reserved name %q", s, reserved)
		}
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(s)
	if err != nil {
		return "", fmt.Errorf("suffix %q has no registrable domain: %w", s, err)
	}
	if s == etld1 {
		return "", fmt.Errorf("suffix %q must be below registrable domain %q", s, etld1)
	}
	return s, nil
}

func Normalize(input string) (string, error) {
	s := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(input)), ".")
	if s == "" {
		return "", fmt.Errorf("suffix is empty")
	}
	for _, label := range strings.Split(s, ".") {
		if !validLabel(label) {
			return "", fmt.Errorf("suffix %q contains an invalid DNS label", input)
		}
	}
	return s, nil
}

func Managed(custom []string) []string {
	out := []string{BuiltIn}
	seen := map[string]bool{BuiltIn: true}
	for _, s := range custom {
		n, err := Normalize(s)
		if err != nil || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	slices.Sort(out[1:])
	return out
}

func HostInManagedSuffix(host string, managed []string) bool {
	h, err := Normalize(host)
	if err != nil {
		return false
	}
	for _, suffix := range managed {
		if h == suffix || strings.HasSuffix(h, "."+suffix) {
			return true
		}
	}
	return false
}

func Load(path string) (Config, error) {
	var cfg Config
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if _, err := toml.Decode(string(body), &cfg); err != nil {
		return cfg, err
	}
	normalized := make([]string, 0, len(cfg.Suffixes))
	for _, raw := range cfg.Suffixes {
		s, err := ValidateCustom(raw)
		if err != nil {
			return cfg, err
		}
		normalized = append(normalized, s)
	}
	cfg.Suffixes = uniqueSorted(normalized)
	return cfg, nil
}

func Save(path string, cfg Config) error {
	cfg.Suffixes = uniqueSorted(cfg.Suffixes)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Lewp-managed custom suffixes. .lewp is built in.\n")
	b.WriteString("suffixes = [")
	for i, s := range cfg.Suffixes {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(fmt.Sprintf("%q", s))
	}
	b.WriteString("]\n")
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func Add(cfg Config, raw ...string) (Config, []string, error) {
	added := []string{}
	all := append([]string{}, cfg.Suffixes...)
	seen := map[string]bool{}
	for _, existing := range all {
		seen[existing] = true
	}
	for _, r := range raw {
		s, err := ValidateCustom(r)
		if err != nil {
			return cfg, nil, err
		}
		if !seen[s] {
			seen[s] = true
			all = append(all, s)
			added = append(added, s)
		}
	}
	cfg.Suffixes = uniqueSorted(all)
	return cfg, added, nil
}

func Remove(cfg Config, raw string) (Config, string, bool, error) {
	s, err := ValidateCustom(raw)
	if err != nil {
		return cfg, "", false, err
	}
	out := cfg.Suffixes[:0]
	removed := false
	for _, existing := range cfg.Suffixes {
		if existing == s {
			removed = true
			continue
		}
		out = append(out, existing)
	}
	cfg.Suffixes = uniqueSorted(out)
	return cfg, s, removed, nil
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		s, err := Normalize(raw)
		if err != nil || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func validLabel(label string) bool {
	if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}
```

- [ ] **Step 4: Run suffix tests**

Run:

```sh
go test ./internal/suffix
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/suffix/suffix.go internal/suffix/suffix_test.go
git commit -m "feat: add managed suffix validation"
```

## Task 2: DNS Handles Managed Suffixes

**Files:**
- Modify: `internal/dns/dns.go`
- Modify: `internal/dns/dns_test.go`

- [ ] **Step 1: Write failing DNS test**

Add to `internal/dns/dns_test.go`:

```go
func TestHandleWithManagedSuffixesAnswersCustomSuffix(t *testing.T) {
	query := mustQuery(t, "feature-1.local.todoordie.com.", dnsmessage.TypeA)
	reply, err := HandleWithSuffixes(query, []string{"lewp", "local.todoordie.com"})
	if err != nil {
		t.Fatal(err)
	}
	answers := parseAnswers(t, reply)
	if len(answers) != 1 {
		t.Fatalf("answers=%d want 1", len(answers))
	}
	a, ok := answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer type=%T", answers[0].Body)
	}
	if got := net.IP(a.A[:]).String(); got != "127.0.0.1" {
		t.Fatalf("A=%s", got)
	}
}

func TestHandleWithManagedSuffixesRejectsUnmanagedHost(t *testing.T) {
	query := mustQuery(t, "feature-1.todoordie.com.", dnsmessage.TypeA)
	reply, err := HandleWithSuffixes(query, []string{"lewp", "local.todoordie.com"})
	if err != nil {
		t.Fatal(err)
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(reply)
	if err != nil {
		t.Fatal(err)
	}
	if header.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("rcode=%v want NXDOMAIN", header.RCode)
	}
}
```

Ensure imports include:

```go
import "net"
```

- [ ] **Step 2: Run DNS tests and verify failure**

Run:

```sh
go test ./internal/dns
```

Expected: FAIL because `HandleWithSuffixes` does not exist.

- [ ] **Step 3: Implement managed suffix DNS handler**

Update `internal/dns/dns.go`:

```go
import "github.com/scottwater/lewp/internal/suffix"
```

Replace `Handle` with wrapper + new function:

```go
func Handle(packet []byte) ([]byte, error) {
	return HandleWithSuffixes(packet, []string{suffix.BuiltIn})
}

func HandleWithSuffixes(packet []byte, managed []string) ([]byte, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(packet); err != nil {
		return nil, err
	}
	reply := dnsmessage.Message{
		Header: dnsmessage.Header{
			ID:            msg.Header.ID,
			Response:      true,
			Authoritative: true,
			RCode:         dnsmessage.RCodeSuccess,
		},
		Questions: msg.Questions,
	}
	if len(msg.Questions) == 0 || !suffix.HostInManagedSuffix(msg.Questions[0].Name.String(), managed) {
		reply.Header.RCode = dnsmessage.RCodeNameError
		return reply.Pack()
	}
	q := msg.Questions[0]
	switch q.Type {
	case dnsmessage.TypeA:
		reply.Answers = append(reply.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 1},
			Body:   &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}},
		})
	case dnsmessage.TypeAAAA:
		reply.Answers = append(reply.Answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: q.Type, Class: dnsmessage.ClassINET, TTL: 1},
			Body:   &dnsmessage.AAAAResource{AAAA: [16]byte{15: 1}},
		})
	default:
		reply.Header.RCode = dnsmessage.RCodeSuccess
	}
	return reply.Pack()
}
```

Add configured serve variant:

```go
func ServeWithSuffixes(ctx context.Context, addr string, managed []string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		reply, err := HandleWithSuffixes(buf[:n], managed)
		if err == nil {
			_, _ = conn.WriteTo(reply, peer)
		}
	}
}

func Serve(ctx context.Context, addr string) error {
	return ServeWithSuffixes(ctx, addr, []string{suffix.BuiltIn})
}
```

Remove the old `isLewp` helper if unused.

- [ ] **Step 4: Run DNS tests**

Run:

```sh
go test ./internal/dns
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/dns/dns.go internal/dns/dns_test.go
git commit -m "feat: resolve configured suffixes"
```

## Task 3: Setup Writes Additive Resolver Files

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`

- [ ] **Step 1: Write failing setup tests**

Add to `internal/cli/cli_test.go`:

```go
func TestRunSetupAddsCustomSuffixResolver(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	ran := []string{}
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "local.todoordie.com"},
		WorkDir:      dir,
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		LogDir:       dir + "/logs",
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand: func(_ context.Context, argv []string) error {
			ran = append(ran, strings.Join(argv, " "))
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(dir + "/resolver/lewp"); err != nil {
		t.Fatalf("missing lewp resolver: %v", err)
	}
	if _, err := os.Stat(dir + "/resolver/local.todoordie.com"); err != nil {
		t.Fatalf("missing custom resolver: %v", err)
	}
	body, err := os.ReadFile(dir + "/suffixes.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "local.todoordie.com") {
		t.Fatalf("suffix config missing custom suffix:\n%s", body)
	}
	if !strings.Contains(stdout.String(), "SUFFIX=local.todoordie.com") {
		t.Fatalf("stdout missing suffix: %q", stdout.String())
	}
	_ = ran
}

func TestRunSetupRejectsApexSuffix(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:         []string{"setup", "--suffix", "todoordie.com"},
		WorkDir:      dir,
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		LogDir:       dir + "/logs",
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand:   func(context.Context, []string) error { return nil },
	})
	if code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be below registrable domain") {
		t.Fatalf("stderr missing validation error: %q", stderr.String())
	}
}
```

- [ ] **Step 2: Run setup tests and verify failure**

Run:

```sh
go test ./internal/cli -run 'TestRunSetup(AddsCustomSuffixResolver|RejectsApexSuffix)'
```

Expected: FAIL because `Config.SuffixesPath` and `--suffix` do not exist.

- [ ] **Step 3: Add CLI config and default suffix path**

In `internal/cli/cli.go`, add field:

```go
SuffixesPath string
```

Set default in `Run`:

```go
if cfg.SuffixesPath == "" {
	cfg.SuffixesPath = defaultSuffixesPath()
}
```

Add helper near CA path helpers:

```go
func defaultSuffixesPath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home + "/Library/Application Support/lewp/suffixes.toml"
	}
	return "suffixes.toml"
}
```

- [ ] **Step 4: Parse repeated setup suffix flags**

Add type in `internal/cli/cli.go`:

```go
type repeatedStrings []string

func (r *repeatedStrings) String() string { return strings.Join(*r, ",") }
func (r *repeatedStrings) Set(v string) error {
	*r = append(*r, v)
	return nil
}
```

In `runSetup`:

```go
var suffixFlags repeatedStrings
fs.Var(&suffixFlags, "suffix", "add a managed suffix below an owned registrable domain")
```

- [ ] **Step 5: Load/add/save suffixes before resolver writes**

Import package:

```go
"github.com/scottwater/lewp/internal/suffix"
```

In `runSetup`, after CA/log setup succeeds and before resolver writes:

```go
suffixCfg, err := suffix.Load(cfg.SuffixesPath)
if err != nil {
	fmt.Fprintf(cfg.Stderr, "read suffix config: %v\n", err)
	return 1
}
suffixCfg, addedSuffixes, err := suffix.Add(suffixCfg, suffixFlags...)
if err != nil {
	fmt.Fprintf(cfg.Stderr, "validate suffix: %v\n", err)
	return 1
}
if len(suffixFlags) > 0 {
	if err := suffix.Save(cfg.SuffixesPath, suffixCfg); err != nil {
		fmt.Fprintf(cfg.Stderr, "write suffix config: %v\n", err)
		return 1
	}
}
```

- [ ] **Step 6: Write resolver file per managed suffix**

Replace `writeResolver(cfg)` with:

```go
if err := writeResolvers(cfg, suffix.Managed(suffixCfg.Suffixes)); err != nil {
	fmt.Fprintf(cfg.Stderr, "write resolver file: %v\n", err)
	fmt.Fprintln(cfg.Stderr, "Next: confirm you can run sudo (the failing command is shown above), then re-run: lewp setup")
	return 1
}
if err := validateResolvers(cfg, suffix.Managed(suffixCfg.Suffixes)); err != nil {
	fmt.Fprintf(cfg.Stderr, "verify resolver file: %v\n", err)
	return 1
}
```

Add helpers:

```go
func resolverPathFor(cfg Config, managedSuffix string) string {
	if managedSuffix == suffix.BuiltIn {
		return cfg.ResolverPath
	}
	return filepath.Join(filepath.Dir(cfg.ResolverPath), managedSuffix)
}

func writeResolvers(cfg Config, managed []string) error {
	for _, s := range managed {
		if err := writeResolverPath(cfg, resolverPathFor(cfg, s)); err != nil {
			return err
		}
	}
	return nil
}

func writeResolverPath(cfg Config, path string) error {
	if strings.HasPrefix(path, "/etc/resolver/") && os.Geteuid() != 0 {
		tmp, err := os.CreateTemp("", "lewp-resolver-*")
		if err != nil {
			return err
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, err := tmp.WriteString(dns.ResolverFile(dns.DefaultPort)); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := cfg.RunCommand(context.Background(), []string{"sudo", "mkdir", "-p", filepath.Dir(path)}); err != nil {
			return err
		}
		return cfg.RunCommand(context.Background(), []string{"sudo", "install", "-m", "0644", tmpPath, path})
	}
	return dns.WriteResolverFile(path, dns.DefaultPort)
}
```

Update `validateResolver` into plural:

```go
func validateResolvers(cfg Config, managed []string) error {
	for _, s := range managed {
		if err := validateResolver(resolverPathFor(cfg, s)); err != nil {
			return err
		}
	}
	return nil
}
```

Add `path/filepath` import.

- [ ] **Step 7: Print added suffixes**

In `runSetup` output block:

```go
for _, s := range addedSuffixes {
	fmt.Fprintf(cfg.Stdout, "SUFFIX=%s\n", s)
	fmt.Fprintf(cfg.Stdout, "RESOLVER_%s=%s\n", strings.ToUpper(strings.ReplaceAll(s, ".", "_")), resolverPathFor(cfg, s))
}
```

- [ ] **Step 8: Run setup tests**

Run:

```sh
go test ./internal/cli -run 'TestRunSetup(AddsCustomSuffixResolver|RejectsApexSuffix)'
```

Expected: PASS.

- [ ] **Step 9: Commit**

```sh
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat: add suffix setup"
```

## Task 4: Suffix List And Remove Commands

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/help.go`
- Modify: `internal/cli/cli_test.go`

- [ ] **Step 1: Write failing suffix command tests**

Add to `internal/cli/cli_test.go`:

```go
func TestRunSuffixListShowsBuiltInAndCustom(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/suffixes.toml", []byte("suffixes = [\"local.todoordie.com\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:         []string{"suffix", "list"},
		WorkDir:      dir,
		SuffixesPath: dir + "/suffixes.toml",
		Stdout:       &stdout,
		Stderr:       &stderr,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"lewp\tbuilt-in", "local.todoordie.com\tcustom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("suffix list missing %q:\n%s", want, got)
		}
	}
}

func TestRunSuffixRemoveDeletesConfigAndResolver(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/resolver", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/suffixes.toml", []byte("suffixes = [\"local.todoordie.com\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/resolver/local.todoordie.com", []byte("nameserver 127.0.0.1\nport 15353\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:         []string{"suffix", "remove", "local.todoordie.com"},
		WorkDir:      dir,
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/suffixes.toml",
		Stdout:       &stdout,
		Stderr:       &stderr,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(dir + "/resolver/local.todoordie.com"); !os.IsNotExist(err) {
		t.Fatalf("resolver still exists or stat failed: %v", err)
	}
	body, err := os.ReadFile(dir + "/suffixes.toml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "local.todoordie.com") {
		t.Fatalf("suffix still in config:\n%s", body)
	}
	if !strings.Contains(stdout.String(), "removed suffix local.todoordie.com") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
```

- [ ] **Step 2: Run suffix command tests and verify failure**

Run:

```sh
go test ./internal/cli -run 'TestRunSuffix(List|Remove)'
```

Expected: FAIL because `suffix` command does not exist.

- [ ] **Step 3: Add `suffix` command dispatch**

In `Run` switch:

```go
case "suffix":
	if helpRequested(cfg.Args[1:]) {
		fmt.Fprint(cfg.Stdout, suffixHelp)
		return 0
	}
	return runSuffix(cfg)
```

Add implementation:

```go
func runSuffix(cfg Config) int {
	if len(cfg.Args) < 2 {
		fmt.Fprint(cfg.Stderr, suffixHelp)
		return 2
	}
	switch cfg.Args[1] {
	case "list":
		return runSuffixList(cfg)
	case "remove":
		return runSuffixRemove(cfg)
	default:
		fmt.Fprintf(cfg.Stderr, "lewp suffix: unknown subcommand %q\n\n", cfg.Args[1])
		fmt.Fprint(cfg.Stderr, suffixHelp)
		return 2
	}
}

func runSuffixList(cfg Config) int {
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "read suffix config: %v\n", err)
		return 1
	}
	fmt.Fprintln(cfg.Stdout, "SUFFIX\tKIND")
	fmt.Fprintf(cfg.Stdout, "%s\tbuilt-in\n", suffix.BuiltIn)
	for _, s := range suffixCfg.Suffixes {
		fmt.Fprintf(cfg.Stdout, "%s\tcustom\n", s)
	}
	return 0
}

func runSuffixRemove(cfg Config) int {
	if len(cfg.Args) != 3 {
		fmt.Fprintln(cfg.Stderr, "usage: lewp suffix remove <suffix>")
		return 2
	}
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "read suffix config: %v\n", err)
		return 1
	}
	updated, removedSuffix, removed, err := suffix.Remove(suffixCfg, cfg.Args[2])
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "validate suffix: %v\n", err)
		return 1
	}
	if !removed {
		fmt.Fprintf(cfg.Stdout, "suffix %s is not configured\n", removedSuffix)
		return 0
	}
	resolverPath := resolverPathFor(cfg, removedSuffix)
	if err := ensureLewpOwnedOrMissing(resolverPath, dns.ResolverFile(dns.DefaultPort)); err != nil {
		fmt.Fprintf(cfg.Stderr, "refusing to remove resolver file: %v\n", err)
		return 1
	}
	if err := removePath(cfg, resolverPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(cfg.Stderr, "remove resolver file: %v\n", err)
		return 1
	}
	if err := suffix.Save(cfg.SuffixesPath, updated); err != nil {
		fmt.Fprintf(cfg.Stderr, "write suffix config: %v\n", err)
		return 1
	}
	fmt.Fprintf(cfg.Stdout, "removed suffix %s\n", removedSuffix)
	fmt.Fprintf(cfg.Stdout, "removed resolver %s\n", resolverPath)
	return 0
}
```

- [ ] **Step 4: Add help text**

In `internal/cli/help.go`, add main command line:

```text
  suffix    List or remove custom managed suffixes
```

Add help const:

```go
suffixHelp = `lewp suffix — manage custom suffixes

Usage:
  lewp suffix list
  lewp suffix remove <suffix>

Custom suffixes are added explicitly with "lewp setup --suffix <suffix>".
.lewp is built in and cannot be removed.
`
```

- [ ] **Step 5: Run suffix command tests**

Run:

```sh
go test ./internal/cli -run 'TestRunSuffix(List|Remove)'
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add internal/cli/cli.go internal/cli/help.go internal/cli/cli_test.go
git commit -m "feat: manage custom suffixes"
```

## Task 5: Lease Host Validation Uses Configured Suffixes

**Files:**
- Modify: `internal/identity/identity.go`
- Modify: `internal/identity/identity_test.go`
- Modify: `internal/control/service.go`
- Modify: `internal/control/service_test.go`

- [ ] **Step 1: Write failing identity tests**

Add to `internal/identity/identity_test.go`:

```go
func TestValidateHostAllowsConfiguredSuffix(t *testing.T) {
	if err := ValidateHostForSuffixes("feature.local.todoordie.com", []string{"lewp", "local.todoordie.com"}); err != nil {
		t.Fatalf("configured suffix host rejected: %v", err)
	}
	if err := ValidateHostForSuffixes("feature.todoordie.com", []string{"lewp", "local.todoordie.com"}); err == nil {
		t.Fatal("unconfigured suffix host accepted")
	}
}
```

- [ ] **Step 2: Implement identity suffix-aware validation**

In `internal/identity/identity.go`, import:

```go
"github.com/scottwater/lewp/internal/suffix"
```

Add option:

```go
ManagedSuffixes []string
```

Change validation call:

```go
managed := opts.ManagedSuffixes
if len(managed) == 0 {
	managed = []string{suffix.BuiltIn}
}
if err := ValidateHostForSuffixes(host, managed); err != nil {
	return Result{}, err
}
```

Add function:

```go
func ValidateHostForSuffixes(host string, managed []string) error {
	host = normalizeHost(host)
	if !suffix.HostInManagedSuffix(host, managed) {
		return fmt.Errorf("host %q must be inside a configured Lewp suffix", host)
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return fmt.Errorf("host %q contains an invalid DNS label", host)
		}
		norm, _, err := NormalizeLabel(label)
		if err != nil || norm != label {
			return fmt.Errorf("host %q contains an invalid DNS label %q", host, label)
		}
	}
	return nil
}
```

Keep `ValidateHost(host string)` as compatibility wrapper:

```go
func ValidateHost(host string) error {
	return ValidateHostForSuffixes(host, []string{suffix.BuiltIn})
}
```

- [ ] **Step 3: Run identity tests**

Run:

```sh
go test ./internal/identity
```

Expected: PASS.

- [ ] **Step 4: Write failing service test**

Add to `internal/control/service_test.go`:

```go
func TestServiceLeaseAllowsConfiguredCustomSuffix(t *testing.T) {
	store := openStore(t)
	svc := NewService(store, registry.PortRange{Start: 41000, End: 41020})
	svc.SetManagedSuffixes([]string{"lewp", "local.todoordie.com"})
	ctx := context.Background()
	dir := t.TempDir()

	lease, err := svc.Lease(ctx, LeaseRequest{
		WorkDir: dir,
		Host:    "feature-1.local.todoordie.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Host != "feature-1.local.todoordie.com" {
		t.Fatalf("host=%q", lease.Host)
	}
}
```

- [ ] **Step 5: Add service managed suffix injection**

In `internal/control/service.go`, add field:

```go
managedSuffixes []string
```

Update constructor:

```go
func NewService(store *registry.Store, portRange registry.PortRange) *Service {
	return &Service{store: store, portRange: portRange, managedSuffixes: []string{suffix.BuiltIn}}
}
```

Add setter:

```go
func (s *Service) SetManagedSuffixes(managed []string) {
	s.managedSuffixes = suffix.Managed(managed)
}
```

Pass into identity resolve:

```go
ManagedSuffixes: s.managedSuffixes,
```

Add import:

```go
"github.com/scottwater/lewp/internal/suffix"
```

- [ ] **Step 6: Run control tests**

Run:

```sh
go test ./internal/control
```

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add internal/identity/identity.go internal/identity/identity_test.go internal/control/service.go internal/control/service_test.go
git commit -m "feat: validate hosts against managed suffixes"
```

## Task 6: Daemon Loads Suffixes For DNS And Control

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/control/socket.go`
- Modify: `internal/control/socket_test.go`
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Add daemon config suffixes**

In `internal/daemon/daemon.go`, add config field:

```go
ManagedSuffixes []string
```

The current `internal/daemon` package constructs the HTTP proxy, not the
control service. This field is wired into proxy construction in Task 7. Control
service suffix injection is handled in `internal/control/socket.go` in the next
step.

- [ ] **Step 2: Add suffix-aware control socket serve**

In `internal/control/socket.go`, keep the existing `Serve` wrapper and add
`ServeWithSuffixes`:

```go
func Serve(ctx context.Context, socketPath, registryPath string, portRange registry.PortRange) error {
	return ServeWithSuffixes(ctx, socketPath, registryPath, portRange, []string{suffix.BuiltIn})
}

func ServeWithSuffixes(ctx context.Context, socketPath, registryPath string, portRange registry.PortRange, managedSuffixes []string) error {
	if err := os.MkdirAll(dir(socketPath), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir(socketPath), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(dir(registryPath), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir(registryPath), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	defer ln.Close()

	store, err := registry.Open(registryPath)
	if err != nil {
		return err
	}
	defer store.Close()
	svc := NewService(store, portRange)
	svc.SetManagedSuffixes(managedSuffixes)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return err
		}
		go handle(conn, svc)
	}
}
```

Add import:

```go
"github.com/scottwater/lewp/internal/suffix"
```

- [ ] **Step 3: Load suffixes in CLI daemon command**

In `runDaemon`, before starting goroutines:

```go
suffixCfg, err := suffix.Load(cfg.SuffixesPath)
if err != nil {
	fmt.Fprintf(cfg.Stderr, "read suffix config: %v\n", err)
	return 1
}
managedSuffixes := suffix.Managed(suffixCfg.Suffixes)
```

Change DNS serve goroutine:

```go
errs <- dns.ServeWithSuffixes(ctx, fmt.Sprintf("127.0.0.1:%d", dns.DefaultPort), managedSuffixes)
```

Change control socket goroutine:

```go
errs <- control.ServeWithSuffixes(ctx, cfg.SocketPath, control.DefaultRegistryPath(), registry.PortRange{Start: 41000, End: 49999}, managedSuffixes)
```

Pass into daemon:

```go
ManagedSuffixes: managedSuffixes,
```

- [ ] **Step 4: Run daemon/control tests**

Run:

```sh
go test ./internal/daemon ./internal/control ./internal/cli
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/cli/cli.go internal/control/socket.go internal/control/socket_test.go internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat: load suffixes in daemon"
```

## Task 7: Proxy Unknown Host Uses Managed Suffixes

**Files:**
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`
- Modify: `internal/daemon/daemon.go`

- [ ] **Step 1: Write failing proxy test**

Add to `internal/proxy/proxy_test.go`:

```go
func TestProxyUnknownConfiguredSuffixShowsLewpDebugPage(t *testing.T) {
	store := openProxyStore(t)
	handler := NewWithSuffixes(store, []string{"lewp", "local.todoordie.com"})

	req := httptest.NewRequest(http.MethodGet, "http://missing.local.todoordie.com/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "missing.local.todoordie.com is not registered with Lewp") {
		t.Fatalf("custom suffix did not get Lewp debug page:\n%s", body)
	}
}
```

- [ ] **Step 2: Implement proxy managed suffixes**

In `internal/proxy/proxy.go`, add field:

```go
managedSuffixes []string
```

Add constructor:

```go
func NewWithSuffixes(store *registry.Store, managed []string) *Proxy {
	return &Proxy{store: store, lastSeen: map[int64]time.Time{}, managedSuffixes: suffix.Managed(managed)}
}
```

Update existing constructor:

```go
func New(store *registry.Store) *Proxy {
	return NewWithSuffixes(store, []string{suffix.BuiltIn})
}
```

Import suffix package:

```go
"github.com/scottwater/lewp/internal/suffix"
```

Change unknown host check:

```go
if suffix.HostInManagedSuffix(host, p.managedSuffixes) {
	writeUnknownRoutePage(w, host)
	p.logRequest(r, host, "", http.StatusNotFound, errors.New("no route registered"))
	return
}
```

- [ ] **Step 3: Wire daemon proxy constructor**

In `internal/daemon/daemon.go`, replace:

```go
proxy.New(store)
```

with:

```go
proxy.NewWithSuffixes(store, cfg.ManagedSuffixes)
```

- [ ] **Step 4: Run proxy tests**

Run:

```sh
go test ./internal/proxy ./internal/daemon
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/proxy/proxy.go internal/proxy/proxy_test.go internal/daemon/daemon.go
git commit -m "feat: show Lewp errors for custom suffixes"
```

## Task 8: Doctor And Uninstall Cover Custom Suffixes

**Files:**
- Modify: `internal/cli/doctor.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`

- [ ] **Step 1: Write uninstall test**

Add to `internal/cli/cli_test.go`:

```go
func TestSystemUninstallRemovesCustomSuffixResolvers(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/resolver", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir + "/resolver/lewp", dir + "/resolver/local.todoordie.com"} {
		if err := os.WriteFile(path, []byte("nameserver 127.0.0.1\nport 15353\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(dir+"/suffixes.toml", []byte("suffixes = [\"local.todoordie.com\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:         []string{"system", "uninstall", "--yes"},
		WorkDir:      dir,
		ResolverPath: dir + "/resolver/lewp",
		SuffixesPath: dir + "/suffixes.toml",
		PlistPath:    dir + "/LaunchAgents/dev.lewp.daemon.plist",
		CAPath:       dir + "/ca.pem",
		CAKeyPath:    dir + "/ca-key.pem",
		Stdout:       &stdout,
		Stderr:       &stderr,
		RunCommand:   func(context.Context, []string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(dir + "/resolver/local.todoordie.com"); !os.IsNotExist(err) {
		t.Fatalf("custom resolver still exists or stat failed: %v", err)
	}
}
```

- [ ] **Step 2: Update uninstall path collection**

In `runSystemUninstall`, load suffix config:

```go
suffixCfg, _ := suffix.Load(cfg.SuffixesPath)
for _, s := range suffixCfg.Suffixes {
	path := resolverPathFor(cfg, s)
	if err := ensureLewpOwnedOrMissing(path, dns.ResolverFile(dns.DefaultPort)); err != nil {
		fmt.Fprintf(cfg.Stderr, "refusing to remove resolver file: %v\n", err)
		return 1
	}
	if err := removePath(cfg, path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(cfg.Stderr, "remove resolver file: %v\n", err)
		return 1
	}
}
```

Also remove `cfg.SuffixesPath` after resolver removal:

```go
_ = os.Remove(cfg.SuffixesPath)
```

- [ ] **Step 3: Update doctor suffix reporting**

In `internal/cli/doctor.go`, after resolver check, load suffix config and append one check per custom suffix:

```go
func suffixResolverChecks(cfg Config) []doctorCheck {
	suffixCfg, err := suffix.Load(cfg.SuffixesPath)
	if err != nil {
		return []doctorCheck{{Name: "custom suffixes", Status: statusFail, Detail: err.Error()}}
	}
	checks := []doctorCheck{}
	for _, s := range suffixCfg.Suffixes {
		path := resolverPathFor(cfg, s)
		c := doctorCheck{Name: "resolver " + s, Inspect: path}
		body, err := os.ReadFile(path)
		if err != nil {
			c.Status = statusFail
			c.Detail = err.Error()
		} else if port, ok := dns.ResolverPort(string(body)); !ok || port != dns.DefaultPort {
			c.Status = statusFail
			c.Detail = "resolver does not point at Lewp DNS port"
		} else {
			c.Status = statusOK
			c.Detail = fmt.Sprintf("%s -> port %d", path, port)
		}
		checks = append(checks, c)
	}
	return checks
}
```

Call it from `runDoctor`:

```go
checks = append(checks, suffixResolverChecks(cfg)...)
```

- [ ] **Step 4: Run CLI tests**

Run:

```sh
go test ./internal/cli
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/cli/cli.go internal/cli/doctor.go internal/cli/cli_test.go
git commit -m "feat: diagnose custom suffixes"
```

## Task 9: Docs And End-To-End Verification

**Files:**
- Modify: `README.md`
- Modify: `DOCUMENTATION.md`

- [ ] **Step 1: Update README**

Add a concise section after hostname configuration:

```md
### Custom public dev suffixes

`.lewp` remains the default. For OAuth providers that reject private TLDs, use
an owned public-domain subtree:

```sh
lewp setup --suffix local.todoordie.com
lewp add --host feature-1.local.todoordie.com
```

Lewp installs a resolver only for `local.todoordie.com`, so `todoordie.com` and
`www.todoordie.com` continue to use normal DNS. Custom suffixes must be below a
registrable domain; apex domains such as `todoordie.com` and `todoordie.co.uk`
are rejected.
```

- [ ] **Step 2: Update DOCUMENTATION**

Document:

```md
lewp setup [--suffix S]
lewp suffix list
lewp suffix remove S
```

Include examples:

```sh
lewp setup --suffix local.todoordie.com
lewp suffix list
lewp suffix remove local.todoordie.com
```

State:

- setup is additive
- `.lewp` is built in
- `system uninstall` removes custom suffix resolver files
- `add --host` rejects unconfigured suffixes

- [ ] **Step 3: Run full test suite**

Run:

```sh
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Build binary**

Run:

```sh
go build ./cmd/lewp
```

Expected: PASS and `./lewp` binary produced in repo root or command completes without errors depending on Go output path.

- [ ] **Step 5: Commit docs**

```sh
git add README.md DOCUMENTATION.md
git commit -m "docs: document custom suffixes"
```

## Task 10: Final Manual Smoke Test

**Files:**
- No source changes expected.

- [ ] **Step 1: Inspect commit series**

Run:

```sh
git log --oneline -10
```

Expected: includes the custom suffix implementation commits in task order.

- [ ] **Step 2: Run focused command smoke tests with temp paths**

Run:

```sh
tmp="$(mktemp -d)"
go test ./internal/cli ./internal/dns ./internal/control ./internal/proxy
```

Expected: PASS.

- [ ] **Step 3: Run final full verification**

Run:

```sh
go test ./... && go build ./cmd/lewp
```

Expected: PASS.

- [ ] **Step 4: Summarize residual manual verification**

Before marking complete, report that real macOS resolver behavior still needs a manual installed smoke test:

```sh
lewp setup --suffix local.todoordie.com --start
lewp doctor
dig doctor.local.todoordie.com @127.0.0.1 -p 15353
```

Expected: loopback DNS answer and doctor suffix resolver checks passing.
