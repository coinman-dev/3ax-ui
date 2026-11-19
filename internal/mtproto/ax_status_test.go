package mtproto

import "testing"

func TestParseMtgVersion(t *testing.T) {
	cases := map[string]string{
		"1.16.1 (go1.27.3: 2026-10-25T11:43:56Z on a45ebefed8, modules checksum X)\n": "1.16.1",
		"v2.1.7\n": "2.1.7",
		"":         "",
		"garbage":  "",
	}
	for out, want := range cases {
		if got := parseMtgVersion(out); got != want {
			t.Errorf("parseMtgVersion(%q) = %q, want %q", out, got, want)
		}
	}
}
