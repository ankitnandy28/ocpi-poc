package main

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"example.com/ocpi-poc/internal/config"
	"example.com/ocpi-poc/internal/credentials"
)

// ---- OCPI types --------------------------------------------------------

type ocpiResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_message,omitempty"`
	Timestamp  string `json:"timestamp"`
	Data       any    `json:"data,omitempty"`
}

type ocpiLocation struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Address     string     `json:"address"`
	City        string     `json:"city"`
	Country     string     `json:"country"`
	Coordinates ocpiCoords `json:"coordinates"`
	EVSEs       []any      `json:"evses,omitempty"`
	LastUpdated string     `json:"last_updated"`
}

type ocpiCoords struct {
	Latitude  string `json:"latitude"`
	Longitude string `json:"longitude"`
}

// cpoLocation mirrors the JSON the Mock CPO sends on /api/locations.
type cpoLocation struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	City        string    `json:"city"`
	Country     string    `json:"country"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	EVSEs       []cpoEVSE `json:"evses"`
	LastUpdated string    `json:"last_updated"`
}

type cpoEVSE struct {
	UID        string         `json:"uid"`
	Status     string         `json:"status"`
	Connectors []cpoConnector `json:"connectors"`
}

type cpoConnector struct {
	ID        string  `json:"id"`
	Standard  string  `json:"standard"`
	PowerType string  `json:"power_type"`
	MaxKW     float64 `json:"max_kw"`
}

// ---- Paging ------------------------------------------------------------

const (
	defaultLimit      = 10
	maxLimit          = 100
	ocpiInvalidParams = 2001
)

// ---- Globals -----------------------------------------------------------

var credStore = credentials.NewTokenStore()

// ---- Helpers -----------------------------------------------------------

func writeOCPI(w http.ResponseWriter, httpStatus int, body ocpiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(body)
}

func ok(w http.ResponseWriter, data any) {
	writeOCPI(w, http.StatusOK, ocpiResponse{
		StatusCode: credentials.StatusSuccess,
		Timestamp:  credentials.NowUTC(),
		Data:       data,
	})
}

func fail(w http.ResponseWriter, httpStatus, ocpiStatus int, msg string) {
	writeOCPI(w, httpStatus, ocpiResponse{
		StatusCode: ocpiStatus,
		StatusMsg:  msg,
		Timestamp:  credentials.NowUTC(),
	})
}

// requireToken accepts either the bootstrap token from config OR the peer
// token learned during the credentials handshake. Real OCPI drops the
// bootstrap token once registered; we keep both valid so testing stays easy.
//
// Tokens are compared in their ENCODED form, because that is what arrives in
// the header. The store keeps the raw token, so encode it here — the same way
// the eMSP encoded it when sending.
func requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")

		// Bootstrap token: config.AuthHeader() is already "Token <base64>".
		if got == config.AuthHeader() {
			next(w, r)
			return
		}

		// Peer token: the store holds the raw form, so encode before comparing.
		if peer := credStore.PeerToken(); peer != "" {
			encoded := base64.StdEncoding.EncodeToString([]byte(peer))
			if got == "Token "+encoded {
				next(w, r)
				return
			}
		}

		fail(w, http.StatusUnauthorized, credentials.StatusUnknownToken,
			"invalid or missing token")
	}
}

func readPaging(r *http.Request) (offset, limit int) {
	offset, limit = 0, defaultLimit

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := parseInt(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return offset, limit
}

// parseInt is a tiny wrapper so handlers stay readable and tests can stub it.
func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errBadNumber
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadNumber
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

var errBadNumber = &numberError{}

type numberError struct{}

func (e *numberError) Error() string { return "not a number" }

// ---- Locations ---------------------------------------------------------

func fetchCPOLocations() ([]cpoLocation, error) {
	resp, err := http.Get(config.CPOBaseURL() + "/api/locations")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var locs []cpoLocation
	if err := json.NewDecoder(resp.Body).Decode(&locs); err != nil {
		return nil, err
	}
	return locs, nil
}

func toOCPILocation(c cpoLocation) ocpiLocation {
	return ocpiLocation{
		ID:      c.ID,
		Name:    c.Name,
		Address: c.Address,
		City:    c.City,
		Country: c.Country,
		Coordinates: ocpiCoords{
			Latitude:  formatFloat(c.Latitude),
			Longitude: formatFloat(c.Longitude),
		},
		LastUpdated: credentials.NowUTC(),
	}
}

// formatFloat prints a float with 6 decimals, which is what OCPI expects
// for coordinates (it stores them as strings, not numbers).
func formatFloat(f float64) string {
	neg := f < 0
	if neg {
		f = -f
	}
	whole := int64(f)
	frac := int64((f - float64(whole)) * 1000000)
	s := formatInt(whole) + "." + pad6(frac)
	if neg {
		return "-" + s
	}
	return s
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func pad6(n int64) string {
	s := formatInt(n)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

// readDateRange reads OCPI date_from / date_to (RFC 3339 UTC, e.g.
// 2025-01-01T00:00:00Z). Empty means "no bound". A present but malformed
// value returns ok=false so the caller can answer 2003, as OCPI requires —
// silently ignoring it would return every location and look like success.
func readDateRange(r *http.Request) (from, to time.Time, ok bool) {
	q := r.URL.Query()

	if v := q.Get("date_from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		from = t
	}
	if v := q.Get("date_to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		to = t
	}
	return from, to, true
}

// withinDateRange reports whether lastUpdated falls inside [from, to].
// A zero from or to means that end is unbounded. A location whose
// last_updated cannot be parsed is excluded when a filter is active.
func withinDateRange(lastUpdated string, from, to time.Time) bool {
	if from.IsZero() && to.IsZero() {
		return true
	}
	t, err := time.Parse(time.RFC3339, lastUpdated)
	if err != nil {
		return false
	}
	if !from.IsZero() && t.Before(from) {
		return false
	}
	if !to.IsZero() && t.After(to) {
		return false
	}
	return true
}

func listOCPILocations(w http.ResponseWriter, r *http.Request) {
	all, err := fetchCPOLocations()
	if err != nil {
		fail(w, http.StatusBadGateway, 3001, "could not fetch locations from CPO")
		return
	}
	from, to, okRange := readDateRange(r)
	if !okRange {
		fail(w, http.StatusBadRequest, ocpiInvalidParams, "invalid date_from or date_to, expected RFC 3339 UTC like 2025-01-01T00:00:00Z")
		return
	}

	filtered := all[:0:0]
	for _, c := range all {
		if withinDateRange(c.LastUpdated, from, to) {
			filtered = append(filtered, c)
		}
	}
	all = filtered

	offset, limit := readPaging(r)
	total := len(all)

	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	page := make([]ocpiLocation, 0, end-start)
	for _, c := range all[start:end] {
		page = append(page, toOCPILocation(c))
	}

	w.Header().Set("X-Total-Count", formatInt(int64(total)))
	w.Header().Set("Link", buildLink(r, offset, limit, total))
	ok(w, page)
}

func buildLink(r *http.Request, offset, limit, total int) string {
	base := "http://" + r.Host + r.URL.Path
	parts := []string{}

	if offset+limit < total {
		parts = append(parts, "<"+base+"?offset="+formatInt(int64(offset+limit))+
			"&limit="+formatInt(int64(limit))+">; rel=\"next\"")
	}
	if offset > 0 {
		prev := offset - limit
		if prev < 0 {
			prev = 0
		}
		parts = append(parts, "<"+base+"?offset="+formatInt(int64(prev))+
			"&limit="+formatInt(int64(limit))+">; rel=\"prev\"")
	}
	return strings.Join(parts, ", ")
}

// ---- Versions & modules ------------------------------------------------

// getVersions answers the OCPI discovery call. It is deliberately NOT behind
// requireToken: a party that has not registered yet has no token, and this is
// the call it needs to find out how to register.
func getVersions(w http.ResponseWriter, r *http.Request) {
	ok(w, []credentials.Version{
		{
			Version: "2.3.0",
			URL:     config.PublicBaseURL() + "/ocpi/2.3.0",
		},
	})
}

// getModules lists the endpoints available under /ocpi/2.3.0. An eMSP reads
// this to discover where credentials and locations live.
func getModules(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{
		"version": "2.3.0",
		"endpoints": []credentials.Endpoint{
			{
				Identifier: "credentials",
				Role:       "RECEIVER",
				URL:        config.PublicBaseURL() + "/ocpi/2.3.0/credentials",
			},
			{
				Identifier: "locations",
				Role:       "SENDER",
				URL:        config.PublicBaseURL() + "/ocpi/2.3.0/locations",
			},
		},
	})
}

// ---- Credentials -------------------------------------------------------

// ourCredentials builds the object we advertise at GET /credentials.
// The endpoint list grows as you add modules (tariffs, sessions, ...).
func ourCredentials() credentials.Credentials {
	return credentials.Credentials{
		Token: config.DefaultTokenRaw,
		URL:   config.PublicBaseURL(),
		Roles: []credentials.CredentialsRole{
			{
				Role: "CPO",
				BusinessDetails: credentials.BusinessDetails{
					Name:    "Demo CPO India",
					Website: "https://example.com",
				},
				Party: credentials.Party{
					CountryCode: config.CountryCode,
					PartyID:     config.PartyID,
				},
				Endpoints: []credentials.Endpoint{
					{
						Identifier: "locations",
						Role:       "SENDER",
						URL:        config.PublicBaseURL() + "/ocpi/2.3.0/locations",
					},
					{
						Identifier: "credentials",
						Role:       "RECEIVER",
						URL:        config.PublicBaseURL() + "/ocpi/2.3.0/credentials",
					},
				},
			},
		},
	}
}

func getCredentials(w http.ResponseWriter, r *http.Request) {
	ok(w, ourCredentials())
}

func postCredentials(w http.ResponseWriter, r *http.Request) {
	if credStore.IsRegistered() {
		fail(w, http.StatusConflict, credentials.StatusAlreadyRegistered,
			"already registered; use PUT to rotate or DELETE to unregister")
		return
	}
	handleRegisterOrRotate(w, r, false)
}

func putCredentials(w http.ResponseWriter, r *http.Request) {
	if !credStore.IsRegistered() {
		fail(w, http.StatusMethodNotAllowed, credentials.StatusNotEnoughInfo,
			"not registered yet; use POST first")
		return
	}
	handleRegisterOrRotate(w, r, true)
}

func handleRegisterOrRotate(w http.ResponseWriter, r *http.Request, rotating bool) {
	var body credentials.Credentials
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, credentials.StatusNotEnoughInfo,
			"invalid JSON body")
		return
	}
	if err := credentials.ValidatePeer(body); err != nil {
		fail(w, http.StatusBadRequest, credentials.StatusNotEnoughInfo, err.Error())
		return
	}

	// A receiver must be able to reach the modules it depends on.
	// The peer's credentials must list the modules we depend on.

	for _, required := range []string{"locations"} {
		if !credentials.HasEndpoint(body, required) {
			fail(w, http.StatusOK, credentials.StatusMissingEndpoint,
				"peer is missing required endpoint: "+required)
			return
		}
	}

	party := credentials.Party{}
	if len(body.Roles) > 0 {
		party = body.Roles[0].Party
	}

	ourToken, err := credStore.GenerateAndRemember(body, party)
	if err != nil {
		fail(w, http.StatusInternalServerError, 3001, "could not generate token")
		return
	}

	reply := ourCredentials()
	reply.Token = ourToken // this is TOKEN_C from our side's view

	verb := "registered"
	if rotating {
		verb = "rotated"
	}
	log.Printf("credentials %s with %s/%s", verb, party.CountryCode, party.PartyID)

	ok(w, reply)
}

func deleteCredentials(w http.ResponseWriter, r *http.Request) {
	peer := credStore.Peer()
	credStore.Clear()
	if peer != nil && len(peer.Roles) > 0 {
		log.Printf("credentials removed for %s/%s",
			peer.Roles[0].Party.CountryCode, peer.Roles[0].Party.PartyID)
	}
	ok(w, nil)
}

// ---- Main --------------------------------------------------------------

func main() {
	mux := http.NewServeMux()
	// Discovery. No token required: these are what a stranger calls first.
	mux.HandleFunc("GET /ocpi/versions", getVersions)
	mux.HandleFunc("GET /ocpi/2.3.0", getModules)
	mux.HandleFunc("GET /ocpi/2.3.0/locations", requireToken(listOCPILocations))

	mux.HandleFunc("GET /ocpi/2.3.0/credentials", requireToken(getCredentials))
	mux.HandleFunc("POST /ocpi/2.3.0/credentials", requireToken(postCredentials))
	mux.HandleFunc("PUT /ocpi/2.3.0/credentials", requireToken(putCredentials))
	mux.HandleFunc("DELETE /ocpi/2.3.0/credentials", requireToken(deleteCredentials))

	log.Printf("OCPI server running at http://localhost:%s", config.Port())
	log.Fatal(http.ListenAndServe(":"+config.Port(), mux))
}
