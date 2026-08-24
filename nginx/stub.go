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

// WriteStub puts the active cover page where nginx serves it from.
//
// It is written only when the content differs: the reconcile job calls this
// every half minute, and rewriting an unchanged file would reset its mtime and
// make every backup and every "what changed here" look busy for no reason.
func WriteStub(html string) error {
	if err := os.MkdirAll(WebRoot, 0755); err != nil {
		return fmt.Errorf("create web root: %w", err)
	}
	if current, err := os.ReadFile(StubPath()); err == nil && string(current) == html {
		return nil
	}
	if err := os.WriteFile(StubPath(), []byte(html), 0644); err != nil {
		return fmt.Errorf("write %s: %w", StubPath(), err)
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

// RemoveStub deletes the page. Used when no site is active any more; nginx
// then has nothing to serve for the root, which is the honest outcome — better
// than leaving a page the panel no longer knows about.
func RemoveStub() error {
	if err := os.Remove(StubPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", StubPath(), err)
	}
	return nil
}
