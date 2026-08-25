package nginx

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/logger"
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
	// streamDir holds the stream-level config. It is ours alone: a distro's
	// nginx.conf includes conf.d from inside http {}, where a stream block is
	// a syntax error.
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
	out, err := exec.Command(Binary, "-v").CombinedOutput()
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

// HasStream reports whether this nginx can multiplex a TCP port by SNI.
// Without the stream module the whole feature is impossible, so the panel asks
// before offering a mode the server cannot deliver.
//
// Two traps here, both of which quietly answer "yes" to a server that would
// reject the config. The flag has to be matched as a whole word — the banner
// also carries --with-stream_ssl_module and friends on builds that have no
// stream module at all. And --with-stream=dynamic means the directive exists
// only once the distro's separate module package is installed and wired in;
// Debian and Ubuntu ship nginx that way, with modules-enabled empty.
func HasStream() bool {
	out, err := exec.Command(Binary, "-V").CombinedOutput()
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

// FreeLoopbackPort returns a loopback port for the http backend.
//
// This must not be a constant. The obvious choice, 8080, is one of the most
// commonly occupied ports on any server — and the failure it produces is the
// worst kind: nginx cannot bind, logs it, keeps running on the old config, and
// the stream block happily forwards our own domain to whatever *is* on 8080.
// The site then answers as somebody else's service instead of not answering.
//
// The range is above the well-known ports and below the ephemeral range, so a
// port picked here is not going to be taken later by an outgoing connection.
//
// taken is the set of ports the caller has already handed out but not yet
// bound, and it is not optional bookkeeping. Nothing this returns is listening
// until nginx reloads, so two calls in a row would otherwise answer with the
// same number — and a config where the site's TLS backend and an inbound's
// relay share a port sails through `nginx -t`, which binds nothing, and then
// fails to start.
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
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// accepting reports whether something answers on a loopback port. Used to check
// that a reload actually took: nginx -s reload returns success the moment the
// signal is sent, long before the new config has managed to bind anything.
func accepting(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// WaitPortReleased waits for a port to stop accepting connections.
//
// A graceful reload does not free a listening socket the moment it returns:
// the old workers keep it until they have finished their connections. Handing
// the port to another process before that produces "address already in use"
// from a process that had every right to expect it free.
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
		return exec.Command("systemctl", "is-active", "--quiet", "nginx").Run() == nil
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

// Test runs `nginx -t`. The returned error carries nginx's own output, which
// names the file and line — the panel shows it verbatim rather than a summary,
// because that message is the only useful thing when a config is refused.
func Test() error {
	// -c is the default path in production; naming it explicitly is what lets
	// the tests point nginx at a temporary tree.
	out, err := exec.Command(Binary, "-t", "-c", MainConfPath()).CombinedOutput()
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
		if err := exec.Command("systemctl", "reload", "nginx").Run(); err == nil {
			return nil
		}
		// systemd knows nginx but could not reload it — perhaps it is running
		// outside the unit. Signalling the master directly still works.
	}
	if out, err := exec.Command(Binary, "-s", "reload").CombinedOutput(); err != nil {
		return fmt.Errorf("reload nginx: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func start() error {
	if hasSystemd() {
		if out, err := exec.Command("systemctl", "start", "nginx").CombinedOutput(); err != nil {
			return fmt.Errorf("start nginx: %s: %w", strings.TrimSpace(string(out)), err)
		}
		// A server that reboots without nginx would come back with 443 shut
		// and everything behind it unreachable.
		_ = exec.Command("systemctl", "enable", "nginx").Run()
		return nil
	}
	if out, err := exec.Command(Binary).CombinedOutput(); err != nil {
		return fmt.Errorf("start nginx: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func hasSystemd() bool {
	_, err := exec.LookPath("systemctl")
	return err == nil
}

// Staged is a config that is written to disk and accepted by `nginx -t`, but
// not yet live.
//
// The two halves are separate because of the order the panel has to work in:
// the inbound that owns the public port has to let go of it before nginx can
// take it, and moving an inbound is the expensive, visible step. Writing and
// verifying the config first means a config nginx would refuse costs nothing,
// and Rollback undoes the whole thing if the move itself goes wrong.
type Staged struct {
	tx  *fileTx
	cfg Config
}

// Stage writes the generated config and verifies it with `nginx -t`. On any
// failure it restores every file it touched and returns nginx's own message,
// which names the file and the line.
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
	} else if err := staged.tx.write(StreamConfPath(), streamConf, 0644); err != nil {
		return fail(err)
	}
	if httpConf == "" {
		if err := staged.tx.remove(HTTPConfPath()); err != nil {
			return fail(err)
		}
	} else if err := staged.tx.write(HTTPConfPath(), httpConf, 0644); err != nil {
		return fail(err)
	}
	if patched != string(main) {
		if err := staged.tx.write(MainConfPath(), patched, 0644); err != nil {
			return fail(err)
		}
	}
	if c.Site != nil {
		if err := os.MkdirAll(c.Site.Root, 0755); err != nil {
			return fail(fmt.Errorf("create web root: %w", err))
		}
	}

	if err := Test(); err != nil {
		return fail(err)
	}
	return staged, nil
}

// Activate reloads nginx onto the staged config and checks that the reload
// actually took, putting the old files back if it did not.
//
// The check is not paranoia. `nginx -s reload` returns success as soon as the
// signal is sent; a new worker that cannot bind its port logs an emergency and
// dies, and the old workers carry on serving the previous config. Without
// looking at the ports afterwards, the panel would report success over a
// front-end that never came up.
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

	// Every relay too. If the reload failed to bind one of these, nginx kept
	// the old workers — which are still holding the public port, so checking
	// that alone would report success over a config that never came up.
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

// NeedsUpdate reports whether what is on disk differs from what c renders to.
// The reconcile job runs every half minute; without this it would reload nginx
// every time, dropping the connection counters and churning the logs for
// nothing.
func NeedsUpdate(c Config) (bool, error) {
	streamConf, err := c.StreamConf()
	if err != nil {
		return false, err
	}
	httpConf, err := c.HTTPConf()
	if err != nil {
		return false, err
	}

	// The include in nginx.conf counts as much as the files it points at. A
	// package upgrade that replaces nginx.conf, or an operator tidying it,
	// takes our stream block out of the build while both of our files sit
	// there looking correct — the front-end is down and comparing only the
	// files would report nothing to do, for ever.
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

// Remove takes the panel's config back out and reloads, leaving the rest of
// nginx untouched. Used when the mode goes back to off; a server without nginx
// has nothing to undo.
func Remove() error {
	if !IsInstalled() {
		return nil
	}
	return Apply(Config{Mode: ModeOff})
}

// withStreamInclude returns nginx.conf with our marked include block present
// exactly once.
//
// A stream block can only appear at the top level and only once, so if the
// operator (or another panel) already has one, adding a second is a syntax
// error. We say so instead of guessing where inside theirs the include should
// go — editing someone else's block is how a working server gets broken.
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

// hasForeignStreamBlock reports whether nginx.conf declares a top-level stream
// block that is not ours. Comments are stripped and brace depth is tracked so
// that the word "stream" inside http {} or in a comment does not count.
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
