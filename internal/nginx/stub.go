package nginx

import (
	"fmt"
	"os"
	"path/filepath"
)

// IndexFile is the page nginx serves for the site's root.
const IndexFile = "index.html"

// StubPath is where the active cover page is written.
func StubPath() string { return filepath.Join(WebRoot, IndexFile) }

// WriteStub puts the active cover page where nginx serves it, only when it differs: reconcile
// calls this every 30s and a rewrite would reset the mtime for no reason.
func WriteStub(html string) error {
	if err := os.MkdirAll(WebRoot, 0o755); err != nil {
		return fmt.Errorf("create web root: %w", err)
	}
	if current, err := os.ReadFile(StubPath()); err != nil || string(current) != html {
		if err := os.WriteFile(StubPath(), []byte(html), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", StubPath(), err)
		}
	}
	// The unit's UMask=0077 strips the modes above, and nginx's workers are not root.
	if err := os.Chmod(WebRoot, 0o755); err != nil {
		return fmt.Errorf("open up %s: %w", WebRoot, err)
	}
	if err := os.Chmod(StubPath(), 0o644); err != nil {
		return fmt.Errorf("open up %s: %w", StubPath(), err)
	}
	return nil
}

// StubOnDisk returns the page currently on disk, or "" when there is none.
func StubOnDisk() string {
	body, err := os.ReadFile(StubPath())
	if err != nil {
		return ""
	}
	return string(body)
}
