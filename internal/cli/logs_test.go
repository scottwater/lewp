package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRunLogsShowsTailOfBothLogs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/daemon.out.log", []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/daemon.err.log", []byte("boom: bind failed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:   []string{"logs", "--lines", "2"},
		LogDir: dir,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"daemon.out.log", "line2", "line3", "daemon.err.log", "boom: bind failed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("logs output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "line1") {
		t.Fatalf("--lines 2 should not include line1:\n%s", got)
	}
}

func TestRunLogsPathOnly(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:   []string{"logs", "--path"},
		LogDir: dir,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, dir+"/daemon.out.log") || !strings.Contains(got, dir+"/daemon.err.log") {
		t.Fatalf("--path missing log paths:\n%s", got)
	}
}

func TestRunLogsReportsMissingLogs(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(Config{
		Args:   []string{"logs"},
		LogDir: dir,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(stderr.String(), "No Lewp logs found yet") {
		t.Fatalf("missing-log guidance absent: %q", stderr.String())
	}
}

func TestDrainFromReadsOnlyAppendedLogData(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/daemon.out.log"
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	offset := int64(len("before\n"))
	if err := os.WriteFile(path, []byte("before\nafter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	next := drainFrom(&out, path, offset)
	if out.String() != "after\n" {
		t.Fatalf("drained %q", out.String())
	}
	if next != int64(len("before\nafter\n")) {
		t.Fatalf("next offset=%d", next)
	}
}

func TestRunLogsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(Config{Args: []string{"logs", "--help"}, Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	got := stdout.String()
	if !strings.Contains(got, "lewp logs") || !strings.Contains(got, "--follow") {
		t.Fatalf("logs help missing content:\n%s", got)
	}
}
