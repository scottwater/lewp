package cli

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

// logFile pairs a human label with the on-disk path of a daemon log written by
// launchd (StandardOutPath / StandardErrorPath).
type logFile struct {
	label string
	path  string
}

func daemonLogFiles(cfg Config) []logFile {
	return []logFile{
		{label: "stdout", path: cfg.LogDir + "/daemon.out.log"},
		{label: "stderr", path: cfg.LogDir + "/daemon.err.log"},
	}
}

func runLogs(cfg Config) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(cfg.Stderr)
	lines := fs.Int("lines", 50, "")
	follow := fs.Bool("follow", false, "")
	fs.BoolVar(follow, "f", false, "")
	pathsOnly := fs.Bool("path", false, "")
	if fs.Parse(cfg.Args[1:]) != nil {
		return 2
	}

	files := daemonLogFiles(cfg)

	if *pathsOnly {
		for _, f := range files {
			fmt.Fprintln(cfg.Stdout, f.path)
		}
		return 0
	}

	missing := 0
	for i, f := range files {
		if i > 0 {
			fmt.Fprintln(cfg.Stdout)
		}
		fmt.Fprintf(cfg.Stdout, "==> %s (%s) <==\n", f.path, f.label)
		body, err := tailLines(f.path, *lines)
		if err != nil {
			missing++
			fmt.Fprintf(cfg.Stdout, "(no log yet: %v)\n", err)
			continue
		}
		if strings.TrimSpace(body) == "" {
			fmt.Fprintln(cfg.Stdout, "(empty)")
			continue
		}
		fmt.Fprint(cfg.Stdout, body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Fprintln(cfg.Stdout)
		}
	}

	if missing == len(files) {
		fmt.Fprintln(cfg.Stderr, "No Lewp logs found yet.")
		fmt.Fprintf(cfg.Stderr, "Expected under %s after: lewp setup && lewp system start\n", cfg.LogDir)
	}

	if *follow {
		return followLogs(cfg, files)
	}
	return 0
}

// tailLines returns the last n lines of the file at path. Daemon logs are small
// in practice, so it reads the whole file rather than seeking from the end.
func tailLines(path string, n int) (string, error) {
	if n <= 0 {
		n = 50
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(data)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n") + "\n", nil
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n", nil
}

// followLogs tails appended content from each log file until interrupted. It is
// intentionally not exercised by unit tests (it blocks on new output); the
// non-follow read above is the deterministic path.
func followLogs(cfg Config, files []logFile) int {
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	defer signal.Stop(sigc)

	offsets := make([]int64, len(files))
	for i, f := range files {
		if info, err := os.Stat(f.path); err == nil {
			offsets[i] = info.Size()
		}
	}

	fmt.Fprintln(cfg.Stderr, "Following logs; press Ctrl-C to stop.")
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-sigc:
			return 0
		case <-ticker.C:
			for i, f := range files {
				offsets[i] = drainFrom(cfg.Stdout, f.path, offsets[i])
			}
		}
	}
}

// drainFrom copies any bytes appended past offset to w and returns the new
// offset. A truncated/rotated file (smaller than offset) resets to the start.
func drainFrom(w io.Writer, path string, offset int64) int64 {
	file, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return offset
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	reader := bufio.NewReader(file)
	n, _ := io.Copy(w, reader)
	return offset + n
}
