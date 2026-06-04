package mcpauth

import (
	"fmt"
	"os/exec"
	"runtime"
)

// openURL invokes the platform's default URL handler to show u in the user's
// browser. Returns an error if the spawn fails. The command is not awaited.
func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("open %s: %w", u, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
