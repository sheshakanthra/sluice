package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Webhook headers (API.md §4).
const (
	SignatureHeader = "MockPay-Signature"
	EventIDHeader   = "MockPay-Event-Id"
	// SignatureTolerance is the allowed |now - t| for a webhook signature.
	SignatureTolerance = 300 * time.Second
)

// Webhook verification errors. All of them mean "reject with 400".
var (
	ErrSignatureMissing   = errors.New("webhook signature missing or malformed")
	ErrSignatureMismatch  = errors.New("webhook signature mismatch")
	ErrSignatureTimestamp = errors.New("webhook signature timestamp outside tolerance")
)

// computeSignature is hex(hmac_sha256(secret, t + "." + body)).
func computeSignature(secret []byte, t int64, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(t, 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	sum := mac.Sum(nil)
	out := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(out, sum)
	return out
}

// Sign returns the MockPay-Signature header value for body sent at t.
func Sign(secret string, t time.Time, body []byte) string {
	ts := t.Unix()
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + string(computeSignature([]byte(secret), ts, body))
}

// VerifySignature checks header against body: a constant-time compare of
// every v1 value, and |now - t| <= tolerance.
func VerifySignature(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var (
		ts     int64
		haveTS bool
		sigs   [][]byte
	)
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ErrSignatureMissing
			}
			ts, haveTS = n, true
		case "v1":
			sigs = append(sigs, []byte(v))
		}
	}
	if !haveTS || len(sigs) == 0 {
		return ErrSignatureMissing
	}
	want := computeSignature([]byte(secret), ts, body)
	match := false
	for _, s := range sigs {
		if hmac.Equal(s, want) { // constant time
			match = true
		}
	}
	if !match {
		return ErrSignatureMismatch
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrSignatureTimestamp
	}
	return nil
}
