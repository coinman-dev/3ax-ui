package mtproto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/config"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
)

// ClientSecret is one active user of an mtproto proxy. Id is the mtg-multi
// [secrets] entry name and the key under which mtg-multi's /stats reports the
// user's traffic; Email is the panel ClientStats key.
type ClientSecret struct {
	Id     string
	Secret string
	Email  string
}

// Instance is the desired runtime configuration of one mtproto inbound.
type Instance struct {
	Id     int
	Tag    string
	Listen string
	Port   int

	// MultiUser selects the backend: true → mtg-multi (a [secrets] table, all
	// Clients served on one port); false → single-secret mtg (only Clients[0]).
	MultiUser bool
	// Clients is the active (enabled, non-expired) client set, in stable order.
	Clients []ClientSecret

	// Optional mtg tuning; each is omitted from the generated TOML when
	// zero-valued so mtg falls back to its own defaults.
	Debug                 bool
	ProxyProtocolListener bool
	PreferIP              string
	Concurrency           int
	PublicIPv4            string
	PublicIPv6            string
	TolerateTimeSkewness  string
	// DNS is mtg's own resolver ("https://1.1.1.1", "tls://1.1.1.1", "1.1.1.1"
	// or "" for the system one). mtg resolves the fronting hostname itself, so
	// a poisoned local resolver is worked around here rather than in /etc.
	DNS string

	// Where a connection that fails the FakeTLS handshake is sent — an active
	// prober, or anyone scanning the port. Without a fronting target mtg just
	// drops the connection, which is itself a recognisable answer; with one the
	// port behaves like the HTTPS server it claims to be.
	//
	// FrontingHost is written as `host`: mtg deprecated `ip`, logs a warning
	// and ignores it. It takes a hostname or a literal address; the SNI still
	// comes from the secret.
	FrontingHost          string
	FrontingPort          int
	FrontingProxyProtocol bool

	// Doppelganger makes the proxy's traffic SHAPE resemble a real site's:
	// mtg crawls these URLs to learn the delay distribution between packets and
	// reproduces it. This is what answers a censor that classifies by timing
	// rather than by signature. Empty URLs leave the subsystem idle.
	DoppelgangerURLs     []string
	DoppelgangerRepeats  int
	DoppelgangerRaidEach string
	DoppelgangerDRS      bool

	// Both defences are ON by default in mtg, so these carry the negative: the
	// config gets `enabled = false` only when the operator turns one off.
	// Blocklist is worth turning off on a LAN — the default FireHOL list
	// contains RFC1918, so a client on the same network is refused.
	AntiReplayDisabled bool
	BlocklistDisabled  bool
	BlocklistURLs      []string

	// When RouteThroughXray is set, mtg dials Telegram through the loopback
	// SOCKS bridge the panel injects into the Xray config at XrayRoutePort, so
	// the egress obeys the core's routing rules instead of going out directly.
	RouteThroughXray bool
	XrayRoutePort    int
}

func (inst Instance) bindTo() string {
	listen := inst.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	return fmt.Sprintf("%s:%d", listen, inst.Port)
}

// fingerprint changes whenever any value that ends up in the generated TOML
// changes, so ensureLocked restarts the proxy when the operator edits a setting
// or adds/removes/enables/disables a client.
func (inst Instance) fingerprint() string {
	parts := []string{
		inst.bindTo(),
		strconv.FormatBool(inst.MultiUser),
		strconv.FormatBool(inst.Debug),
		strconv.FormatBool(inst.ProxyProtocolListener),
		inst.PreferIP,
		strconv.Itoa(inst.Concurrency),
		inst.PublicIPv4,
		inst.PublicIPv6,
		inst.TolerateTimeSkewness,
		inst.DNS,
		inst.FrontingHost,
		strconv.Itoa(inst.FrontingPort),
		strconv.FormatBool(inst.FrontingProxyProtocol),
		strings.Join(inst.DoppelgangerURLs, ","),
		strconv.Itoa(inst.DoppelgangerRepeats),
		inst.DoppelgangerRaidEach,
		strconv.FormatBool(inst.DoppelgangerDRS),
		strconv.FormatBool(inst.AntiReplayDisabled),
		strconv.FormatBool(inst.BlocklistDisabled),
		strings.Join(inst.BlocklistURLs, ","),
		strconv.FormatBool(inst.RouteThroughXray),
		strconv.Itoa(inst.XrayRoutePort),
	}
	for _, c := range inst.activeClients() {
		// Email is part of the fingerprint so a per-client email rename restarts
		// the sidecar and refreshes the cached id→email map used for traffic
		// attribution (otherwise that client's traffic is dropped until a restart).
		parts = append(parts, c.Id+"="+c.Secret+"="+c.Email)
	}
	return strings.Join(parts, "|")
}

// activeClients returns the clients the backend will actually serve: all of them
// for mtg-multi, only the first for single-secret mtg.
func (inst Instance) activeClients() []ClientSecret {
	if inst.MultiUser || len(inst.Clients) <= 1 {
		return inst.Clients
	}
	return inst.Clients[:1]
}

// Traffic is a traffic delta since the previous scrape, attributed to a single
// client. Uuid is the client's stable id (the mtg-multi [secrets]/stats key and
// the mtproto_clients.uuid the panel keys traffic by); Email is the human label.
// LastSeen is the scrape time when the client had at least one live connection
// at scrape time (0 otherwise), used to drive per-client online status.
type Traffic struct {
	Tag      string
	Uuid     string
	Email    string
	Up       int64
	Down     int64
	LastSeen int64
}

type managed struct {
	proc        *Process
	tag         string
	fingerprint string
	statsPort   int
	multiUser   bool
	clients     []ClientSecret
	// Single-secret mode: per-process cumulative counters.
	lastUp   int64
	lastDown int64
	haveLast bool
	// Multi-user mode: per-client cumulative counters, keyed by client Id.
	lastByClient map[string]clientCounter
}

type clientCounter struct {
	up   int64
	down int64
}

// Manager owns the set of running mtg processes keyed by inbound id.
//
// Two locks, deliberately: opMu serialises process lifecycle work (spawning,
// stopping, writing configs, allocating a stats port) so two callers can never
// race over the same inbound, while mu only ever guards in-memory state and is
// never held across exec, disk or network I/O. Keeping them apart means a
// traffic scrape or a reconcile batch no longer blocks behind a starting mtg.
type Manager struct {
	opMu sync.Mutex

	mu    sync.Mutex
	procs map[int]*managed
	// swept records that the one-time startup cleanup of orphaned mtg
	// processes (survivors of a previous x-ui run) has already run.
	swept bool
}

var (
	managerOnce sync.Once
	manager     *Manager
)

// GetManager returns the process-wide mtg manager singleton.
func GetManager() *Manager {
	managerOnce.Do(func() {
		manager = &Manager{procs: map[int]*managed{}}
	})
	return manager
}

// InstanceFromInbound derives a desired Instance from an mtproto inbound and its
// client rows (read from the mtproto_clients table by the caller — the mtproto
// package stays DB-free). It keeps only the clients the backend should currently
// serve (enabled and not past their expiry). Returns false when the inbound is
// not a usable mtproto inbound or has no active clients.
func InstanceFromInbound(ib *model.Inbound, clients []model.MtprotoClient) (Instance, bool) {
	if ib == nil || ib.Protocol != model.MTProto {
		return Instance{}, false
	}
	settings := ib.Settings
	var parsed struct {
		Debug                 bool   `json:"debug"`
		ProxyProtocolListener bool   `json:"proxyProtocolListener"`
		PreferIP              string `json:"preferIp"`
		Concurrency           int    `json:"concurrency"`
		PublicIPv4            string `json:"publicIpv4"`
		PublicIPv6            string `json:"publicIpv6"`
		TolerateTimeSkewness  string `json:"tolerateTimeSkewness"`
		DNS                   string `json:"dns"`
		DomainFronting        struct {
			Host string `json:"host"`
			// Legacy key from before mtg deprecated it; still read so an inbound
			// configured earlier keeps its fronting target after an upgrade.
			IP            string `json:"ip"`
			Port          int    `json:"port"`
			ProxyProtocol bool   `json:"proxyProtocol"`
		} `json:"domainFronting"`
		Doppelganger struct {
			URLs           []string `json:"urls"`
			RepeatsPerRaid int      `json:"repeatsPerRaid"`
			RaidEach       string   `json:"raidEach"`
			DRS            bool     `json:"drs"`
		} `json:"doppelganger"`
		// Pointers: absent means "leave mtg's default (on)", false means the
		// operator turned the defence off.
		AntiReplay struct {
			Enabled *bool `json:"enabled"`
		} `json:"antiReplay"`
		Blocklist struct {
			Enabled *bool    `json:"enabled"`
			URLs    []string `json:"urls"`
		} `json:"blocklist"`
		RouteThroughXray bool `json:"routeThroughXray"`
		RouteXrayPort    int  `json:"routeXrayPort"`
	}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		return Instance{}, false
	}

	// `host` wins; `ip` is only a fallback for inbounds saved before mtg
	// deprecated it, so upgrading does not quietly turn fronting off.
	frontingHost := strings.TrimSpace(parsed.DomainFronting.Host)
	if frontingHost == "" {
		frontingHost = strings.TrimSpace(parsed.DomainFronting.IP)
	}

	nowMs := time.Now().UnixMilli()
	active := make([]ClientSecret, 0, len(clients))
	for _, c := range clients {
		if !c.Enable {
			continue
		}
		if c.ExpiryTime > 0 && c.ExpiryTime <= nowMs {
			continue
		}
		if strings.TrimSpace(c.Uuid) == "" || strings.TrimSpace(c.Secret) == "" {
			continue
		}
		active = append(active, ClientSecret{Id: c.Uuid, Secret: c.Secret, Email: c.Email})
	}
	if len(active) == 0 {
		return Instance{}, false
	}
	return Instance{
		Id:                    ib.Id,
		Tag:                   ib.Tag,
		Listen:                ib.Listen,
		Port:                  ib.Port,
		MultiUser:             MultiUserSupported(),
		Clients:               active,
		Debug:                 parsed.Debug,
		ProxyProtocolListener: parsed.ProxyProtocolListener,
		PreferIP:              parsed.PreferIP,
		Concurrency:           parsed.Concurrency,
		PublicIPv4:            parsed.PublicIPv4,
		PublicIPv6:            parsed.PublicIPv6,
		TolerateTimeSkewness:  parsed.TolerateTimeSkewness,
		DNS:                   parsed.DNS,
		FrontingHost:          frontingHost,
		FrontingPort:          parsed.DomainFronting.Port,
		FrontingProxyProtocol: parsed.DomainFronting.ProxyProtocol,
		DoppelgangerURLs:      nonEmpty(parsed.Doppelganger.URLs),
		DoppelgangerRepeats:   parsed.Doppelganger.RepeatsPerRaid,
		DoppelgangerRaidEach:  parsed.Doppelganger.RaidEach,
		DoppelgangerDRS:       parsed.Doppelganger.DRS,
		AntiReplayDisabled:    parsed.AntiReplay.Enabled != nil && !*parsed.AntiReplay.Enabled,
		BlocklistDisabled:     parsed.Blocklist.Enabled != nil && !*parsed.Blocklist.Enabled,
		BlocklistURLs:         nonEmpty(parsed.Blocklist.URLs),
		RouteThroughXray:      parsed.RouteThroughXray,
		XrayRoutePort:         parsed.RouteXrayPort,
	}, true
}

// nonEmpty drops blank entries from a list the panel may have collected from a
// textarea, where a stray newline is normal.
func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Ensure starts the mtg process for an instance, or restarts it when its
// configuration changed. A no-op when the desired process is already running.
func (m *Manager) Ensure(inst Instance) error {
	m.sweepOrphansOnce()
	return m.ensureOne(inst)
}

// sweepOrphansOnce kills mtg processes left running by a previous x-ui run,
// exactly once per process lifetime and before any of our own mtg are started.
// Because x-ui owns every mtg process, anything alive at this point is an orphan
// that would otherwise keep holding an inbound port with a stale secret.
// The kill itself runs without mu held — only the "already swept" flag needs it.
func (m *Manager) sweepOrphansOnce() {
	m.mu.Lock()
	if m.swept {
		m.mu.Unlock()
		return
	}
	m.swept = true
	m.mu.Unlock()
	// Sweep both backend binaries: a panel may have switched between mtg and
	// mtg-multi across updates, leaving an orphan of the other kind.
	bin := config.GetBinFolderPath()
	total := 0
	for _, name := range []string{multiUserBinaryName(), singleBinaryName()} {
		total += killStrayMtgProcesses(bin + "/" + name)
	}
	if total > 0 {
		logger.Warningf("mtproto: terminated %d orphaned mtg process(es) from a previous run", total)
	}
}

// ensureOne brings a single inbound to the desired state. It takes opMu for the
// duration (so concurrent callers cannot spawn two processes for the same
// inbound) but only takes mu for the map lookups and the final commit.
func (m *Manager) ensureOne(inst Instance) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	fp := inst.fingerprint()
	m.mu.Lock()
	cur, exists := m.procs[inst.Id]
	m.mu.Unlock()

	if exists {
		if cur.fingerprint == fp && cur.proc.IsRunning() {
			m.mu.Lock()
			cur.tag = inst.Tag
			m.mu.Unlock()
			return nil
		}
		cur.proc.Stop() // process I/O: mu intentionally not held
		m.mu.Lock()
		delete(m.procs, inst.Id)
		m.mu.Unlock()
	}

	statsPort, err := FreeLocalPort()
	if err != nil {
		return err
	}
	cfgPath := configPathForID(inst.Id)
	if err := writeConfig(cfgPath, inst, statsPort); err != nil {
		return err
	}
	proc := newProcess(cfgPath, fmt.Sprintf("inbound %d", inst.Id))
	if err := proc.Start(); err != nil {
		return err
	}

	m.mu.Lock()
	m.procs[inst.Id] = &managed{
		proc:         proc,
		tag:          inst.Tag,
		fingerprint:  fp,
		statsPort:    statsPort,
		multiUser:    inst.MultiUser,
		clients:      inst.activeClients(),
		lastByClient: map[string]clientCounter{},
	}
	m.mu.Unlock()

	logger.Infof("mtproto: started %s for inbound %d on %s (%d client(s))", GetBinaryName(), inst.Id, inst.bindTo(), len(inst.activeClients()))
	return nil
}

// Remove stops and forgets the mtg process for an inbound id.
func (m *Manager) Remove(id int) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.removeOne(id)
}

// removeOne drops the process from the map first, then stops it outside mu.
// Caller holds opMu.
func (m *Manager) removeOne(id int) {
	m.mu.Lock()
	cur, ok := m.procs[id]
	if ok {
		delete(m.procs, id)
	}
	m.mu.Unlock()
	if !ok {
		return
	}
	cur.proc.Stop()
	_ = os.Remove(configPathForID(id))
	logger.Infof("mtproto: stopped mtg for inbound %d", id)
}

// Reconcile drives the running set toward the desired instances: it stops
// processes that are no longer wanted and (re)starts the rest. Used at boot
// and periodically to recover from crashes.
func (m *Manager) Reconcile(desired []Instance) {
	m.sweepOrphansOnce()

	want := make(map[int]struct{}, len(desired))
	for _, inst := range desired {
		want[inst.Id] = struct{}{}
	}

	m.mu.Lock()
	stale := make([]int, 0, len(m.procs))
	for id := range m.procs {
		if _, ok := want[id]; !ok {
			stale = append(stale, id)
		}
	}
	m.mu.Unlock()

	// Each item takes opMu on its own instead of holding it for the whole
	// batch, so a client add/edit arriving mid-reconcile waits for one
	// process at most rather than for every mtproto inbound on the server.
	for _, id := range stale {
		m.opMu.Lock()
		m.removeOne(id)
		m.opMu.Unlock()
	}
	for _, inst := range desired {
		if err := m.ensureOne(inst); err != nil {
			logger.Warningf("mtproto: reconcile failed for inbound %d: %v", inst.Id, err)
		}
	}
}

// StopAll stops every managed mtg process. Called on panel shutdown.
// RunningCount returns how many mtg sidecars are alive right now. The panel's
// dashboard shows MTProto as running when at least one is, mirroring how the
// tunnel flavours report their interface.
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	procs := make([]*managed, 0, len(m.procs))
	for _, cur := range m.procs {
		procs = append(procs, cur)
	}
	m.mu.Unlock()

	count := 0
	for _, cur := range procs {
		if cur.proc != nil && cur.proc.IsRunning() {
			count++
		}
	}
	return count
}

func (m *Manager) StopAll() {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	running := m.procs
	m.procs = map[int]*managed{}
	m.mu.Unlock()

	for id, cur := range running {
		_ = cur.proc.Stop()
		_ = os.Remove(configPathForID(id))
	}
}

// CollectTraffic scrapes each running proxy's stats endpoint and returns the
// byte deltas since the previous scrape — per-client for multi-user (mtg-multi
// /stats), per-inbound for single-secret (mtg /metrics, attributed to the sole
// client).
func (m *Manager) CollectTraffic() []Traffic {
	// Snapshot the state we need under the lock, then release before doing
	// network I/O so that Ensure/Reconcile/Remove are not blocked.
	type snap struct {
		id           int
		statsPort    int
		tag          string
		multiUser    bool
		clients      []ClientSecret
		haveLast     bool
		lastUp       int64
		lastDown     int64
		lastByClient map[string]clientCounter
	}
	m.mu.Lock()
	snaps := make([]snap, 0, len(m.procs))
	for id, cur := range m.procs {
		if cur.proc == nil || !cur.proc.IsRunning() {
			continue
		}
		lbc := make(map[string]clientCounter, len(cur.lastByClient))
		for k, v := range cur.lastByClient {
			lbc[k] = v
		}
		snaps = append(snaps, snap{
			id:           id,
			statsPort:    cur.statsPort,
			tag:          cur.tag,
			multiUser:    cur.multiUser,
			clients:      cur.clients,
			haveLast:     cur.haveLast,
			lastUp:       cur.lastUp,
			lastDown:     cur.lastDown,
			lastByClient: lbc,
		})
	}
	m.mu.Unlock()

	out := make([]Traffic, 0, len(snaps))
	for _, s := range snaps {
		if s.multiUser {
			users, ok := scrapeStats(s.statsPort)
			if !ok {
				continue
			}
			nowMs := time.Now().UnixMilli()
			newByClient := make(map[string]clientCounter, len(s.clients))
			for _, c := range s.clients {
				u, exists := users[c.Id]
				if !exists {
					continue
				}
				newByClient[c.Id] = clientCounter{up: u.up, down: u.down}
				var du, dd int64
				if prev, had := s.lastByClient[c.Id]; had {
					du = u.up - prev.up
					dd = u.down - prev.down
					if du < 0 {
						du = 0
					}
					if dd < 0 {
						dd = 0
					}
				}
				// A live connection at scrape time marks the client online now.
				var lastSeen int64
				if u.connections > 0 {
					lastSeen = nowMs
				}
				if du > 0 || dd > 0 || lastSeen > 0 {
					out = append(out, Traffic{Tag: s.tag, Uuid: c.Id, Email: c.Email, Up: du, Down: dd, LastSeen: lastSeen})
				}
			}
			m.mu.Lock()
			if cur, ok := m.procs[s.id]; ok {
				cur.lastByClient = newByClient
			}
			m.mu.Unlock()
			continue
		}

		// Single-secret mode: /metrics is per-inbound; attribute to clients[0].
		up, down, ok := scrapeTraffic(s.statsPort)
		if !ok {
			continue
		}
		var du, dd int64
		if s.haveLast {
			du = up - s.lastUp
			dd = down - s.lastDown
			if du < 0 {
				du = 0
			}
			if dd < 0 {
				dd = 0
			}
		}
		m.mu.Lock()
		if cur, ok := m.procs[s.id]; ok {
			cur.lastUp = up
			cur.lastDown = down
			cur.haveLast = true
		}
		m.mu.Unlock()

		if s.haveLast && (du > 0 || dd > 0) && len(s.clients) > 0 {
			out = append(out, Traffic{Tag: s.tag, Uuid: s.clients[0].Id, Email: s.clients[0].Email, Up: du, Down: dd})
		}
	}
	return out
}

// userStat is one mtg-multi /stats user entry: cumulative byte counters plus the
// number of live connections at scrape time (used for online status).
type userStat struct {
	up          int64
	down        int64
	connections int64
}

// scrapeStats reads mtg-multi's /stats JSON and returns each user's cumulative
// byte counters + live connection count, keyed by the [secrets] entry name
// (= client Uuid). bytes_in is the client's upload (toward Telegram), bytes_out
// its download.
func scrapeStats(port int) (map[string]userStat, bool) {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/stats", port))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	var parsed struct {
		Users map[string]struct {
			Connections int64 `json:"connections"`
			BytesIn     int64 `json:"bytes_in"`
			BytesOut    int64 `json:"bytes_out"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, false
	}
	out := make(map[string]userStat, len(parsed.Users))
	for name, u := range parsed.Users {
		out[name] = userStat{up: u.BytesIn, down: u.BytesOut, connections: u.Connections}
	}
	return out, true
}

// FreeLocalPort asks the OS for an unused loopback TCP port. It is used both
// for mtg's metrics endpoint and to allocate the per-inbound SOCKS egress
// bridge port persisted into mtproto inbound settings.
func FreeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// renderConfig builds the mtg TOML for an instance. Top-level keys must precede
// any [section] header in TOML, so the layout is: required keys, then the
// optional scalar tuning, then [domain-fronting], and finally [stats.prometheus]
// — which x-ui always emits and scrapes for traffic (see scrapeTraffic).
// renderConfig builds the proxy TOML. TOML top-level keys must precede any
// [section] header, so the layout is: top-level keys (secret/bind-to/api-bind-to
// + tuning), then [secrets] (multi-user), [domain-fronting], [network], and
// finally [stats.prometheus] (single-user). statsPort hosts mtg-multi's /stats
// (multi-user) or mtg's /metrics (single-user) — the panel scrapes whichever.
func renderConfig(inst Instance, statsPort int) string {
	var b strings.Builder
	clients := inst.activeClients()
	if inst.MultiUser {
		// mtg-multi reads per-user traffic from /stats on api-bind-to.
		fmt.Fprintf(&b, "api-bind-to = \"127.0.0.1:%d\"\n", statsPort)
	} else if len(clients) > 0 {
		fmt.Fprintf(&b, "secret = %q\n", clients[0].Secret)
	}
	fmt.Fprintf(&b, "bind-to = %q\n", inst.bindTo())
	if inst.Debug {
		b.WriteString("debug = true\n")
	}
	if inst.ProxyProtocolListener {
		b.WriteString("proxy-protocol-listener = true\n")
	}
	if inst.PreferIP != "" {
		fmt.Fprintf(&b, "prefer-ip = %q\n", inst.PreferIP)
	}
	if inst.Concurrency > 0 {
		fmt.Fprintf(&b, "concurrency = %d\n", inst.Concurrency)
	}
	if inst.PublicIPv4 != "" {
		fmt.Fprintf(&b, "public-ipv4 = %q\n", inst.PublicIPv4)
	}
	if inst.PublicIPv6 != "" {
		fmt.Fprintf(&b, "public-ipv6 = %q\n", inst.PublicIPv6)
	}
	if inst.TolerateTimeSkewness != "" {
		fmt.Fprintf(&b, "tolerate-time-skewness = %q\n", inst.TolerateTimeSkewness)
	}
	if inst.MultiUser {
		// The secret name is the client Uuid; quote it so a UUID's dashes are a
		// valid TOML key. mtg-multi reports /stats users under this exact name.
		b.WriteString("\n[secrets]\n")
		for _, c := range clients {
			fmt.Fprintf(&b, "%q = %q\n", c.Id, c.Secret)
		}
	}
	if inst.FrontingHost != "" || inst.FrontingPort > 0 || inst.FrontingProxyProtocol {
		b.WriteString("\n[domain-fronting]\n")
		if inst.FrontingHost != "" {
			// `host`, never `ip`: mtg deprecated the latter and now logs a
			// warning and ignores the value, silently leaving fronting off.
			fmt.Fprintf(&b, "host = %q\n", inst.FrontingHost)
		}
		if inst.FrontingPort > 0 {
			fmt.Fprintf(&b, "port = %d\n", inst.FrontingPort)
		}
		if inst.FrontingProxyProtocol {
			b.WriteString("proxy-protocol = true\n")
		}
	}
	// [network] holds mtg's own resolver and, when the inbound opts into Xray
	// routing, the loopback SOCKS bridge the panel injects into the running Xray
	// config so the egress obeys the core's routing rules.
	routed := inst.RouteThroughXray && inst.XrayRoutePort > 0
	if inst.DNS != "" || routed {
		b.WriteString("\n[network]\n")
		if inst.DNS != "" {
			fmt.Fprintf(&b, "dns = %q\n", inst.DNS)
		}
		if routed {
			fmt.Fprintf(&b, "proxies = [\"socks5://127.0.0.1:%d\"]\n", inst.XrayRoutePort)
		}
	}
	writeDefense(&b, inst)
	if !inst.MultiUser {
		fmt.Fprintf(&b, "\n[stats.prometheus]\nenabled = true\nbind-to = \"127.0.0.1:%d\"\nhttp-path = \"/metrics\"\nmetric-prefix = \"mtg\"\n", statsPort)
	}
	return b.String()
}

// writeDefense renders the [defense.*] sections. Doppelganger is the one that
// answers traffic-shape classification; anti-replay and blocklist are already on
// by default, so only an explicit "off" (or a custom list) is worth writing.
func writeDefense(b *strings.Builder, inst Instance) {
	if len(inst.DoppelgangerURLs) > 0 || inst.DoppelgangerDRS {
		b.WriteString("\n[defense.doppelganger]\n")
		if len(inst.DoppelgangerURLs) > 0 {
			fmt.Fprintf(b, "urls = %s\n", tomlStringArray(inst.DoppelgangerURLs))
		}
		if inst.DoppelgangerRepeats > 0 {
			fmt.Fprintf(b, "repeats-per-raid = %d\n", inst.DoppelgangerRepeats)
		}
		if inst.DoppelgangerRaidEach != "" {
			fmt.Fprintf(b, "raid-each = %q\n", inst.DoppelgangerRaidEach)
		}
		if inst.DoppelgangerDRS {
			b.WriteString("drs = true\n")
		}
	}
	if inst.AntiReplayDisabled {
		b.WriteString("\n[defense.anti-replay]\nenabled = false\n")
	}
	if inst.BlocklistDisabled || len(inst.BlocklistURLs) > 0 {
		b.WriteString("\n[defense.blocklist]\n")
		if inst.BlocklistDisabled {
			b.WriteString("enabled = false\n")
		} else {
			fmt.Fprintf(b, "urls = %s\n", tomlStringArray(inst.BlocklistURLs))
		}
	}
}

// tomlStringArray renders a TOML inline array of quoted strings.
func tomlStringArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, fmt.Sprintf("%q", v))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func writeConfig(path string, inst Instance, statsPort int) error {
	if err := os.MkdirAll(configDir(), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(renderConfig(inst, statsPort)), 0o640)
}

// scrapeTraffic reads the mtg Prometheus metrics endpoint and sums byte
// counters by direction. mtg exposes a traffic counter labelled with a
// direction; "to_telegram" is treated as upload and "to_client" as download.
// Best-effort: an unreachable endpoint or unrecognised format yields ok=false.
func scrapeTraffic(port int) (up int64, down int64, ok bool) {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port))
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	found := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || !strings.Contains(line, "traffic") {
			continue
		}
		name, labels, value, perr := parseMetricLine(line)
		if perr != nil || !strings.HasPrefix(name, "mtg") {
			continue
		}
		switch labels["direction"] {
		case "to_telegram", "egress", "up":
			up += int64(value)
		case "to_client", "ingress", "down":
			down += int64(value)
		default:
			down += int64(value)
		}
		found = true
	}
	if err := scanner.Err(); err != nil {
		logger.Debug("mtproto: metrics scan error:", err)
	}
	return up, down, found
}

func parseMetricLine(line string) (name string, labels map[string]string, value float64, err error) {
	labels = map[string]string{}
	rest := line
	if brace := strings.IndexByte(line, '{'); brace >= 0 {
		name = line[:brace]
		end := strings.IndexByte(line, '}')
		if end < brace {
			return "", nil, 0, fmt.Errorf("malformed metric line")
		}
		for _, kv := range strings.Split(line[brace+1:end], ",") {
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			labels[strings.TrimSpace(kv[:eq])] = strings.Trim(strings.TrimSpace(kv[eq+1:]), `"`)
		}
		rest = strings.TrimSpace(line[end+1:])
	} else {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return "", nil, 0, fmt.Errorf("malformed metric line")
		}
		name = fields[0]
		rest = fields[1]
	}
	valFields := strings.Fields(rest)
	if len(valFields) == 0 {
		return "", nil, 0, fmt.Errorf("missing metric value")
	}
	value, err = strconv.ParseFloat(valFields[0], 64)
	if err != nil {
		return "", nil, 0, err
	}
	return name, labels, value, nil
}
