package tunnel

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// to30Server maps a generated 3.0 set onto a server record the way the panel
// does when the admin saves the form.
func to30Server(o Obfuscation30) *Server {
	s := toServer(o.Obfuscation20)
	s.HeaderProtectionKey = o.HeaderProtectionKey
	s.ContentPaddingAddition = o.ContentPaddingAddition
	s.RekeyAfterTime = o.RekeyAfterTime
	s.RekeyTimeout = o.RekeyTimeout
	s.RejectAfterTime = o.RejectAfterTime
	s.KeepaliveTimeout = o.KeepaliveTimeout
	s.MaxHandshakeAttempts = o.MaxHandshakeAttempts
	s.RandomTrailers = o.RandomTrailers
	s.DisableCookies = o.DisableCookies
	return s
}

// TestGenerateObfuscation30 pins the two constraints the kernel imposes on a
// generated 3.0 set. Violate either and `awg setconf` fails with nothing but
// "Invalid argument", so they have to hold for every draw, not most of them.
func TestGenerateObfuscation30(t *testing.T) {
	for i := 0; i < 500; i++ {
		for _, preset := range []string{"default", "mobile"} {
			o := GenerateObfuscation30(preset)

			if err := ValidateObfuscation(to30Server(o)); err != nil {
				t.Fatalf("generated %s set rejected by its own validator: %v", preset, err)
			}

			// Header protection carries its nonce in the padding, so every
			// packet type needs at least that much.
			for n, s := range []int{o.S1, o.S2, o.S3, o.S4} {
				if s < headerProtectionNonceSize {
					t.Fatalf("%s: S%d = %d is below the nonce size %d", preset, n+1, s, headerProtectionNonceSize)
				}
			}

			raw, err := base64.StdEncoding.DecodeString(o.HeaderProtectionKey)
			if err != nil || len(raw) != headerProtectionKeyLen {
				t.Fatalf("%s: header protection key %q is not %d base64 bytes (err %v)",
					preset, o.HeaderProtectionKey, headerProtectionKeyLen, err)
			}

			// A session that expires before it renegotiates drops every few
			// minutes, so the whole rekey window must sit below the reject one
			// whatever the kernel draws from each range.
			_, rekeyHi := bounds(t, o.RekeyAfterTime)
			rejectLo, _ := bounds(t, o.RejectAfterTime)
			if rekeyHi >= rejectLo {
				t.Fatalf("%s: RekeyAfterTime %s can reach RejectAfterTime %s",
					preset, o.RekeyAfterTime, o.RejectAfterTime)
			}

			if !o.RandomTrailers {
				t.Fatalf("%s: RandomTrailers should be on in a generated 3.0 set", preset)
			}
			// Cookies are the flood protection; a generator must not silently
			// trade it away.
			if o.DisableCookies {
				t.Fatalf("%s: DisableCookies must stay off unless the operator asks", preset)
			}
		}
	}
}

// bounds parses "N" or "N-M" the way amneziawg-tools does.
func bounds(t *testing.T, v string) (int, int) {
	t.Helper()
	if err := validateU16Range(v); err != nil {
		t.Fatalf("range %q is invalid: %v", v, err)
	}
	lo, hi, isRange := strings.Cut(v, "-")
	l := atoiOrFail(t, lo)
	if !isRange {
		return l, l
	}
	return l, atoiOrFail(t, hi)
}

func atoiOrFail(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			t.Fatalf("%q is not a number", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func TestValidateU16Range(t *testing.T) {
	for _, ok := range []string{"", "0", "5", "5-10", "65535", "0-65535", " 7 - 9 "} {
		if err := validateU16Range(ok); err != nil {
			t.Errorf("%q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"abc", "10-5", "-1", "65536", "1-70000", "1-2-3", "5-"} {
		if err := validateU16Range(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestValidateHeaderProtectionKey(t *testing.T) {
	if err := validateHeaderProtectionKey(GenerateHeaderProtectionKey()); err != nil {
		t.Errorf("a freshly generated key was rejected: %v", err)
	}
	// A WireGuard key is the same length, so length alone is not enough of a
	// check for the operator — but the wrong length must fail.
	for _, bad := range []string{"not base64!", base64.StdEncoding.EncodeToString([]byte("short")), "aGVsbG8="} {
		if err := validateHeaderProtectionKey(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

// TestHeaderProtectionRequiresPadding: the kernel refuses the interface when a
// packet type has less padding than the nonce needs. The panel has to say so
// while the operator can still act on it, instead of letting awg-quick fail.
func TestHeaderProtectionRequiresPadding(t *testing.T) {
	base := func() *Server {
		return &Server{
			Jmin: 50, Jmax: 1000, S1: 20, S2: 30, S3: 20, S4: 20,
			H1: "1", H2: "2", H3: "3", H4: "4",
			HeaderProtectionKey: GenerateHeaderProtectionKey(),
		}
	}
	if err := ValidateObfuscation(base()); err != nil {
		t.Fatalf("a valid header-protected server was rejected: %v", err)
	}
	for i, mangle := range []func(*Server){
		func(s *Server) { s.S1 = 11 },
		func(s *Server) { s.S2 = 0 },
		func(s *Server) { s.S3 = 0 },
		func(s *Server) { s.S4 = 5 },
	} {
		s := base()
		mangle(s)
		err := ValidateObfuscation(s)
		if err == nil {
			t.Errorf("case %d: too-small padding was accepted", i)
			continue
		}
		if !strings.Contains(err.Error(), "header protection") {
			t.Errorf("case %d: error should explain the cause, got %q", i, err)
		}
	}
	// Without a key the same paddings are fine — 1.x/2.0 servers are unaffected.
	s := base()
	s.HeaderProtectionKey = ""
	s.S1, s.S2, s.S3, s.S4 = 0, 0, 0, 0
	if err := ValidateObfuscation(s); err != nil {
		t.Fatalf("a server without header protection was rejected: %v", err)
	}
}

func TestMajorVersion(t *testing.T) {
	cases := map[string]int{
		"v3.1.20260812": 3,
		"3.1.20260812":  3,
		"v1.0.20210914": 1,
		"":              0,
		"unknown":       0,
	}
	for in, want := range cases {
		if got := majorVersion(in); got != want {
			t.Errorf("majorVersion(%q) = %d, want %d", in, got, want)
		}
	}
}

// TestObfuscation30JSONShape pins the wire contract with the panel: the JS
// generator handler copies these exact keys into the form, and an embedded
// struct that stopped inlining would silently deliver a nested object instead.
func TestObfuscation30JSONShape(t *testing.T) {
	raw, err := json.Marshal(GenerateObfuscation30("default"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{
		// the 2.0 half the form has always read
		"jc", "jmin", "jmax", "s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4",
		"i1", "i2", "i3", "i4", "i5",
		// and the 3.0 fields, named as AWG3_FIELDS in awg.html
		"headerProtectionKey", "contentPaddingAddition", "rekeyAfterTime",
		"rekeyTimeout", "rejectAfterTime", "keepaliveTimeout",
		"maxHandshakeAttempts", "randomTrailers", "disableCookies",
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("generated set is missing %q; the form field would stay empty", k)
		}
	}
	if len(got) != len(want) {
		t.Errorf("payload has %d keys, expected %d: %v", len(got), len(want), got)
	}
}

// TestGeneratedSignaturePackets: I2-I5 are parsed by the kernel module, and a
// malformed tag is refused with EINVAL — the interface then fails to come up.
// The grammar is <b 0xHEX> (even number of hex digits), <r N>, <c>, <t>.
func TestGeneratedSignaturePackets(t *testing.T) {
	tag := regexp.MustCompile(`^<(b 0x[0-9a-f]+|r [0-9]+|c|t)>$`)
	for i := 0; i < 200; i++ {
		o := GenerateObfuscation20("default")
		for n, packet := range map[string]string{"I2": o.I2, "I3": o.I3, "I4": o.I4, "I5": o.I5} {
			if packet == "" {
				t.Fatalf("%s should be generated", n)
			}
			// Split "<a><b>" into its tags without losing the delimiters.
			parts := strings.SplitAfter(packet, ">")
			for _, p := range parts {
				if p == "" {
					continue
				}
				if !tag.MatchString(p) {
					t.Fatalf("%s = %q has an unparsable tag %q", n, packet, p)
				}
				if hex, ok := strings.CutPrefix(p, "<b 0x"); ok {
					hex = strings.TrimSuffix(hex, ">")
					if len(hex)%2 != 0 {
						t.Fatalf("%s = %q: literal bytes need an even number of hex digits", n, packet)
					}
				}
			}
		}
	}
}
