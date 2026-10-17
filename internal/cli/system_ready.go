package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/launchd"
)

// launchctl success means the job was submitted, not that its control server
// is ready. Bound both startup retries and individual requests so a daemon
// that accepts connections without replying cannot make start hang.
func waitForDaemon(ctx context.Context, socketPath string) error {
	var lastErr error
	for {
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		_, lastErr = control.Call(probeCtx, socketPath, control.Request{Command: "doctor"})
		cancel()
		if lastErr == nil {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%w (last control request: %v)", ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
}

func confirmDaemonReady(cfg Config) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForDaemon(ctx, cfg.SocketPath); err != nil {
		fmt.Fprintf(cfg.Stderr, "launchctl succeeded, but the daemon did not become responsive within 5s: %v\n", err)
		fmt.Fprintf(cfg.Stderr, "Inspect the daemon error log: %s/daemon.err.log\n", cfg.LogDir)
		fmt.Fprintln(cfg.Stderr, "Run: lewp logs --lines 50, then: lewp doctor")
		if plan, err := launchd.Plan("print", launchd.Config{Label: launchd.DefaultLabel}); err == nil {
			diagnosticCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if out, err := cfg.RunCommandOutput(diagnosticCtx, plan); err == nil && strings.TrimSpace(out) != "" {
				fmt.Fprintf(cfg.Stderr, "$ %s\n%s\n", strings.Join(plan, " "), strings.TrimRight(out, "\n"))
			}
		}
		return 1
	}
	fmt.Fprintln(cfg.Stdout, "lewp daemon is responding on the control socket")
	return 0
}
