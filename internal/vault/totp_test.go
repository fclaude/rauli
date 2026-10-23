package vault

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The seed of RFC 6238's test vectors is the ASCII text 12345678901234567890.
const rfcSeed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestTOTPMatchesTheRFCVectors(t *testing.T) {
	for _, tt := range []struct {
		unix int64
		want string // the RFC's eight-digit values for SHA-1
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	} {
		at := time.Unix(tt.unix, 0)
		got8, err := totpCode("otpauth://totp/x?secret="+rfcSeed+"&digits=8", at)
		if err != nil || got8 != tt.want {
			t.Errorf("t=%d, 8 digits: got %q, %v; want %q", tt.unix, got8, err, tt.want)
		}
		// With the default six digits, the code is the last six of those.
		got6, err := totpCode(rfcSeed, at)
		if err != nil || got6 != tt.want[2:] {
			t.Errorf("t=%d, 6 digits: got %q, %v; want %q", tt.unix, got6, err, tt.want[2:])
		}
	}
}

func TestTOTPSeedsAreReadTheWayPeoplePasteThem(t *testing.T) {
	at := time.Unix(1111111109, 0)
	want, _ := totpCode(rfcSeed, at)
	for _, seed := range []string{
		strings.ToLower(rfcSeed),
		"gezd gnbv gy3t qojq gezd gnbv gy3t qojq",
		"GEZD-GNBV-GY3T-QOJQ-GEZD-GNBV-GY3T-QOJQ",
		rfcSeed + "====",
		"  " + rfcSeed + "\n",
		"otpauth://totp/Example:alice?secret=" + rfcSeed + "&issuer=Example",
	} {
		if got, err := totpCode(seed, at); err != nil || got != want {
			t.Errorf("seed %q: got %q, %v; want %q", seed, got, err, want)
		}
	}
	// A 60 second period holds one code from 0 to 59 seconds and changes at 60;
	// a 30 second period has already changed by 59.
	code := func(period, sec int64) string {
		c, err := totpCode(fmt.Sprintf("otpauth://totp/x?secret=%s&period=%d", rfcSeed, period), time.Unix(sec, 0))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if code(60, 0) != code(60, 59) || code(60, 59) == code(60, 60) || code(30, 0) == code(30, 59) {
		t.Errorf("the period is not honoured: %s %s %s / %s %s", code(60, 0), code(60, 59), code(60, 60), code(30, 0), code(30, 59))
	}
}

func TestTOTPRefusesWhatItCannotCompute(t *testing.T) {
	for _, bad := range []string{
		"", "!!!not base32!!!", "1", "otpauth://hotp/x?secret=" + rfcSeed,
		"otpauth://totp/x?secret=" + rfcSeed + "&algorithm=SHA256",
		"otpauth://totp/x?secret=" + rfcSeed + "&digits=4",
		"otpauth://totp/x?secret=" + rfcSeed + "&period=0",
		"otpauth://totp/x?secret=",
	} {
		_, err := totpCode(bad, time.Unix(59, 0))
		if err == nil {
			t.Errorf("%q should be refused", bad)
			continue
		}
		if strings.Contains(err.Error(), rfcSeed) || (bad != "" && len(bad) > 8 && strings.Contains(err.Error(), bad)) {
			t.Errorf("the error repeats the seed: %v", err)
		}
	}
}
