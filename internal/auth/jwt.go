// Package auth issues and verifies SLUICE's stateless tokens (API.md §1.1)
// with golang-jwt/jwt/v5: HS256, signed with the first key of the TOKEN_KEYS
// ring and verified against any key in it by the `kid` header. Verification
// is strict: only HS256, a known kid, iss=sluice, the expected audience, and
// exp/iat checked with 5 s of clock-skew leeway.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/config"
)

// Audience is the `aud` claim; each token type has its own.
type Audience string

// Token audiences.
const (
	AudSession   Audience = "session"
	AudQueue     Audience = "queue"
	AudAdmission Audience = "admission"
)

// Issuer is the fixed `iss` claim.
const Issuer = "sluice"

// ClockSkew is tolerated on exp and iat.
const ClockSkew = 5 * time.Second

// SessionTTL is the session token lifetime.
const SessionTTL = 24 * time.Hour

// Verification errors. ErrTokenExpired maps to 401 TOKEN_EXPIRED; every
// other failure wraps ErrTokenInvalid (401 TOKEN_INVALID).
var (
	ErrTokenInvalid = errors.New("token invalid")
	ErrTokenExpired = errors.New("token expired")
)

// Claims are the verified claims callers use.
type Claims struct {
	Subject  uuid.UUID
	Audience Audience
	// EventID is set on queue and admission tokens.
	EventID uuid.UUID
	// Ticket is the queue ticket (0 = pre-queue); queue tokens only.
	Ticket   *int64
	IssuedAt time.Time
	Expires  time.Time
	ID       string
}

// ExpiresAt is the token's expiry (UTC).
func (c Claims) ExpiresAt() time.Time { return c.Expires.UTC() }

// wireClaims is the JWT body: registered claims plus eid and tkt.
type wireClaims struct {
	jwt.RegisteredClaims
	EventID string `json:"eid,omitempty"`
	Ticket  *int64 `json:"tkt,omitempty"`
}

// Keyring signs with the first key and verifies with any key.
type Keyring struct {
	signer config.TokenKey
	byID   map[string][]byte
	now    func() time.Time
}

// NewKeyring builds a key ring. now may be nil (time.Now).
func NewKeyring(keys []config.TokenKey, now func() time.Time) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("auth: empty key ring")
	}
	if now == nil {
		now = time.Now
	}
	k := &Keyring{signer: keys[0], byID: make(map[string][]byte, len(keys)), now: now}
	for _, key := range keys {
		if key.KID == "" || len(key.Secret) < config.MinTokenKeyBytes {
			return nil, fmt.Errorf("auth: key %s is unusable", key)
		}
		k.byID[key.KID] = key.Secret
	}
	return k, nil
}

// Issue signs a token for subject with the given audience and TTL. eventID
// may be uuid.Nil (session tokens).
func (k *Keyring) Issue(aud Audience, subject, eventID uuid.UUID, ticket *int64, ttl time.Duration) (string, Claims, error) {
	now := k.now().Truncate(time.Second)
	jti, err := uuid.NewV7()
	if err != nil {
		return "", Claims{}, fmt.Errorf("auth: jti: %w", err)
	}
	wc := wireClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: Issuer, Subject: subject.String(), Audience: jwt.ClaimStrings{string(aud)},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl)), ID: jti.String(),
		},
		Ticket: ticket,
	}
	if eventID != uuid.Nil {
		wc.EventID = eventID.String()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, wc)
	tok.Header["kid"] = k.signer.KID
	s, err := tok.SignedString(k.signer.Secret)
	if err != nil {
		return "", Claims{}, fmt.Errorf("auth: sign: %w", err)
	}
	return s, Claims{Subject: subject, Audience: aud, EventID: eventID, Ticket: ticket,
		IssuedAt: now, Expires: now.Add(ttl), ID: wc.ID}, nil
}

// Verify checks the signature, issuer, audience and lifetime of token.
func (k *Keyring) Verify(token string, aud Audience) (Claims, error) {
	var wc wireClaims
	_, err := jwt.ParseWithClaims(token, &wc, k.key,
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(Issuer), jwt.WithAudience(string(aud)),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(),
		jwt.WithLeeway(ClockSkew), jwt.WithTimeFunc(k.now),
	)
	if errors.Is(err, jwt.ErrTokenExpired) {
		return Claims{}, ErrTokenExpired
	}
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrTokenInvalid, err)
	}
	sub, err := uuid.Parse(wc.Subject)
	if err != nil || sub == uuid.Nil || wc.ID == "" || wc.IssuedAt == nil {
		return Claims{}, fmt.Errorf("%w: bad sub, jti or iat", ErrTokenInvalid)
	}
	c := Claims{Subject: sub, Audience: aud, Ticket: wc.Ticket, ID: wc.ID,
		IssuedAt: wc.IssuedAt.UTC(), Expires: wc.ExpiresAt.UTC()}
	if wc.EventID != "" {
		if c.EventID, err = uuid.Parse(wc.EventID); err != nil {
			return Claims{}, fmt.Errorf("%w: bad eid", ErrTokenInvalid)
		}
	}
	return c, nil
}

func (k *Keyring) key(t *jwt.Token) (any, error) {
	kid, _ := t.Header["kid"].(string)
	if secret, ok := k.byID[kid]; ok {
		return secret, nil
	}
	return nil, errors.New("unknown kid")
}
