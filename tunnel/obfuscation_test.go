package tunnel

import (
	"strconv"
	"strings"
	"testing"
)

func toServer(o Obfuscation20) *Server {
	return &Server{
		Jc: o.Jc, Jmin: o.Jmin, Jmax: o.Jmax,
		S1: o.S1, S2: o.S2, S3: o.S3, S4: o.S4,
		H1: o.H1, H2: o.H2, H3: o.H3, H4: o.H4, I1: o.I1,
	}
}

func TestValidateObfuscation(t *testing.T) {
	// Every generated set must validate.
	for i := 0; i < 500; i++ {
		if err := ValidateObfuscation(toServer(GenerateObfuscation20("default"))); err != nil {
			t.Fatalf("generated default set rejected: %v", err)
		}
		if err := ValidateObfuscation(toServer(GenerateObfuscation20("mobile"))); err != nil {
			t.Fatalf("generated mobile set rejected: %v", err)
		}
	}
	// Legacy 1.x defaults and an empty (fallback) H value are valid.
	if err := ValidateObfuscation(&Server{Jmin: 50, Jmax: 1000, H1: "1", H2: "2", H3: "3", H4: "4"}); err != nil {
		t.Fatalf("1.x defaults rejected: %v", err)
	}
	if err := ValidateObfuscation(&Server{Jmax: 1, H1: "", H2: "2", H3: "3", H4: "4"}); err != nil {
		t.Fatalf("empty H rejected: %v", err)
	}
	if err := ValidateObfuscation(&Server{Jmax: 1, H1: "100000-800000", H2: "2", H3: "3", H4: "4"}); err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}
	// Malformed inputs must be rejected.
	bad := []*Server{
		{Jmin: 100, Jmax: 50, H1: "1", H2: "2", H3: "3", H4: "4"}, // Jmin > Jmax
		{Jmax: 1, S3: 100, H1: "1", H2: "2", H3: "3", H4: "4"},    // S3 > 64
		{Jmax: 1, S4: 100, H1: "1", H2: "2", H3: "3", H4: "4"},    // S4 > 32
		{Jmax: 1, H1: "abc", H2: "2", H3: "3", H4: "4"},           // non-numeric
		{Jmax: 1, H1: "800-100", H2: "2", H3: "3", H4: "4"},       // low > high
		{Jmax: 1, H1: "1-2-3", H2: "2", H3: "3", H4: "4"},         // malformed range
	}
	for i, b := range bad {
		if err := ValidateObfuscation(b); err == nil {
			t.Fatalf("invalid case %d was accepted", i)
		}
	}
}

func parseRange(t *testing.T, s string) (int, int) {
	t.Helper()
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("H value %q is not a range", s)
	}
	lo, err1 := strconv.Atoi(parts[0])
	hi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		t.Fatalf("H range %q has non-numeric bounds", s)
	}
	return lo, hi
}

func TestGenerateObfuscation20(t *testing.T) {
	// Run many iterations: generation is randomized, so constraints must hold
	// for every produced set, not just one lucky draw.
	for i := 0; i < 2000; i++ {
		for _, preset := range []string{"default", "mobile"} {
			o := GenerateObfuscation20(preset)

			// Junk packets
			if preset == "mobile" && o.Jc != 3 {
				t.Fatalf("mobile Jc=%d, want 3", o.Jc)
			}
			if o.Jc < 1 {
				t.Fatalf("Jc=%d must be >= 1", o.Jc)
			}
			if o.Jmin >= o.Jmax {
				t.Fatalf("Jmin=%d must be < Jmax=%d", o.Jmin, o.Jmax)
			}
			if o.Jmax > 1280 {
				t.Fatalf("Jmax=%d exceeds 1280", o.Jmax)
			}

			// Padding
			if o.S1+56 == o.S2 {
				t.Fatalf("constraint violated: S1+56 == S2 (%d,%d)", o.S1, o.S2)
			}
			if o.S3 < 0 || o.S3 > 64 {
				t.Fatalf("S3=%d out of [0,64]", o.S3)
			}
			if o.S4 < 0 || o.S4 > 32 {
				t.Fatalf("S4=%d out of [0,32]", o.S4)
			}

			// Header ranges: each valid, ascending, non-overlapping, >=5, <=2^31-1
			ranges := [4]string{o.H1, o.H2, o.H3, o.H4}
			prevHi := 4 // values 1-4 reserved
			for _, r := range ranges {
				lo, hi := parseRange(t, r)
				if lo < 5 {
					t.Fatalf("H range %q lower bound < 5", r)
				}
				if hi > awgHMax {
					t.Fatalf("H range %q upper bound > 2^31-1", r)
				}
				if hi-lo < hMinWidth {
					t.Fatalf("H range %q narrower than %d", r, hMinWidth)
				}
				if lo <= prevHi {
					t.Fatalf("H ranges overlap or unordered: %q after hi=%d", r, prevHi)
				}
				prevHi = hi
			}

			// CPS signature packet
			if !strings.HasPrefix(o.I1, "<r ") || !strings.HasSuffix(o.I1, ">") {
				t.Fatalf("I1=%q not in <r N> form", o.I1)
			}
		}
	}
}
