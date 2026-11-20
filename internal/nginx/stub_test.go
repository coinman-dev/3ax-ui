package nginx

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The unit runs with UMask=0077; nginx's workers are another user and answered
// 403 for a cover page only root could read.
func TestWriteStubStaysReadableUnderTheUnitUmask(t *testing.T) {
	prevRoot := WebRoot
	WebRoot = filepath.Join(t.TempDir(), "www")
	prevMask := syscall.Umask(0o077)
	t.Cleanup(func() {
		syscall.Umask(prevMask)
		WebRoot = prevRoot
	})
	for _, step := range []string{"first write", "unchanged page on a tree created too tight"} {
		if err := WriteStub("<p>cover</p>"); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		for path, want := range map[string]os.FileMode{WebRoot: 0o755, StubPath(): 0o644} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s: %s is %o, want %o", step, path, got, want)
			}
		}
		_ = os.Chmod(WebRoot, 0o700)
		_ = os.Chmod(StubPath(), 0o600)
	}
}
