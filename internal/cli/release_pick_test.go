package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/scottwater/lewp/internal/control"
)

// leasePickDirs leases one route per new directory and returns the directories
// in the sorted order the picker lists them.
func leasePickDirs(t *testing.T, socketPath string, n int) []string {
	t.Helper()
	dirs := make([]string, n)
	for i := range dirs {
		dirs[i] = t.TempDir()
		runOK(t, Config{Args: []string{"lease", "--root", "work", "--name", fmt.Sprintf("pick-%d", i)}, WorkDir: dirs[i], SocketPath: socketPath})
	}
	sort.Strings(dirs)
	return dirs
}

func runPickRelease(t *testing.T, socketPath, input string, args ...string) (int, string, string) {
	t.Helper()
	opts, err := parseReleaseArgs(append([]string{"--pick"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := executeRelease(Config{WorkDir: t.TempDir(), SocketPath: socketPath, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(input)}, opts, true)
	return code, stdout.String(), stderr.String()
}

func TestRunReleasePickReleasesOnlySelectedPaths(t *testing.T) {
	socketPath := startTestDaemon(t)
	dirs := leasePickDirs(t, socketPath, 3)
	code, stdout, stderr := runPickRelease(t, socketPath, "1, 3\ny\n")
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, want := range []string{"Choose paths to release:", "Release plan:", "Planned releases: 2\n", "Proceed? [y/N] ", "Released: 2\nForgotten: 0\n"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output missing %q:\n%s", want, stdout)
		}
	}
	assertNoActiveInfo(t, socketPath, dirs[0])
	assertInfoContains(t, socketPath, dirs[1], ".work.lewp")
	assertNoActiveInfo(t, socketPath, dirs[2])
}

func TestRunReleasePickAbortsWithoutChanges(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{name: "blank selection", input: "\n"},
		{name: "eof", input: ""},
		{name: "declined confirmation", input: "all\nn\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socketPath := startTestDaemon(t)
			dirs := leasePickDirs(t, socketPath, 2)
			code, stdout, stderr := runPickRelease(t, socketPath, tc.input)
			if code != 1 || !strings.Contains(stdout, "release aborted") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			for _, dir := range dirs {
				assertInfoContains(t, socketPath, dir, ".work.lewp")
			}
		})
	}
}

func TestRunReleasePickDryRunAndYes(t *testing.T) {
	socketPath := startTestDaemon(t)
	dirs := leasePickDirs(t, socketPath, 2)
	code, stdout, stderr := runPickRelease(t, socketPath, "all\n", "--dry-run")
	if code != 0 || !strings.Contains(stdout, "Planned releases: 2\n") || strings.Contains(stdout, "Proceed?") {
		t.Fatalf("dry run: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, dir := range dirs {
		assertInfoContains(t, socketPath, dir, ".work.lewp")
	}

	code, stdout, stderr = runPickRelease(t, socketPath, "2\n", "--yes")
	if code != 0 || strings.Contains(stdout, "Proceed?") || !strings.HasSuffix(stdout, "Released: 1\nForgotten: 0\n") {
		t.Fatalf("--yes: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertInfoContains(t, socketPath, dirs[0], ".work.lewp")
	assertNoActiveInfo(t, socketPath, dirs[1])
}

func TestRunReleasePickInvalidSelection(t *testing.T) {
	socketPath := startTestDaemon(t)
	leasePickDirs(t, socketPath, 2)
	code, _, stderr := runPickRelease(t, socketPath, "3\n")
	if code != 2 || !strings.Contains(stderr, "between 1 and 2") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestRunReleasePickRequiresTerminal(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"release", "--pick"}, WorkDir: t.TempDir(), SocketPath: "/nonexistent.sock", Stdout: &stdout, Stderr: &stderr, Stdin: &bytes.Buffer{}})
	if code != 2 || !strings.Contains(stderr.String(), "--pick requires an interactive terminal") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestParseReleasePickRejectsSelectors(t *testing.T) {
	for _, args := range [][]string{
		{"--pick", "--path", "/work"},
		{"--pick", "--host", "app.work.lewp"},
		{"--pick", "--port", "42137"},
		{"--pick", "--route"},
		{"--pick", "--name", "vite"},
		{"--pick", "--json"},
	} {
		if _, err := parseReleaseArgs(args); err == nil || !strings.Contains(err.Error(), "--pick can only be combined") {
			t.Fatalf("%v: err=%v", args, err)
		}
	}
	if _, err := parseReleaseArgs([]string{"--pick", "--forget", "--dry-run", "-y"}); err != nil {
		t.Fatal(err)
	}
}

func TestParsePickSelection(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []int
	}{
		{input: "", want: nil},
		{input: "2", want: []int{1}},
		{input: "3 1,1", want: []int{0, 2}},
		{input: "2-4", want: []int{1, 2, 3}},
		{input: " ALL ", want: []int{0, 1, 2, 3}},
	} {
		got, err := parsePickSelection(tc.input, 4)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: got %v err %v, want %v", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"0", "5", "3-2", "x", "1-"} {
		if _, err := parsePickSelection(input, 4); err == nil {
			t.Fatalf("%q: expected error", input)
		}
	}
}

func TestRunPickerKeys(t *testing.T) {
	entries := []pickEntry{{path: "/a", display: "/a"}, {path: "/b", display: "/b"}, {path: "/c", display: "/c"}}
	for _, tc := range []struct {
		name, keys string
		want       []string
		cancelled  bool
	}{
		{name: "space and arrows", keys: " j\x1b[B \r", want: []string{"/a", "/c"}},
		{name: "up arrow and x", keys: "jj\x1b[Ax\r", want: []string{"/b"}},
		{name: "toggle all", keys: "a\r", want: []string{"/a", "/b", "/c"}},
		{name: "toggle all twice", keys: "aa\r", want: nil},
		{name: "q cancels", keys: " q", cancelled: true},
		{name: "ctrl-c cancels", keys: " \x03", cancelled: true},
		{name: "bare esc cancels", keys: " \x1b", cancelled: true},
		{name: "eof cancels", keys: " ", cancelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runPicker(bufio.NewReader(strings.NewReader(tc.keys)), io.Discard, entries, 80, 24)
			if tc.cancelled {
				if !errors.Is(err, errPickCancelled) {
					t.Fatalf("got %v err %v, want cancel", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v err %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestRunPickerScrollsLongLists(t *testing.T) {
	entries := make([]pickEntry, 10)
	for i := range entries {
		entries[i] = pickEntry{path: fmt.Sprintf("/p%d", i), display: fmt.Sprintf("/p%d", i)}
	}
	var out bytes.Buffer
	got, err := runPicker(bufio.NewReader(strings.NewReader(strings.Repeat("j", 9)+" \r")), &out, entries, 80, 6)
	if err != nil || !reflect.DeepEqual(got, []string{"/p9"}) {
		t.Fatalf("got %v err %v", got, err)
	}
	if !strings.Contains(out.String(), "(8-10 of 10)") {
		t.Fatalf("scrolled window not rendered:\n%q", out.String())
	}
}

func TestGroupPickEntries(t *testing.T) {
	got := groupPickEntries([]control.ListEntry{
		{Path: "/home/me/web", Name: "vite", Port: 41003, State: "stale", Kind: "port"},
		{Path: "/home/me/web", Host: "*.web.work.lewp", Port: 41001, State: "stale", Kind: "alias"},
		{Path: "/home/me/web", Host: "web.work.lewp", Port: 41001, State: "stale", Kind: "route"},
		{Path: "/elsewhere/api", Host: "api.work.lewp", Port: 41002, State: "down", Kind: "route"},
		{Path: "/home/me/old", Host: "old.work.lewp", Port: 41004, State: "released", Kind: "route"},
		{Path: "/home/me/ports", Name: "redis", Port: 41005, State: "up", Kind: "port"},
	}, "/home/me")
	want := []pickEntry{
		{path: "/elsewhere/api", display: "/elsewhere/api", host: "api.work.lewp"},
		{path: "/home/me/old", display: "~/old", state: "released", host: "old.work.lewp"},
		{path: "/home/me/ports", display: "~/ports", host: "port redis"},
		{path: "/home/me/web", display: "~/web", state: "stale", host: "web.work.lewp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestWritePickPreviewShowsOneLinePerPath(t *testing.T) {
	var out bytes.Buffer
	entries := []pickEntry{
		{host: "brakeman.local.kickofflabs.com", state: "stale", display: "~/work/app/master/.worktrees/security-password-policy"},
		{host: "app.work.lewp", display: "~/work/app"},
	}
	if err := writePickPreview(&out, entries, 7, 0); err != nil {
		t.Fatal(err)
	}
	want := "Release plan:\n" +
		"  brakeman.local.kickofflabs.com  stale  …ter/.worktrees/security-password-policy\n" +
		"  app.work.lewp                          ~/work/app\n" +
		"Planned releases: 7\nPlanned forgets: 0\n"
	if out.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}
