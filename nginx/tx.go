package nginx

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/coinman-dev/3ax-ui/v2/logger"
)

// fileTx records what a file looked like before it was touched so that a
// half-applied config can be put back. nginx sits in front of every protocol
// once it is enabled, so "the new config was refused" must never be able to
// turn into "and the old one is gone too".
type fileTx struct {
	saved    map[string]snapshot
	order    []string
	finished bool
}

type snapshot struct {
	existed bool
	data    []byte
	mode    os.FileMode
}

func newTx() *fileTx {
	return &fileTx{saved: map[string]snapshot{}}
}

// keep snapshots a file once; later writes to the same path reuse the first
// snapshot, which is the state we have to return to.
func (t *fileTx) keep(path string) error {
	if _, ok := t.saved[path]; ok {
		return nil
	}
	info, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		t.saved[path] = snapshot{}
	case err != nil:
		return fmt.Errorf("stat %s: %w", path, err)
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		t.saved[path] = snapshot{existed: true, data: data, mode: info.Mode().Perm()}
	}
	t.order = append(t.order, path)
	return nil
}

func (t *fileTx) write(path, content string, mode os.FileMode) error {
	if err := t.keep(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func (t *fileTx) remove(path string) error {
	if err := t.keep(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// rollback puts every touched file back the way it was, newest first.
func (t *fileTx) rollback() {
	if t.finished {
		return
	}
	t.finished = true
	for i := len(t.order) - 1; i >= 0; i-- {
		path := t.order[i]
		s := t.saved[path]
		var err error
		if s.existed {
			err = os.WriteFile(path, s.data, s.mode)
		} else if err = os.Remove(path); os.IsNotExist(err) {
			err = nil
		}
		if err != nil {
			// Nothing useful is left to do, but an operator reading the log
			// needs to know which file to fix by hand.
			logger.Errorf("could not restore %s: %v", path, err)
		}
	}
}

func (t *fileTx) commit() { t.finished = true }

// done is deferred by the caller: any path out of the function that did not
// commit is a failure, and rolls back.
func (t *fileTx) done() { t.rollback() }
