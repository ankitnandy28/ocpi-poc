// Package credentials implements the OCPI 2.3.0 Credentials module.
//
// The handshake uses three tokens, named A, B and C:
//
//	A  - given out of band (email, portal). Used once to bootstrap.
//	B  - created by the party that registers (the Sender).
//	C  - created by the party receiving the registration (the Receiver),
//	     and used by the Sender for every request from then on.
//
// After the handshake, A is thrown away and must no longer work.
package credentials

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

// Party identifies one OCPI party, e.g. a CPO or an eMSP.
type Party struct {
	CountryCode string `json:"country_code"`
	PartyID     string `json:"party_id"`
}

// BusinessDetails is who you are, in human terms.
// Only Name is required by the spec; the rest are optional.
type BusinessDetails struct {
	Name    string `json:"name"`
	Website string `json:"website,omitempty"`
}

// Endpoint is one module URL you serve or consume.
//
// Role is "SENDER" if you provide the data, "RECEIVER" if you consume it.
type Endpoint struct {
	Identifier string `json:"identifier"`
	Role       string `json:"role"`
	URL        string `json:"url"`
}

// Version is one entry in the OCPI /versions discovery reply. Both fields are
// plain lowercase JSON on the wire; the shape is fixed by the spec:
//
//	{"data": [{"version": "2.3.0", "url": "https://.../ocpi/2.3.0"}]}
type Version struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// Credentials is the body of GET, POST and PUT on /credentials.
type Credentials struct {
	Token string            `json:"token"`
	URL   string            `json:"url"`
	Roles []CredentialsRole `json:"roles"`
}

// CredentialsRole ties one party to the endpoints it serves.
// Roles is a list because one platform can act as both CPO and eMSP.
type CredentialsRole struct {
	Role            string          `json:"role"` // CPO, EMSP, HUB, NAP, ...
	BusinessDetails BusinessDetails `json:"business_details"`
	Party           Party           `json:"party"`
	Endpoints       []Endpoint      `json:"endpoints"`
}

// Status codes from the OCPI 2.3.0 credentials module.
// Kept here so handlers read clearly.
const (
	StatusSuccess            = 1000
	StatusUnknownToken       = 2004
	StatusNotEnoughInfo      = 2002
	StatusMissingEndpoint    = 3003
	StatusUnsupportedVersion = 3002 // no matching OCPI version / not enough information
	StatusAlreadyRegistered  = 2001
)

// TokenStore remembers the other party's token in memory.
//
// In a demo, memory is fine: the token resets when the server restarts.
// A real implementation would store this in a database.
type TokenStore struct {
	mu    sync.RWMutex // mu guards everything below it
	token string       // the peer's current token (TOKEN_B or TOKEN_C)
	peer  *Credentials // the peer's details, saved from the POST body
	party Party        // which party owns the peer token
}

// NewTokenStore returns an empty store, ready to use.
func NewTokenStore() *TokenStore {
	return &TokenStore{}
}

// GenerateToken makes a random, URL-safe token.
//
// Random is better than a fixed string: nobody can guess it, and two runs
// of the server produce different tokens. 32 random bytes give 256 bits
// of entropy, which is far more than enough for a demo.
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("could not read random bytes: %w", err)
	}
	// RawURLEncoding keeps the token free of +, / and = so it is safe
	// inside a URL and inside an Authorization header.
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// SavePeer stores the token and details we received during POST or PUT.
func (s *TokenStore) SavePeer(token string, peer Credentials, party Party) error {
	if token == "" {
		return fmt.Errorf("peer token is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
	if len(peer.Roles) > 0 {
		s.peer = &peer
	}
	s.party = party
	return nil
}

// PeerToken returns the peer's token, or "" if we have not registered yet.
func (s *TokenStore) PeerToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Peer returns a copy of the peer's details, or nil if none are saved.
//
// A copy is returned so callers cannot change the stored data by accident.
func (s *TokenStore) Peer() *Credentials {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.peer == nil {
		return nil
	}
	dup := *s.peer
	dup.Roles = append([]CredentialsRole(nil), s.peer.Roles...)
	return &dup
}

// IsRegistered reports whether we already completed a handshake.
func (s *TokenStore) IsRegistered() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token != ""
}

// Clear forgets the peer. Used by DELETE /credentials.
func (s *TokenStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
	s.peer = nil
	s.party = Party{}
}

// GenerateAndRemember makes a fresh token, stores it as the peer token,
// and returns it. This is how a Receiver produces TOKEN_C during POST.
//
// Returning the same value it stores avoids a class of bug where the
// token sent back and the token remembered are different strings.
func (s *TokenStore) GenerateAndRemember(peer Credentials, party Party) (string, error) {
	tok, err := GenerateToken()
	if err != nil {
		return "", err
	}
	if err := s.SavePeer(tok, peer, party); err != nil {
		return "", err
	}
	return tok, nil
}

// ValidatePeer checks a POST or PUT body for the fields the spec requires.
//
// Deliberately strict: a real partner sends all of these, and accepting a
// half-filled body now means a confusing failure much later.
func ValidatePeer(c Credentials) error {
	if c.Token == "" {
		return fmt.Errorf("token is required")
	}
	if c.URL == "" {
		return fmt.Errorf("url is required")
	}
	if len(c.Roles) == 0 {
		return fmt.Errorf("at least one role is required")
	}
	for i, r := range c.Roles {
		if r.Role == "" {
			return fmt.Errorf("role %d: role is required", i)
		}
		if r.BusinessDetails.Name == "" {
			return fmt.Errorf("role %d: business_details.name is required", i)
		}
		if r.Party.CountryCode == "" || r.Party.PartyID == "" {
			return fmt.Errorf("role %d: party.country_code and party_id are required", i)
		}
		if len(r.Endpoints) == 0 {
			return fmt.Errorf("role %d: at least one endpoint is required", i)
		}
		for j, e := range r.Endpoints {
			if e.Identifier == "" || e.Role == "" || e.URL == "" {
				return fmt.Errorf("role %d endpoint %d: identifier, role and url are required", i, j)
			}
			if e.Role != "SENDER" && e.Role != "RECEIVER" {
				return fmt.Errorf("role %d endpoint %d: role must be SENDER or RECEIVER", i, j)
			}
		}
	}
	return nil
}

// HasEndpoint reports whether the body lists an endpoint with the given
// identifier. Used to answer with 3003 when a required module is missing.
func HasEndpoint(c Credentials, identifier string) bool {
	for _, r := range c.Roles {
		for _, e := range r.Endpoints {
			if e.Identifier == identifier {
				return true
			}
		}
	}
	return false
}

// NowUTC is exported so handlers and tests format timestamps identically.
func NowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
