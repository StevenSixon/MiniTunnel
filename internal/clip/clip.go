// Package clip provides OS clipboard access and the bidirectional sync loop
// used by the clipboard-sync feature. The agent serves it as a tiny local TCP
// service (just another allowlisted port, like sshd or Screen Sharing); the
// client reaches it through the normal tunnel. The relay is not involved
// beyond piping bytes, so it needs no changes.
//
// Clipboard access shells out to the platform tools (pbpaste/pbcopy on macOS,
// wl-clipboard or xclip on Linux) rather than linking a GUI toolkit — keeping
// the repo pure Go with no new dependencies. Text only.
package clip

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Read returns the current clipboard contents as text.
func Read() (string, error) {
	cmd, err := pasteCmd()
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", cmd.Path, err)
	}
	return string(out), nil
}

// Write replaces the clipboard contents with text.
func Write(text string) error {
	cmd, err := copyCmd()
	if err != nil {
		return err
	}
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", cmd.Path, err)
	}
	return nil
}

func pasteCmd() (*exec.Cmd, error) { return toolCmd(true) }
func copyCmd() (*exec.Cmd, error)  { return toolCmd(false) }

// toolCmd picks the platform clipboard tool. On macOS, when running as root
// (the agent LaunchDaemon), pbcopy/pbpaste would operate on root's pasteboard,
// not the logged-in user's — so the command is re-targeted into the console
// user's session via launchctl asuser + sudo -u. Without this the sync would
// "work" in a terminal test and silently do nothing in the real deployment.
func toolCmd(paste bool) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		tool := "pbcopy"
		if paste {
			tool = "pbpaste"
		}
		if os.Geteuid() == 0 {
			if uid, ok := consoleUID(); ok && uid != 0 {
				u := strconv.Itoa(uid)
				return exec.Command("launchctl", "asuser", u, "sudo", "-u", "#"+u, tool), nil
			}
		}
		return exec.Command(tool), nil
	case "linux":
		// Wayland first, then X11. -n on wl-paste drops the trailing newline it
		// would otherwise append.
		if _, err := exec.LookPath("wl-paste"); err == nil {
			if paste {
				return exec.Command("wl-paste", "-n"), nil
			}
			return exec.Command("wl-copy"), nil
		}
		if _, err := exec.LookPath("xclip"); err == nil {
			if paste {
				return exec.Command("xclip", "-selection", "clipboard", "-o"), nil
			}
			return exec.Command("xclip", "-selection", "clipboard"), nil
		}
		return nil, fmt.Errorf("no clipboard tool found (install wl-clipboard or xclip)")
	}
	return nil, fmt.Errorf("clipboard sync not supported on %s", runtime.GOOS)
}
