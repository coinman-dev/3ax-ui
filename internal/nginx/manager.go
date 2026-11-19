package nginx

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Paths are variables rather than constants so the tests can point the whole
// package at a temporary tree.
var (
	// ConfRoot is nginx's configuration directory.
	ConfRoot = "/etc/nginx"
	// WebRoot is where the stub site is written for nginx to serve.
	WebRoot = "/usr/local/x-ui/www"
	// Binary is the nginx executable; looked up on PATH when left as is.
	Binary = "nginx"
)

const (
	// streamDir is ours alone: a distro includes conf.d inside http {}, where a stream block
	// is a syntax error.
	streamDir = "stream-enabled"
	confName  = "3ax-ui.conf"

	includeBegin = "# --- BEGIN 3AX-UI STREAM ---"
	includeEnd   = "# --- END 3AX-UI STREAM ---"
)

var includeRe = regexp.MustCompile(`(?s)\n?` + regexp.QuoteMeta(includeBegin) + `.*?` + regexp.QuoteMeta(includeEnd) + `\n?`)

// MainConfPath is nginx's top-level configuration file.
func MainConfPath() string { return filepath.Join(ConfRoot, "nginx.conf") }

// HTTPConfPath is our http-level file, in whichever directory this distro
// includes from inside http {} — conf.d on Debian and RHEL, http.d on Alpine.
func HTTPConfPath() string {
	for _, dir := range []string{"http.d", "conf.d"} {
		if st, err := os.Stat(filepath.Join(ConfRoot, dir)); err == nil && st.IsDir() {
			return filepath.Join(ConfRoot, dir, confName)
		}
	}
	return filepath.Join(ConfRoot, "conf.d", confName)
}

// StreamConfPath is our stream-level file.
func StreamConfPath() string { return filepath.Join(ConfRoot, streamDir, confName) }

// IsInstalled reports whether the nginx binary is available.
func IsInstalled() bool {
	_, err := exec.LookPath(Binary)
	return err == nil
}

// Version returns a display string such as "v1.24.0", or "" when nginx is not
// installed. The banner goes to stderr, hence CombinedOutput.
func Version() string {
	out, err := run(Binary, "-v")
	if err != nil {
		return ""
	}
	// "nginx version: nginx/1.24.0", or "nginx/1.24.0 (Ubuntu)" on a distro
	// that stamps its name in — the first field is the version either way.
	_, v, found := strings.Cut(strings.TrimSpace(string(out)), "nginx/")
	if !found {
		return ""
	}
	v, _, _ = strings.Cut(strings.TrimSpace(v), " ")
	if v == "" {
		return ""
	}
	return "v" + v
}

// HasStream reports whether nginx can multiplex by SNI. The flag must match as a whole word
// (not --with-stream_ssl_module), and =dynamic counts only once the module is loaded (Debian).
func HasStream() bool {
	out, err := run(Binary, "-V")
	if err != nil {
		return false
	}
	for _, field := range strings.Fields(string(out)) {
		switch field {
		case "--with-stream":
			return true
		case "--with-stream=dynamic":
			return streamModuleLoaded()
		}
	}
	return false
}

// streamModuleLoaded reports whether a dynamically built stream module is
// actually loaded, which nginx -V does not say.
func streamModuleLoaded() bool {
	if matches, _ := filepath.Glob(filepath.Join(ConfRoot, "modules-enabled", "*stream*.conf")); len(matches) > 0 {
		return true
	}
	// Distros without a modules-enabled directory put load_module directly in
	// the main file.
	main, err := os.ReadFile(MainConfPath())
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(main)) {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "load_module") && strings.Contains(line, "ngx_stream_module") {
			return true
		}
	}
	return false
}

// FreeLoopbackPort picks a free port below the ephemeral range. taken holds ports handed out but
// not yet bound: nothing listens until reload, and `nginx -t` binds nothing so misses a clash.
func FreeLoopbackPort(preferred int, taken map[int]bool) (int, error) {
	if preferred > 0 && !taken[preferred] && portFree(preferred) {
		return preferred, nil
	}
	for port := 8081; port <= 8999; port++ {
		if !taken[port] && portFree(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free loopback port between 8081 and 8999")
}

// portFree reports whether a loopback port can be bound right now.
func portFree(port int) bool {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// accepting reports whether something answers on a loopback port, to confirm a reload took:
// nginx -s reload succeeds once signalled, long before the new config binds anything.
func accepting(port int) bool {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// WaitPortReleased waits for a port to stop accepting: after a graceful reload the old workers
// hold it until their connections finish, so the next owner would get "address already in use".
func WaitPortReleased(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !accepting(port) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return !accepting(port)
}

// IsRunning reports whether nginx is up.
func IsRunning() bool {
	if hasSystemd() {
		return runErr("systemctl", "is-active", "--quiet", "nginx") == nil
	}
	// No systemd: a master process writes its pid file and removes it on exit,
	// so its presence is the cheapest side-effect-free answer.
	for _, p := range []string{"/run/nginx.pid", "/var/run/nginx.pid", "/usr/local/nginx/logs/nginx.pid"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// Test runs `nginx -t`. The error carries nginx's own output verbatim, since its file and line
// are the only useful thing when a config is refused.
func Test() error {
	// -c is the default path in production; naming it explicitly is what lets
	// the tests point nginx at a temporary tree.
	out, err := run(Binary, "-t", "-c", MainConfPath())
	if err != nil {
		return fmt.Errorf("nginx -t: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Reload asks a running nginx to pick up the new config, starting it if it is
// not running yet.
func Reload() error {
	if !IsRunning() {
		return start()
	}
	if hasSystemd() {
		if err := runErr("systemctl", "reload", "nginx"); err == nil {
			return nil
		}
		// systemd knows nginx but could not reload it — perhaps it is running
		// outside the unit. Signalling the master directly still works.
	}
	if out, err := run(Binary, "-s", "reload"); err != nil {
		return fmt.Errorf("reload nginx: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func start() error {
	if hasSystemd() {
		if out, err := run("systemctl", "start", "nginx"); err != nil {
			return fmt.Errorf("start nginx: %s: %w", strings.TrimSpace(string(out)), err)
		}
		// A server that reboots without nginx would come back with 443 shut
		// and everything behind it unreachable.
		_ = runErr("systemctl", "enable", "nginx")
		return nil
	}
	if out, err := run(Binary); err != nil {
		return fmt.Errorf("start nginx: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// commandTimeout bounds every nginx/systemctl call; a hung reload must not wedge the 30s job.
const commandTimeout = 30 * time.Second

func run(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func runErr(name string, args ...string) error {
	_, err := run(name, args...)
	return err
}

func hasSystemd() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// Staged is written and passed `nginx -t` but is not live, so a refused config costs nothing
// before the inbound gives up the public port, and Rollback undoes a failed move.
type Staged struct {
	tx  *fileTx
	cfg Config
}

// Stage writes the generated config and verifies it with `nginx -t`. On failure it restores every
// file it touched and returns nginx's own message.
func Stage(c Config) (*Staged, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !IsInstalled() {
		return nil, fmt.Errorf("nginx is not installed")
	}
	if c.Mode != ModeOff && !HasStream() {
		return nil, fmt.Errorf("this nginx was built without the stream module, so it cannot route port %d by SNI", c.Port)
	}

	streamConf, err := c.StreamConf()
	if err != nil {
		return nil, err
	}
	httpConf, err := c.HTTPConf()
	if err != nil {
		return nil, err
	}

	main, err := os.ReadFile(MainConfPath())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", MainConfPath(), err)
	}
	patched := stripInclude(string(main))
	if c.Mode != ModeOff {
		if patched, err = withStreamInclude(string(main)); err != nil {
			return nil, err
		}
	}

	staged := &Staged{tx: newTx(), cfg: c}
	fail := func(err error) (*Staged, error) {
		staged.tx.rollback()
		return nil, err
	}

	if streamConf == "" {
		if err := staged.tx.remove(StreamConfPath()); err != nil {
			return fail(err)
		}
	} else if err := staged.tx.write(StreamConfPath(), streamConf, 0o644); err != nil {
		return fail(err)
	}
	if httpConf == "" {
		if err := staged.tx.remove(HTTPConfPath()); err != nil {
			return fail(err)
		}
	} else if err := staged.tx.write(HTTPConfPath(), httpConf, 0o644); err != nil {
		return fail(err)
	}
	if patched != string(main) {
		if err := staged.tx.write(MainConfPath(), patched, 0o644); err != nil {
			return fail(err)
		}
	}
	if c.Site != nil {
		if err := os.MkdirAll(c.Site.Root, 0o755); err != nil {
			return fail(fmt.Errorf("create web root: %w", err))
		}
	}

	if err := Test(); err != nil {
		return fail(err)
	}
	return staged, nil
}

// Activate reloads onto the staged config and restores the old files unless the ports answer:
// a reload succeeds once signalled, and a worker that cannot bind dies while the old ones serve.
func (s *Staged) Activate() error {
	if s == nil {
		return nil
	}
	if err := Reload(); err != nil {
		s.Rollback()
		return err
	}
	if err := s.verify(); err != nil {
		s.Rollback()
		return err
	}
	s.tx.commit()
	logger.Info("nginx config applied, mode", string(s.cfg.Mode))
	return nil
}

// verify waits briefly for nginx to pick up the new config and confirms it is
// listening where the config says it should be.
func (s *Staged) verify() error {
	if s.cfg.Mode == ModeOff {
		return nil
	}
	want := []struct {
		port int
		what string
	}{{s.cfg.Port, "the public port"}}
	if s.cfg.Site != nil {
		_, portStr, err := net.SplitHostPort(s.cfg.Site.Listen)
		if err == nil {
			if port, err := strconv.Atoi(portStr); err == nil {
				want = append(want, struct {
					port int
					what string
				}{port, "the loopback port for " + s.cfg.Site.Domain})
			}
		}
	}

	// Every relay too: if one failed to bind, the old workers still hold the public port, so
	// checking that alone would report success over a config that never came up.
	for _, r := range s.cfg.Routes {
		if r.Relay == "" {
			continue
		}
		if _, portStr, err := net.SplitHostPort(r.Relay); err == nil {
			if port, err := strconv.Atoi(portStr); err == nil {
				want = append(want, struct {
					port int
					what string
				}{port, "the relay for " + r.Name})
			}
		}
	}

	for _, w := range want {
		ok := false
		// A reload is not instant; give the new worker a moment to bind.
		for range 10 {
			if accepting(w.port) {
				ok = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !ok {
			return fmt.Errorf("nginx did not come up on %s (%d) — most likely something else already has it; see the nginx error log", w.what, w.port)
		}
	}
	return nil
}

// Rollback restores the previous files and puts nginx back on them. Safe to
// call after Activate: a committed transaction ignores it.
func (s *Staged) Rollback() {
	if s == nil {
		return
	}
	if s.tx.finished {
		return
	}
	s.tx.rollback()
	if err := Reload(); err != nil {
		logger.Errorf("nginx was rolled back but would not reload: %v", err)
	}
}

// ConfigInstalled reports whether our stream config is on disk; starting a stopped nginx without
// it would put the distro's default site on port 80 of a server that never asked for one.
func ConfigInstalled() bool {
	_, err := os.Stat(StreamConfPath())
	return err == nil
}

// NeedsUpdate reports whether disk differs from what c renders. Reconcile runs every 30s and
// would otherwise reload nginx each time for nothing.
func NeedsUpdate(c Config) (bool, error) {
	streamConf, err := c.StreamConf()
	if err != nil {
		return false, err
	}
	httpConf, err := c.HTTPConf()
	if err != nil {
		return false, err
	}

	// A package upgrade or operator can drop our include from nginx.conf while our files look
	// correct; comparing only the files would then report nothing to do, for ever.
	if main, err := os.ReadFile(MainConfPath()); err == nil {
		if included := includeRe.MatchString(string(main)); included != (c.Mode != ModeOff) {
			return true, nil
		}
	}
	for _, f := range []struct {
		path string
		want string
	}{
		{StreamConfPath(), streamConf},
		{HTTPConfPath(), httpConf},
	} {
		got, err := os.ReadFile(f.path)
		if os.IsNotExist(err) {
			if f.want != "" {
				return true, nil
			}
			continue
		}
		if err != nil {
			return false, fmt.Errorf("read %s: %w", f.path, err)
		}
		if string(got) != f.want {
			return true, nil
		}
	}
	return false, nil
}

// Apply stages the config and activates it in one step. Use Stage/Activate
// when something else has to happen in between.
func Apply(c Config) error {
	staged, err := Stage(c)
	if err != nil {
		return err
	}
	return staged.Activate()
}

// stripInclude removes our marked block from nginx.conf.
func stripInclude(main string) string {
	return includeRe.ReplaceAllString(main, "\n")
}

// Remove takes the panel's config back out and reloads, leaving the rest of nginx untouched.
// A server without nginx has nothing to undo.
func Remove() error {
	if !IsInstalled() {
		return nil
	}
	return Apply(Config{Mode: ModeOff})
}

// withStreamInclude returns nginx.conf with our include present once. nginx allows one top-level
// stream block, so a foreign one is reported rather than edited, which could break the server.
func withStreamInclude(main string) (string, error) {
	include := fmt.Sprintf("%s\nstream {\n    include %s/*.conf;\n}\n%s\n",
		includeBegin, filepath.Join(ConfRoot, streamDir), includeEnd)

	if includeRe.MatchString(main) {
		return includeRe.ReplaceAllString(main, "\n"+include), nil
	}
	if hasForeignStreamBlock(main) {
		return "", fmt.Errorf("%s already has a stream block; add \"include %s/*.conf;\" inside it and try again",
			MainConfPath(), filepath.Join(ConfRoot, streamDir))
	}
	return strings.TrimRight(main, "\n") + "\n\n" + include, nil
}

// hasForeignStreamBlock reports a top-level stream block that is not ours, tracking brace depth
// and stripping comments so "stream" inside http {} or a comment does not count.
func hasForeignStreamBlock(main string) bool {
	depth := 0
	for _, line := range strings.Split(includeRe.ReplaceAllString(main, "\n"), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		trimmed := strings.TrimSpace(line)
		if depth == 0 && (trimmed == "stream" || strings.HasPrefix(trimmed, "stream ") || strings.HasPrefix(trimmed, "stream{")) {
			return true
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if depth < 0 {
			depth = 0
		}
	}
	return false
}
