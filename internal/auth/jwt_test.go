package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/config"
)

var (
	k1 = config.TokenKey{KID: "k1", Secret: []byte("k1-0123456789abcdef0123456789abcd")}
	k2 = config.TokenKey{KID: "k2", Secret: []byte("k2-0123456789abcdef0123456789abcd")}
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func ring(t *testing.T, c *clock, keys ...config.TokenKey) *Keyring {
	t.Helper()
	k, err := NewKeyring(keys, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestIssueVerify(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)}
	kr := ring(t, c, k1)
	uid, eid := uuid.New(), uuid.New()
	tkt := int64(42)

	tok, claims, err := kr.Issue(AudQueue, uid, eid, &tkt, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := kr.Verify(tok, AudQueue)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != uid || got.EventID != eid || got.Ticket == nil || *got.Ticket != 42 || got.ID != claims.ID {
		t.Fatalf("claims = %+v", got)
	}
	if got.ExpiresAt() != c.t.Add(time.Minute) {
		t.Errorf("exp = %v", got.ExpiresAt())
	}

	sess, _, err := kr.Issue(AudSession, uid, uuid.Nil, nil, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	hdr, _ := base64.RawURLEncoding.DecodeString(strings.Split(sess, ".")[0])
	var h map[string]string
	if err := json.Unmarshal(hdr, &h); err != nil || h["alg"] != "HS256" || h["kid"] != "k1" || h["typ"] != "JWT" {
		t.Errorf("header = %s", hdr)
	}
	body, _ := base64.RawURLEncoding.DecodeString(strings.Split(sess, ".")[1])
	if strings.Contains(string(body), `"eid"`) || strings.Contains(string(body), `"tkt"`) || !strings.Contains(string(body), `"iss":"sluice"`) {
		t.Errorf("session token carries eid/tkt: %s", body)
	}
}

func TestVerifyRejects(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)}
	kr := ring(t, c, k1)
	uid := uuid.New()
	tok, _, err := kr.Issue(AudAdmission, uid, uuid.New(), nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	other, _, _ := ring(t, c, k2).Issue(AudAdmission, uid, uuid.New(), nil, time.Minute)
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT","kid":"k1"}`)) + "." + parts[1] + "."

	tests := []struct {
		name  string
		token string
		aud   Audience
		at    time.Time
		want  error
	}{
		{"valid", tok, AudAdmission, c.t, nil},
		{"wrong audience", tok, AudSession, c.t, ErrTokenInvalid},
		{"malformed", "abc", AudAdmission, c.t, ErrTokenInvalid},
		{"tampered claims", parts[0] + "." + parts[1] + "x." + parts[2], AudAdmission, c.t, ErrTokenInvalid},
		{"tampered signature", parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2])), AudAdmission, c.t, ErrTokenInvalid},
		{"alg none", none, AudAdmission, c.t, ErrTokenInvalid},
		{"unknown kid", other, AudAdmission, c.t, ErrTokenInvalid},
		{"expired 4s ago: within skew", tok, AudAdmission, c.t.Add(time.Minute + 4*time.Second), nil},
		{"expired 5s ago: past skew", tok, AudAdmission, c.t.Add(time.Minute + 5*time.Second), ErrTokenExpired},
		{"issued 5s in the future: within skew", tok, AudAdmission, c.t.Add(-5 * time.Second), nil},
		{"issued 6s in the future", tok, AudAdmission, c.t.Add(-6 * time.Second), ErrTokenInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := ring(t, &clock{t: tc.at}, k1)
			_, err := v.Verify(tc.token, tc.aud)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestKeyRotation: a ring [k2, k1] signs with k2 and still accepts k1 tokens.
func TestKeyRotation(t *testing.T) {
	c := &clock{t: time.Now()}
	old, _, err := ring(t, c, k1).Issue(AudSession, uuid.New(), uuid.Nil, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rotated := ring(t, c, k2, k1)
	if _, err := rotated.Verify(old, AudSession); err != nil {
		t.Errorf("old-key token after rotation: %v", err)
	}
	fresh, _, _ := rotated.Issue(AudSession, uuid.New(), uuid.Nil, nil, time.Hour)
	if _, err := ring(t, c, k1).Verify(fresh, AudSession); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("k2 token on a k1-only ring: %v, want invalid", err)
	}
}

func TestNewKeyringRejectsWeakKeys(t *testing.T) {
	if _, err := NewKeyring(nil, nil); err == nil {
		t.Error("empty ring accepted")
	}
	if _, err := NewKeyring([]config.TokenKey{{KID: "x", Secret: []byte("short")}}, nil); err == nil {
		t.Error("short key accepted")
	}
}

// TestIssueUntil: the expiry comes from the caller's absolute time, floored
// to the second, whatever the clock reads when the token is issued; the
// returned claims match what Verify reads back.
func TestIssueUntil(t *testing.T) {
	exp := time.Date(2026, 11, 23, 10, 5, 7, 850_000_000, time.UTC) // .85 s
	want := time.Date(2026, 11, 23, 10, 5, 7, 0, time.UTC)
	uid, eid := uuid.New(), uuid.New()
	for _, now := range []time.Time{
		time.Date(2026, 11, 23, 10, 0, 0, 100_000_000, time.UTC),
		time.Date(2026, 11, 23, 10, 0, 0, 900_000_000, time.UTC),
		time.Date(2026, 11, 23, 10, 4, 59, 999_000_000, time.UTC),
	} {
		c := &clock{t: now}
		kr := ring(t, c, k1)
		tok, claims, err := kr.IssueUntil(AudAdmission, uid, eid, nil, exp)
		if err != nil {
			t.Fatal(err)
		}
		got, err := kr.Verify(tok, AudAdmission)
		if err != nil {
			t.Fatal(err)
		}
		if !claims.ExpiresAt().Equal(want) || !got.ExpiresAt().Equal(want) {
			t.Errorf("now %v: claims exp %v, verified exp %v, want %v", now, claims.ExpiresAt(), got.ExpiresAt(), want)
		}
	}
}
