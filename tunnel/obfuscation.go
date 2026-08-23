package tunnel

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Obfuscation20 is a generated AmneziaWG 2.0 obfuscation parameter set. The
// same values must be applied to both ends of a tunnel, so the server stores
// them and every client config inherits them.
type Obfuscation20 struct {
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1"`
	S2   int    `json:"s2"`
	S3   int    `json:"s3"`
	S4   int    `json:"s4"`
	H1   string `json:"h1"`
	H2   string `json:"h2"`
	H3   string `json:"h3"`
	H4   string `json:"h4"`
	I1   string `json:"i1"`
}

// awgHMax is the upper bound for H values: 2^31-1. The AmneziaWG spec allows the
// full uint32, but the amneziawg-windows-client config editor rejects values
// above 2^31-1, so we stay in the safe half for cross-client compatibility.
const awgHMax = 2147483647

// hMinWidth is the minimum width of each H1-H4 range.
const hMinWidth = 1000

// randInt returns a uniform random int in [min, max] using crypto/rand.
// Falls back to min on the (practically impossible) RNG error.
func randInt(min, max int) int {
	if max <= min {
		return min
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min)+1))
	if err != nil {
		return min
	}
	return min + int(n.Int64())
}

// GenerateObfuscation20 produces a randomized AmneziaWG 2.0 parameter set.
// preset "mobile" tunes junk packets for restrictive mobile carriers; any other
// value uses the balanced "default" preset. Ranges and constraints follow the
// maintained bivlked/amneziawg-installer generator. Values are randomized per
// call so each server gets a unique fingerprint (static values get profiled by
// DPI), which is the whole point of the obfuscation.
func GenerateObfuscation20(preset string) Obfuscation20 {
	var o Obfuscation20

	switch preset {
	case "mobile":
		// Jc=3 and a narrow Jmax survive carriers like Tele2/Yota/Megafon.
		o.Jc = 3
		o.Jmin = randInt(30, 50)
		o.Jmax = o.Jmin + randInt(20, 80)
	default:
		// Balance between obfuscation strength and mobile compatibility.
		o.Jc = randInt(3, 6)
		o.Jmin = randInt(40, 89)
		o.Jmax = o.Jmin + randInt(50, 250)
	}

	o.S1 = randInt(15, 150)
	o.S2 = randInt(15, 150)
	// Kernel constraint: S1+56 must not equal S2 (else init and response
	// handshake packets end up the same size after padding).
	for o.S1+56 == o.S2 {
		o.S2 = randInt(15, 150)
	}
	o.S3 = randInt(8, 55) // cookie padding (max 64)
	o.S4 = randInt(4, 27) // transport padding (max 32)

	h := generateHRanges()
	o.H1, o.H2, o.H3, o.H4 = h[0], h[1], h[2], h[3]

	// CPS signature packet: N random bytes prepended before each handshake.
	o.I1 = fmt.Sprintf("<r %d>", randInt(32, 256))

	return o
}

// generateHRanges returns four non-overlapping "low-high" ranges for H1-H4.
// Each is at least hMinWidth wide, the lowest bound is >= 5 (values 1-4 are
// reserved for vanilla WireGuard message types) and the highest is <= 2^31-1.
// The space is split into four bands and a random sub-range is taken from each,
// which guarantees non-overlap (with a gap) and a valid width without retries.
func generateHRanges() [4]string {
	const lo = 5
	bandSize := (awgHMax - lo + 1) / 4
	var out [4]string
	for i := 0; i < 4; i++ {
		bandLo := lo + i*bandSize
		bandHi := bandLo + bandSize - 1
		// Reserve room for the minimum width and a 1-value gap to the next band.
		start := randInt(bandLo, bandHi-hMinWidth-1)
		end := randInt(start+hMinWidth, bandHi-1)
		out[i] = fmt.Sprintf("%d-%d", start, end)
	}
	return out
}

// hMaxValid is the largest accepted H value: uint32 max, the kernel's limit.
const hMaxValid int64 = 4294967295

// ValidateObfuscation rejects malformed obfuscation parameters before they are
// saved and applied, so a bad manual entry can't bring the interface down on
// `awg-quick up`. Empty H values are allowed (they fall back to a default when
// the config is generated). Accepts a single value ("1") or a range ("100-800").
func ValidateObfuscation(server *Server) error {
	if server.Jmin > server.Jmax {
		return fmt.Errorf("invalid Jmin/Jmax: %d must not exceed %d", server.Jmin, server.Jmax)
	}
	if server.S3 < 0 || server.S3 > 64 {
		return fmt.Errorf("invalid S3 value %d (must be 0..64)", server.S3)
	}
	if server.S4 < 0 || server.S4 > 32 {
		return fmt.Errorf("invalid S4 value %d (must be 0..32)", server.S4)
	}
	for i, h := range []string{server.H1, server.H2, server.H3, server.H4} {
		if err := validateHValue(h); err != nil {
			return fmt.Errorf("invalid H%d: %w", i+1, err)
		}
	}
	return validateObfuscation30(server)
}

// validateHValue checks one H parameter: empty, a single uint32, or "low-high"
// with 0 <= low <= high <= uint32 max.
func validateHValue(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if lo, hi, isRange := strings.Cut(v, "-"); isRange {
		l, err1 := strconv.ParseInt(strings.TrimSpace(lo), 10, 64)
		h, err2 := strconv.ParseInt(strings.TrimSpace(hi), 10, 64)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("range %q must be two integers", v)
		}
		if l < 0 || h > hMaxValid || l > h {
			return fmt.Errorf("range %q must satisfy 0 <= low <= high <= %d", v, hMaxValid)
		}
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || n > hMaxValid {
		return fmt.Errorf("value %q must be an integer in 0..%d or a low-high range", v, hMaxValid)
	}
	return nil
}

// ====================== AmneziaWG 3.0 ======================

// headerProtectionNonceSize mirrors HEADER_PROTECTION_NONCE_SIZE in the kernel
// module. With header protection on, the module rejects the interface unless
// S1-S4 each reserve at least this many bytes, because the nonce is carried in
// that padding. Getting this wrong costs an interface that refuses to come up
// with nothing but "Invalid argument" from `awg setconf`.
const headerProtectionNonceSize = 12

// headerProtectionKeyLen is the raw length of the ChaCha20 key the config
// carries base64-encoded.
const headerProtectionKeyLen = 32

// u16RangeMax is the largest value either end of a 3.0 range may take: the
// kernel stores these as two uint16 halves of one u32.
const u16RangeMax = 65535

// Obfuscation30 is a generated AmneziaWG 3.0 parameter set: the whole 2.0 set
// plus header protection and the randomised protocol timers. The 2.0 half and
// HeaderProtectionKey must be identical on both ends; the timers need not be,
// but the panel writes them to both configs so a client behaves like its server.
type Obfuscation30 struct {
	Obfuscation20
	HeaderProtectionKey    string `json:"headerProtectionKey"`
	ContentPaddingAddition string `json:"contentPaddingAddition"`
	RekeyAfterTime         string `json:"rekeyAfterTime"`
	RekeyTimeout           string `json:"rekeyTimeout"`
	RejectAfterTime        string `json:"rejectAfterTime"`
	KeepaliveTimeout       string `json:"keepaliveTimeout"`
	MaxHandshakeAttempts   string `json:"maxHandshakeAttempts"`
	RandomTrailers         bool   `json:"randomTrailers"`
	DisableCookies         bool   `json:"disableCookies"`
}

// GenerateObfuscation30 produces a randomized AmneziaWG 3.0 parameter set on top
// of the 2.0 one for the same preset.
//
// Two constraints shape the numbers. S3/S4 are lifted to at least the nonce
// size, because header protection is part of the set and the kernel would
// otherwise refuse the interface. And the rekey window stays strictly below the
// reject window whatever the random draw, since a session that expires before
// it renegotiates would drop every few minutes.
func GenerateObfuscation30(preset string) Obfuscation30 {
	base := GenerateObfuscation20(preset)
	if base.S3 < headerProtectionNonceSize {
		base.S3 = randInt(headerProtectionNonceSize, 55)
	}
	if base.S4 < headerProtectionNonceSize {
		base.S4 = randInt(headerProtectionNonceSize, 32)
	}

	o := Obfuscation30{
		Obfuscation20:       base,
		HeaderProtectionKey: GenerateHeaderProtectionKey(),
		// Kernel defaults are 120 / 5 / 180 / 10 / 18. Each range brackets its
		// default closely enough to stay well-behaved, while making the timing
		// of a session differ from every other server running the same panel.
		ContentPaddingAddition: randRange(0, 16, 8, 48),
		RekeyAfterTime:         randRange(100, 115, 15, 30),
		RekeyTimeout:           randRange(4, 6, 1, 3),
		RejectAfterTime:        randRange(165, 185, 10, 25),
		KeepaliveTimeout:       randRange(8, 11, 2, 4),
		MaxHandshakeAttempts:   randRange(14, 18, 2, 6),
		RandomTrailers:         true,
		// Cookies are WireGuard's answer to handshake floods. Turning them off
		// removes one more distinguishable packet type, but it is a trade the
		// operator should make deliberately, not one a generator makes for them.
		DisableCookies: false,
	}
	return o
}

// randRange builds a "lo-hi" range string: lo is drawn from [loMin, loMax] and
// hi sits a random [widthMin, widthMax] above it, clamped to the u16 ceiling.
func randRange(loMin, loMax, widthMin, widthMax int) string {
	lo := randInt(loMin, loMax)
	hi := lo + randInt(widthMin, widthMax)
	if hi > u16RangeMax {
		hi = u16RangeMax
	}
	return fmt.Sprintf("%d-%d", lo, hi)
}

// GenerateHeaderProtectionKey returns a fresh base64 ChaCha20 key for
// HeaderProtectionKey. On the (practically impossible) RNG failure it returns
// "", which the caller treats as "header protection stays off" rather than
// installing a predictable key.
func GenerateHeaderProtectionKey() string {
	buf := make([]byte, headerProtectionKeyLen)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// validateHeaderProtectionKey accepts the base64 form of exactly 32 bytes,
// which is what the tools' parse_key() will take.
func validateHeaderProtectionKey(v string) error {
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return fmt.Errorf("must be base64: %w", err)
	}
	if len(raw) != headerProtectionKeyLen {
		return fmt.Errorf("must decode to %d bytes, got %d", headerProtectionKeyLen, len(raw))
	}
	return nil
}

// validateU16Range checks one 3.0 range parameter: empty, a single value, or
// "low-high" with 0 <= low <= high <= 65535 — the same shape
// u16_range_from_string() accepts in amneziawg-tools.
func validateU16Range(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	lo, hi, isRange := strings.Cut(v, "-")
	l, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil {
		return fmt.Errorf("value %q must be a number or a low-high range", v)
	}
	h := l
	if isRange {
		if h, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil {
			return fmt.Errorf("range %q must be two numbers", v)
		}
	}
	if l < 0 || h > u16RangeMax || l > h {
		return fmt.Errorf("range %q must satisfy 0 <= low <= high <= %d", v, u16RangeMax)
	}
	return nil
}

// validateObfuscation30 checks the 3.0 half of a server record.
func validateObfuscation30(server *Server) error {
	for _, p := range []struct{ name, value string }{
		{"ContentPaddingAddition", server.ContentPaddingAddition},
		{"RekeyAfterTime", server.RekeyAfterTime},
		{"RekeyTimeout", server.RekeyTimeout},
		{"RejectAfterTime", server.RejectAfterTime},
		{"KeepaliveTimeout", server.KeepaliveTimeout},
		{"MaxHandshakeAttempts", server.MaxHandshakeAttempts},
	} {
		if err := validateU16Range(p.value); err != nil {
			return fmt.Errorf("invalid %s: %w", p.name, err)
		}
	}

	key := strings.TrimSpace(server.HeaderProtectionKey)
	if key == "" {
		return nil
	}
	if err := validateHeaderProtectionKey(key); err != nil {
		return fmt.Errorf("invalid HeaderProtectionKey: %w", err)
	}
	// The kernel refuses the interface outright when any padding is too small
	// to hold the nonce, so catch it here where it can still be explained.
	for i, s := range []int{server.S1, server.S2, server.S3, server.S4} {
		if s < headerProtectionNonceSize {
			return fmt.Errorf(
				"S%d = %d is too small for header protection: every packet type needs at least %d bytes of padding to carry the nonce",
				i+1, s, headerProtectionNonceSize)
		}
	}
	return nil
}
