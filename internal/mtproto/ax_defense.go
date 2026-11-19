package mtproto

// mtg-multi options 3AX-UI 1.x exposed and upstream does not: worker
// concurrency, clock-skew tolerance, mtg's own resolver and the [defense.*]
// tables (doppelganger, anti-replay, blocklist) that answer DPI probing.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type axDefense struct {
	Concurrency          int
	TolerateTimeSkewness string
	DNS                  string
	DoppelgangerURLs     []string
	DoppelgangerRepeats  int
	DoppelgangerRaidEach string
	DoppelgangerDRS      bool
	AntiReplayDisabled   bool
	BlocklistDisabled    bool
	BlocklistURLs        []string
}

func parseAxDefense(settings string) axDefense {
	var raw struct {
		Concurrency          int    `json:"concurrency"`
		TolerateTimeSkewness string `json:"tolerateTimeSkewness"`
		DNS                  string `json:"dns"`
		Doppelganger         struct {
			URLs           []string `json:"urls"`
			RepeatsPerRaid int      `json:"repeatsPerRaid"`
			RaidEach       string   `json:"raidEach"`
			DRS            bool     `json:"drs"`
		} `json:"doppelganger"`
		// Pointers: absent keeps mtg's default (on); only an explicit false is "off".
		AntiReplay struct {
			Enabled *bool `json:"enabled"`
		} `json:"antiReplay"`
		Blocklist struct {
			Enabled *bool    `json:"enabled"`
			URLs    []string `json:"urls"`
		} `json:"blocklist"`
	}
	if json.Unmarshal([]byte(settings), &raw) != nil {
		return axDefense{}
	}
	return axDefense{
		Concurrency:          max(raw.Concurrency, 0),
		TolerateTimeSkewness: strings.TrimSpace(raw.TolerateTimeSkewness),
		DNS:                  strings.TrimSpace(raw.DNS),
		DoppelgangerURLs:     nonEmpty(raw.Doppelganger.URLs),
		DoppelgangerRepeats:  max(raw.Doppelganger.RepeatsPerRaid, 0),
		DoppelgangerRaidEach: strings.TrimSpace(raw.Doppelganger.RaidEach),
		DoppelgangerDRS:      raw.Doppelganger.DRS,
		AntiReplayDisabled:   raw.AntiReplay.Enabled != nil && !*raw.AntiReplay.Enabled,
		BlocklistDisabled:    raw.Blocklist.Enabled != nil && !*raw.Blocklist.Enabled,
		BlocklistURLs:        nonEmpty(raw.Blocklist.URLs),
	}
}

func nonEmpty(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (d axDefense) fingerprint() string {
	return strings.Join([]string{
		strconv.Itoa(d.Concurrency), d.TolerateTimeSkewness, d.DNS,
		strings.Join(d.DoppelgangerURLs, ","), strconv.Itoa(d.DoppelgangerRepeats), d.DoppelgangerRaidEach,
		strconv.FormatBool(d.DoppelgangerDRS), strconv.FormatBool(d.AntiReplayDisabled),
		strconv.FormatBool(d.BlocklistDisabled), strings.Join(d.BlocklistURLs, ","),
	}, "|")
}

// writeTopLevel adds the scalar options; they must precede the first table.
func (d axDefense) writeTopLevel(b *strings.Builder) {
	if d.Concurrency > 0 {
		fmt.Fprintf(b, "concurrency = %d\n", d.Concurrency)
	}
	if d.TolerateTimeSkewness != "" {
		fmt.Fprintf(b, "tolerate-time-skewness = %q\n", d.TolerateTimeSkewness)
	}
}

// writeDefense adds the [defense.*] tables. Anti-replay and the blocklist are
// on in mtg by default, so only "off" or a custom list is written.
func (d axDefense) writeDefense(b *strings.Builder) {
	if len(d.DoppelgangerURLs) > 0 || d.DoppelgangerDRS {
		b.WriteString("\n[defense.doppelganger]\n")
		if len(d.DoppelgangerURLs) > 0 {
			fmt.Fprintf(b, "urls = %s\n", tomlStringArray(d.DoppelgangerURLs))
		}
		if d.DoppelgangerRepeats > 0 {
			fmt.Fprintf(b, "repeats-per-raid = %d\n", d.DoppelgangerRepeats)
		}
		if d.DoppelgangerRaidEach != "" {
			fmt.Fprintf(b, "raid-each = %q\n", d.DoppelgangerRaidEach)
		}
		if d.DoppelgangerDRS {
			b.WriteString("drs = true\n")
		}
	}
	if d.AntiReplayDisabled {
		b.WriteString("\n[defense.anti-replay]\nenabled = false\n")
	}
	if d.BlocklistDisabled {
		b.WriteString("\n[defense.blocklist]\nenabled = false\n")
	} else if len(d.BlocklistURLs) > 0 {
		fmt.Fprintf(b, "\n[defense.blocklist]\nurls = %s\n", tomlStringArray(d.BlocklistURLs))
	}
}

func tomlStringArray(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
