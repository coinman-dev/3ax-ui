package axlegacy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const operatorNdppd = `route-ttl 30000

proxy eth9 {
    rule 2001:db8:ffff::/64 {
        static
    }
}

# --- BEGIN AWG ---
proxy eth0 {
    router yes
    rule 2001:db8::/112 {
        iface awg0
    }
}
# --- END AWG ---
`

type fakeHost struct {
	dir      string
	commands []string
}

func setupHost(t *testing.T, ifaces []database.AxLegacyInterface, ndppd string) *fakeHost {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	h := &fakeHost{dir: t.TempDir()}
	for _, d := range []string{"amneziawg", "wireguard"} {
		if err := os.MkdirAll(filepath.Join(h.dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	awgConfDir, wgConfDir = filepath.Join(h.dir, "amneziawg"), filepath.Join(h.dir, "wireguard")
	ndppdConf = filepath.Join(h.dir, "ndppd.conf")
	if ndppd != "" {
		if err := os.WriteFile(ndppdConf, []byte(ndppd), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runHost = func(name string, args ...string) ([]byte, error) {
		h.commands = append(h.commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	if ifaces != nil {
		raw, _ := json.Marshal(ifaces)
		if err := database.GetDB().Create(&model.Setting{Key: database.AxLegacyHostCleanupKey, Value: string(raw)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestRetireTakesThe1xTunnelsDownOnce(t *testing.T) {
	h := setupHost(t, []database.AxLegacyInterface{{Kind: "awg", Name: "awg0"}, {Kind: "wg", Name: "wg0"}}, operatorNdppd)
	awgConf := filepath.Join(h.dir, "amneziawg", "awg0.conf")
	if err := os.WriteFile(awgConf, []byte("[Interface]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	RetireHost()

	want := []string{
		"awg-quick down " + awgConf,
		"ip link del wg0",
		"systemctl restart ndppd",
	}
	if !slices.Equal(h.commands, want) {
		t.Errorf("commands = %q, want %q", h.commands, want)
	}
	if _, err := os.Stat(awgConf); !os.IsNotExist(err) {
		t.Errorf("awg0.conf still in place, so a reboot would bring the 1.x tunnel back")
	}
	if _, err := os.Stat(awgConf + ".3ax-1x-bak"); err != nil {
		t.Errorf("awg0.conf not kept aside: %v", err)
	}
	ndppd, _ := os.ReadFile(ndppdConf)
	if strings.Contains(string(ndppd), "BEGIN AWG") || !strings.Contains(string(ndppd), "proxy eth9") {
		t.Errorf("ndppd.conf after cleanup:\n%s", ndppd)
	}
	var left int64
	database.GetDB().Model(&model.Setting{}).Where("key = ?", database.AxLegacyHostCleanupKey).Count(&left)
	if left != 0 {
		t.Errorf("cleanup setting kept, so every start would repeat it")
	}

	h.commands = nil
	RetireHost()
	if len(h.commands) != 0 {
		t.Errorf("second start ran %q", h.commands)
	}
}

// The sandboxed unit can write ndppd.conf (a bind mount) but not unlink it from
// read-only /etc, so the file is emptied in place; a read-only dir stands in.
func TestRetireStopsNdppdWhenOnlyOurBlocksWereThere(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory standing in for the sandbox")
	}
	h := setupHost(t, []database.AxLegacyInterface{{Kind: "awg", Name: "awg0"}},
		"route-ttl 30000\n\n# --- BEGIN AWG ---\nproxy eth0 {\n}\n# --- END AWG ---\n")
	readOnlyEtc := filepath.Join(h.dir, "etc")
	if err := os.Mkdir(readOnlyEtc, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ndppdConf, filepath.Join(readOnlyEtc, "ndppd.conf")); err != nil {
		t.Fatal(err)
	}
	ndppdConf = filepath.Join(readOnlyEtc, "ndppd.conf")
	if err := os.Chmod(readOnlyEtc, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyEtc, 0o700) })
	RetireHost()
	if !slices.Contains(h.commands, "systemctl stop ndppd") {
		t.Errorf("ndppd not stopped: %q", h.commands)
	}
	if left, err := os.ReadFile(ndppdConf); err != nil || len(left) != 0 {
		t.Errorf("ndppd.conf = %q, %v; want it emptied", left, err)
	}
}

func TestRetireRefusesAnInterfaceNameThatIsAPath(t *testing.T) {
	h := setupHost(t, []database.AxLegacyInterface{{Kind: "awg", Name: "../../etc/passwd"}}, "")
	RetireHost()
	if len(h.commands) != 0 {
		t.Errorf("ran %q for a name that is not an interface", h.commands)
	}
}

func TestRetireWithoutA1xDatabaseDoesNothing(t *testing.T) {
	h := setupHost(t, nil, operatorNdppd)
	RetireHost()
	if len(h.commands) != 0 {
		t.Errorf("ran %q on a panel that never was 1.x", h.commands)
	}
	if ndppd, _ := os.ReadFile(ndppdConf); string(ndppd) != operatorNdppd {
		t.Errorf("touched ndppd.conf:\n%s", ndppd)
	}
}

// 1.x could apply a tunnel's PostUp twice; "down" deletes one copy of each rule, and the
// survivors kept DNATing a forwarded port to whichever client later got that address.
func TestRetireRemovesEveryCopyOfThe1xRules(t *testing.T) {
	h := setupHost(t, []database.AxLegacyInterface{{Kind: "awg", Name: "awg0"}}, "")
	rules := []string{
		"iptables -t nat -D POSTROUTING -s 10.66.66.0/24 -o eth0 -j MASQUERADE",
		"iptables -t nat -D PREROUTING -p tcp -i eth0 --dport 37015 -m comment --comment 3ax-fwd-87e0 -j DNAT --to-destination 10.66.66.3:37015",
		"ip6tables -D FORWARD -i awg0 -j ACCEPT",
	}
	awgConf := filepath.Join(h.dir, "amneziawg", "awg0.conf")
	conf := "[Interface]\nPostDown = " + strings.Join(append(slices.Clone(rules), "ip -6 neigh del proxy 2001:db8::2 dev eth1"), "; ") + "\n"
	if err := os.WriteFile(awgConf, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	copies := map[string]int{}
	for _, r := range rules {
		copies[r] = 2
	}
	runHost = func(name string, args ...string) ([]byte, error) {
		cmd := name + " " + strings.Join(args, " ")
		h.commands = append(h.commands, cmd)
		if name == "awg-quick" { // its PostDown deletes one copy of each
			for _, r := range rules {
				copies[r]--
			}
			return nil, nil
		}
		if _, isRule := copies[cmd]; isRule {
			if copies[cmd] == 0 {
				return []byte("Bad rule (does a matching rule exist in that chain?)"), errors.New("exit status 1")
			}
			copies[cmd]--
		}
		return nil, nil
	}

	RetireHost()

	for _, r := range rules {
		if copies[r] != 0 {
			t.Errorf("%d copies left of: %s", copies[r], r)
		}
	}
	if slices.Contains(h.commands, "ip -6 neigh del proxy 2001:db8::2 dev eth1") {
		t.Errorf("re-ran a PostDown command that is not a firewall rule: %q", h.commands)
	}
}
