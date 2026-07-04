//go:build !darwin

package clip

import "fmt"

// Image clipboard sync is macOS-only for now; other platforms sync text only.
// (wl-clipboard/xclip can carry image/png too — add here if ever needed.)

func ImageSig() (string, bool) { return "", false }

func ReadImage() ([]byte, error) {
	return nil, fmt.Errorf("image clipboard not supported on this platform")
}

func WriteImage([]byte) error {
	return fmt.Errorf("image clipboard not supported on this platform")
}
