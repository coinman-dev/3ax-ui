package tunnel

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/shared/crypto"
)

// PeerStatus holds runtime stats for one peer parsed from `<tool> show`.
type PeerStatus struct {
	PublicKey           string `json:"publicKey"`
	Endpoint            string `json:"endpoint"`
	LatestHandshake     int64  `json:"latestHandshake"` // unix timestamp
	TransferRx          int64  `json:"transferRx"`      // bytes received
	TransferTx          int64  `json:"transferTx"`      // bytes transmitted
	PersistentKeepalive int    `json:"persistentKeepalive"`
}

// ConfigPath returns the on-disk path of an interface's config file.
func ConfigPath(k Kind, interfaceName string) string {
	return filepath.Join(k.ConfigDir, interfaceName+".conf")
}

// WriteServerConfig writes the config to <ConfigDir>/<interface>.conf. The file
// holds the server private key, hence 0600 in a 0700 directory.
func WriteServerConfig(k Kind, interfaceName string, config string) error {
	if err := os.MkdirAll(k.ConfigDir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(ConfigPath(k, interfaceName), []byte(config), 0600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// RemoveServerConfig deletes an interface's config file.
func RemoveServerConfig(k Kind, interfaceName string) {
	if err := os.Remove(ConfigPath(k, interfaceName)); err != nil && !os.IsNotExist(err) {
		logger.Warningf("Failed to remove %s config file: %v", k.Title, err)
	}
}

// InterfaceUp brings the interface up via <quick> up.
func InterfaceUp(k Kind, interfaceName string) error {
	output, err := exec.Command(k.Quick, "up", ConfigPath(k, interfaceName)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s up failed: %s: %w", k.Quick, string(output), err)
	}
	logger.Info(k.Title, "interface", interfaceName, "is up")
	return nil
}

// InterfaceDown takes the interface down.
func InterfaceDown(k Kind, interfaceName string) error {
	output, err := exec.Command(k.Quick, "down", ConfigPath(k, interfaceName)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s down failed: %s: %w", k.Quick, string(output), err)
	}
	logger.Info(k.Title, "interface", interfaceName, "is down")
	return nil
}

// SyncConfig applies config changes without dropping existing connections.
// Note for AmneziaWG: syncconf does not re-apply obfuscation parameters to a
// live interface — a change there needs RestartInterface instead.
func SyncConfig(k Kind, interfaceName string) error {
	configPath := ConfigPath(k, interfaceName)

	// Strip the config down to the interface section syncconf accepts.
	stripped, err := exec.Command(k.Quick, "strip", configPath).Output()
	if err != nil {
		logger.Warningf("%s strip failed, restarting interface: %v", k.Quick, err)
		return RestartInterface(k, interfaceName)
	}

	syncCmd := exec.Command(k.Tool, "syncconf", interfaceName, "/dev/stdin")
	syncCmd.Stdin = strings.NewReader(string(stripped))
	output, err := syncCmd.CombinedOutput()
	if err != nil {
		logger.Warningf("%s syncconf failed, restarting interface: %s: %v", k.Tool, string(output), err)
		return RestartInterface(k, interfaceName)
	}
	return nil
}

// RestartInterface performs a full down+up cycle.
func RestartInterface(k Kind, interfaceName string) error {
	// Down may fail because the interface is not up — that is not an error here.
	_ = InterfaceDown(k, interfaceName)
	time.Sleep(500 * time.Millisecond)
	return InterfaceUp(k, interfaceName)
}

// IsInterfaceUp reports whether the interface exists in the system.
func IsInterfaceUp(k Kind, interfaceName string) bool {
	return exec.Command(k.Tool, "show", interfaceName).Run() == nil
}

// GetPeerStats parses `<tool> show <iface> dump` into per-peer traffic stats.
// Tab-separated; the first line describes the interface (and contains its
// private key, so it is skipped), the rest are peers:
// public-key preshared-key endpoint allowed-ips latest-handshake rx tx keepalive
func GetPeerStats(k Kind, interfaceName string) ([]PeerStatus, error) {
	output, err := exec.Command(k.Tool, "show", interfaceName, "dump").Output()
	if err != nil {
		return nil, fmt.Errorf("%s show dump failed: %w", k.Tool, err)
	}

	var peers []PeerStatus
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	if scanner.Scan() {
		// interface line — skipped
	}
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) < 8 {
			continue
		}
		handshake, _ := strconv.ParseInt(fields[4], 10, 64)
		rx, _ := strconv.ParseInt(fields[5], 10, 64)
		tx, _ := strconv.ParseInt(fields[6], 10, 64)
		keepalive, _ := strconv.Atoi(fields[7])

		peers = append(peers, PeerStatus{
			PublicKey:           fields[0],
			Endpoint:            fields[2],
			LatestHandshake:     handshake,
			TransferRx:          rx,
			TransferTx:          tx,
			PersistentKeepalive: keepalive,
		})
	}
	return peers, nil
}

// IsInstalled reports whether both CLI tools of this flavour are available.
func IsInstalled(k Kind) bool {
	_, err1 := exec.LookPath(k.Tool)
	_, err2 := exec.LookPath(k.Quick)
	return err1 == nil && err2 == nil
}

// Version returns a version string for display.
//
// History worth keeping: amneziawg-tools used to report the inherited
// wireguard-tools version ("v1.0.20210914") and never bump it (upstream issue
// #21), so the panel derived the generation from the kernel module's build date
// and printed "v2.0.<date>". Upstream has since fixed the tools — they now
// print their own version, e.g. "amneziawg-tools v3.1.20260812" — and the old
// workaround started lying the other way, showing "v2.0.…" for a 3.1 install.
// So: trust the tools when their banner belongs to this flavour, and fall back
// to the kernel module version otherwise.
func Version(k Kind) string {
	if v := toolVersion(k); v != "" {
		return v
	}
	if v := ModuleVersion(k); v != "" {
		return withVPrefix(v)
	}
	return "unknown"
}

// toolVersion reads `<tool> --version` and extracts the version token, or
// returns "" when the banner is the legacy cosmetic one.
func toolVersion(k Kind) string {
	out, err := exec.Command(k.Tool, "--version").Output()
	if err != nil {
		return ""
	}
	banner := strings.TrimSpace(string(out))
	// For AmneziaWG a wireguard-tools banner means the pre-fix cosmetic version.
	if k.Obfuscation && strings.Contains(strings.ToLower(banner), "wireguard-tools") {
		return ""
	}
	return versionToken(banner)
}

// versionToken picks the "vX.Y.Z" field out of a tools banner such as
// "amneziawg-tools v3.1.20260812 - https://amnezia.org".
func versionToken(banner string) string {
	for _, field := range strings.Fields(banner) {
		if len(field) > 1 && (field[0] == 'v' || field[0] == 'V') && field[1] >= '0' && field[1] <= '9' {
			return field
		}
	}
	return ""
}

// SupportsV3 reports whether this host can run AmneziaWG 3.0 — header
// protection and the randomised protocol timers.
//
// It asks the kernel instead of reading version numbers, because version
// numbers lied. This used to accept either half reporting 3.x, on the reasoning
// that the module and the tools are packaged together. In August 2026 they were
// not: the tools called themselves 3.1 while the module was still 3.0, the
// panel believed the tools, wrote the 3.0 keys into the config, and the module
// refused the whole set with a bare EINVAL. What the operator saw was
// «awg setconf: Unable to modify interface: Invalid argument», a tunnel that
// would not come up, and a log naming nothing.
//
// The module is the only thing that can answer the question, and the cheapest
// way to ask is to try: build a throwaway interface, hand it the keys, see what
// it says. Cached, because the answer cannot change without the module being
// reloaded, which does not happen under a running panel.
func SupportsV3(k Kind) bool {
	if !k.Obfuscation {
		return false
	}
	v3Mu.Lock()
	defer v3Mu.Unlock()
	if known, ok := v3Cache[k.Name]; ok {
		return known
	}
	ok := probeV3(k)
	v3Cache[k.Name] = ok
	if ok {
		logger.Infof("%s: the kernel accepts the 3.0 parameters", k.Title)
	} else {
		logger.Infof("%s: the kernel does not accept the 3.0 parameters, keeping to 2.0", k.Title)
	}
	return ok
}

var (
	v3Mu    sync.Mutex
	v3Cache = map[string]bool{}
)

// v3ProbeIface is the scratch interface the probe builds. The name is ours and
// deliberately unlike anything a person would choose, so a leftover from an
// interrupted probe is recognisable and safe to delete.
const v3ProbeIface = "awgv3probe"

// v3ProbeServer is the parameter set the probe offers the kernel: one of every
// 3.0 shape, over paddings big enough to carry the header-protection nonce.
//
// S1-S4 are not decoration. With header protection on, the module refuses the
// interface unless every padding can hold the nonce — and a probe that leaves
// them at zero trips that rule and reports «no 3.0 support» from a kernel that
// supports it perfectly well. This function got that wrong once already.
func v3ProbeServer() *Server {
	pad := headerProtectionNonceSize + 8
	return &Server{
		// S1+56 must not equal S2, or the two handshake packets come out the
		// same size and the kernel says so.
		S1: pad, S2: pad + 10, S3: pad, S4: pad,
		HeaderProtectionKey:    GenerateHeaderProtectionKey(),
		ContentPaddingAddition: "8-26",
		RekeyAfterTime:         "106-131",
		RekeyTimeout:           "6-8",
		RejectAfterTime:        "174-199",
		KeepaliveTimeout:       "9-12",
		MaxHandshakeAttempts:   "14-20",
		RandomTrailers:         true,
	}
}

// probeV3 creates a throwaway interface and offers the kernel one of each 3.0
// parameter. A module that takes them will run the real config; one that
// refuses them here would have refused the real config too, with the difference
// that here nothing is down.
func probeV3(k Kind) bool {
	key, _, err := crypto.GenerateKeyPair()
	if err != nil {
		return false
	}

	// A previous probe that was killed between add and delete would leave the
	// interface behind and the add below would fail on the name.
	_, _ = runCmd("ip", "link", "del", "dev", v3ProbeIface)
	if out, err := runCmd("ip", "link", "add", v3ProbeIface, "type", k.KernelModule); err != nil {
		logger.Debugf("%s: cannot probe for 3.0 support: %v: %s", k.Title, err, strings.TrimSpace(string(out)))
		return false
	}
	defer func() { _, _ = runCmd("ip", "link", "del", "dev", v3ProbeIface) }()

	conf, err := os.CreateTemp("", "awg-v3-probe-*.conf")
	if err != nil {
		return false
	}
	defer os.Remove(conf.Name())

	probe := v3ProbeServer()
	var body strings.Builder
	fmt.Fprintf(&body, "[Interface]\nPrivateKey = %s\n", key)
	fmt.Fprintf(&body, "S1 = %d\nS2 = %d\nS3 = %d\nS4 = %d\n", probe.S1, probe.S2, probe.S3, probe.S4)
	// The same writer that produces the real config, so the question asked here
	// is exactly the one that will be asked for real.
	writeObfuscation30(&body, probe)
	if _, err := conf.WriteString(body.String()); err != nil {
		conf.Close()
		return false
	}
	conf.Close()

	out, err := runCmd(k.Tool, "setconf", v3ProbeIface, conf.Name())
	if err != nil {
		logger.Debugf("%s: the kernel refused the 3.0 parameters: %s", k.Title, strings.TrimSpace(string(out)))
		return false
	}
	return true
}

// majorVersion pulls the leading number out of "v3.1.20260812" / "3.1.20260812",
// returning 0 when there is nothing to read.
func majorVersion(v string) int {
	v = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(v), "v"), "V")
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}

// ModuleVersion returns the kernel module version (e.g. "3.1.20260812"),
// preferring sysfs and falling back to modinfo.
func ModuleVersion(k Kind) string {
	if b, err := os.ReadFile("/sys/module/" + k.KernelModule + "/version"); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	if out, err := exec.Command("modinfo", "-F", "version", k.KernelModule).Output(); err == nil {
		return strings.TrimSpace(string(out))
	}
	return ""
}

// withVPrefix prefixes a version string with "v" for display, unless it already
// has one or is empty.
func withVPrefix(v string) string {
	if v == "" || v[0] == 'v' || v[0] == 'V' {
		return v
	}
	return "v" + v
}
