package vault

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// totpCode computes the one-time code for a moment, as RFC 6238 describes it:
// HMAC-SHA-1 over the number of periods since the epoch, truncated as RFC 4226
// says. A seed is either plain base32, which gives six digits and a 30 second
// period, or an otpauth:// address, which may carry digits and period. Neither
// the seed nor the code appears in an error.
func totpCode(seed string, at time.Time) (string, error) {
	digits, period := 6, 30
	secret := strings.TrimSpace(seed)

	if strings.HasPrefix(strings.ToLower(secret), "otpauth://") {
		u, err := url.Parse(secret)
		if err != nil || !strings.EqualFold(u.Host, "totp") {
			return "", errors.New("the one-time code setup is not an otpauth://totp address")
		}
		q := u.Query()
		secret = q.Get("secret")
		if a := q.Get("algorithm"); a != "" && !strings.EqualFold(a, "SHA1") {
			return "", fmt.Errorf("one-time codes made with %s are not supported; SHA1 is the one that works", a)
		}
		if d := q.Get("digits"); d != "" {
			n, err := strconv.Atoi(d)
			if err != nil || n < 6 || n > 8 {
				return "", errors.New("the one-time code needs 6, 7 or 8 digits")
			}
			digits = n
		}
		if p := q.Get("period"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > 3600 {
				return "", errors.New("the one-time code period must be between 1 and 3600 seconds")
			}
			period = n
		}
	}

	key, err := decodeSeed(secret)
	if err != nil {
		return "", errors.New("the one-time code seed is not valid base32")
	}
	defer func() {
		for i := range key {
			key[i] = 0
		}
	}()

	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix())/uint64(period))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%mod), nil
}

// decodeSeed reads base32 the way people paste it: in any case, with spaces,
// dashes or missing padding.
func decodeSeed(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(s))
	s = strings.TrimRight(s, "=")
	if s == "" {
		return nil, errors.New("empty")
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
}
