package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestReinstallRestartsRunningDaemonAfterInstall(t *testing.T) {
	body, err := os.ReadFile("../../bin/reinstall")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, want := range []string{
		"installed_path=",
		"system status",
		"system restart",
		"waiting for lewp daemon",
		"seq 1 50",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bin/reinstall missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "system restart >/dev/null") {
		t.Fatalf("bin/reinstall should leave restart diagnostics visible:\n%s", script)
	}
}
