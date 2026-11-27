// Package auth issues and verifies SLUICE's stateless tokens (API.md §1.1)
// with golang-jwt/jwt/v5: HS256, signed with the first key of the TOKEN_KEYS
// ring and verified against any key in it by the `kid` header. Verification
// is strict: only HS256, a known kid, iss=sluice, the expected audience, and
// exp/iat checked with 5 s of clock-skew leeway.
package auth

import (
	"context"
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
	// touch, if set, runs on every request that Require or RequireLive
	// lets through with a valid admission token (the queue's sliding
	// admission lease): it renews the lease and reports its state.
	touch AdmissionCheck
	// spendGrace is how long past its exp (the admission's hard cap) an
	// admission token is still accepted, and only to finish or give back
	// what it already spent: POST /orders for the user's own live hold,
	// DELETE /holds/{id}, DELETE /queue. POST /holds answers 403
	// ADMISSION_EXPIRED inside the grace. It is HOLD_TTL: a hold taken just
	// before the cap lives that long (ADR 0018). 0 disables it.
	spendGrace time.Duration
}

// AdmissionState is what the server-side admission lease check found
// (ADR 0010 addendum part 3).
type AdmissionState int

// Admission lease states.
const (
	// AdmissionLive: the lease is live (and was renewed if it could be).
	AdmissionLive AdmissionState = iota
	// AdmissionLapsed: the admission ended (released, left, idle or past
	// the cap). The token may still verify, but the admission is over.
	AdmissionLapsed
	// AdmissionUnknown: the queue state is not initialised in Redis (it was
	// lost, R1), so only the token can decide until the queue is restored.
	AdmissionUnknown
	// AdmissionError: the check itself failed (Redis unreachable).
	AdmissionError
)

// AdmissionCheck renews the admission lease of c and reports its state.
type AdmissionCheck func(ctx context.Context, c Claims) AdmissionState

// WithAdmissionTouch returns a copy of k whose Require and RequireLive call
// f with the claims of every request that presents a valid admission token,
// before the handler runs. f must be quick.
func (k *Keyring) WithAdmissionTouch(f AdmissionCheck) *Keyring {
	c := *k
	c.touch = f
	return &c
}

// WithSpendGrace returns a copy of k that accepts admission tokens up to d
// past their exp on the routes that finish or give back an admission's
// spend (ADR 0018).
func (k *Keyring) WithSpendGrace(d time.Duration) *Keyring {
	c := *k
	c.spendGrace = max(d, 0)
	return &c
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
	return k.issue(aud, subject, eventID, ticket, now, now.Add(ttl))
}

// IssueUntil signs a token that expires at exp, floored to the second (JWT
// exp has 1 s resolution), so it never outlives the thing it stands for.
// Unlike Issue, the expiry does not depend on when the call is made: every
// token issued for the same admission carries the same exp.
func (k *Keyring) IssueUntil(aud Audience, subject, eventID uuid.UUID, ticket *int64, exp time.Time) (string, Claims, error) {
	return k.issue(aud, subject, eventID, ticket, k.now().Truncate(time.Second), exp.Truncate(time.Second))
}

func (k *Keyring) issue(aud Audience, subject, eventID uuid.UUID, ticket *int64, now, exp time.Time) (string, Claims, error) {
	jti, err := uuid.NewV7()
	if err != nil {
		return "", Claims{}, fmt.Errorf("auth: jti: %w", err)
	}
	wc := wireClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: Issuer, Subject: subject.String(), Audience: jwt.ClaimStrings{string(aud)},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp), ID: jti.String(),
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
		IssuedAt: now, Expires: exp, ID: wc.ID}, nil
}

// Verify checks the signature, issuer, audience and lifetime of token.
func (k *Keyring) Verify(token string, aud Audience) (Claims, error) {
	return k.verify(token, aud, ClockSkew)
}

// verifyPastExp verifies an admission token that Verify found expired,
// accepting it up to spendGrace past exp. ok is false outside the grace.
func (k *Keyring) verifyPastExp(token string) (Claims, bool) {
	if k.spendGrace <= 0 {
		return Claims{}, false
	}
	c, err := k.verify(token, AudAdmission, ClockSkew+k.spendGrace)
	return c, err == nil
}

func (k *Keyring) verify(token string, aud Audience, leeway time.Duration) (Claims, error) {
	var wc wireClaims
	_, err := jwt.ParseWithClaims(token, &wc, k.key,
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(Issuer), jwt.WithAudience(string(aud)),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(),
		jwt.WithLeeway(leeway), jwt.WithTimeFunc(k.now),
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

// Subject verifies token's signature, issuer and lifetime, whatever its
// audience, and returns its subject. Rate limiters use it to key by user
// before the route's own Require runs.
func (k *Keyring) Subject(token string) (uuid.UUID, bool) {
	var wc wireClaims
	_, err := jwt.ParseWithClaims(token, &wc, k.key,
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(Issuer), jwt.WithExpirationRequired(),
		jwt.WithLeeway(ClockSkew), jwt.WithTimeFunc(k.now),
	)
	if err != nil {
		return uuid.Nil, false
	}
	sub, err := uuid.Parse(wc.Subject)
	return sub, err == nil && sub != uuid.Nil
}
