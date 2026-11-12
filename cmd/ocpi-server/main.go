// ocpi server: the middle piece of this poc.
// it talks to the eMSP in ocpi format (credentials handshake, locations),
// and gets the real location data from the mock cpo.

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

//Types for the ocpi replies to be sent

// ocpiResponse is the standard ocpi wrapper. Every reply from this server uses it.
// StatusMsg and Data are left out of the json when empty,
type ocpiResponse struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_message,omitempty"`
	Timestamp  string `json:"timestamp"`
	Data       any    `json:"data,omitempty"`
}

// ocpiLocation is one location in ocpi format. This is what the emsp receives.
// it has fewer fields than the full OCPI spec.
type ocpiLocation struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Address     string     `json:"address"`
	City        string     `json:"city"`
	Country     string     `json:"country"`
	Coordinates ocpiCoords `json:"coordinates"`
	// not filled in yet (toOCPILocation never sets it), so it never appears in the reply.
	EVSEs       []any  `json:"evses,omitempty"`
	LastUpdated string `json:"last_updated"`
}

// ocpiCoords holds the position. ocpi sends latitude and longitude as text, not numbers.
type ocpiCoords struct {
	Latitude  string `json:"latitude"`
	Longitude string `json:"longitude"`
}

// cpoLocation is a location exactly as the Mock CPO sends it (GET /api/locations).
// first i read it in, then convert it to an ocpiLocation. The json names must match the cpo's json.
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

// cpoEVSE is one charging point, as the mock cpo sends it. i have read it but i will not pass it on yet.
type cpoEVSE struct {
	UID        string         `json:"uid"`
	Status     string         `json:"status"`
	Connectors []cpoConnector `json:"connectors"`
}

// cpoConnector is one plug on a charging point, as the mock cpo sends it. Also this is not passed on yet.
type cpoConnector struct {
	ID        string  `json:"id"`
	Standard  string  `json:"standard"`
	PowerType string  `json:"power_type"`
	MaxKW     float64 `json:"max_kw"`
}

// PAGING SETTINGS:

// defaultLimit: how many locations per page if the caller sends no limit.
// maxLimit: the biggest page size to be allowed.
// ocpiInvalidParams: ocpi status code for "invalid or missing parameters".
const (
	defaultLimit      = 10
	maxLimit          = 100
	ocpiInvalidParams = 2001
)

// SHARED DATA

// credStore remembers the partner that registered, like its details and its token.
// it lives in memory only, so restarting the server clears it i believe.
var credStore = credentials.NewTokenStore()

// HELPER FUNCTIONS

// writeOCPI sends an ocpiResponse as json, with the given http status code.
func writeOCPI(w http.ResponseWriter, httpStatus int, body ocpiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(body)
}

// ok sends a success reply: HTTP 200 and ocpi status "success", with data inside
func ok(w http.ResponseWriter, data any) {
	writeOCPI(w, http.StatusOK, ocpiResponse{
		StatusCode: credentials.StatusSuccess,
		Timestamp:  credentials.NowUTC(),
		Data:       data,
	})
}

// fail sends an error reply. It needs two codes: the http status (like 401)
// and the ocpi status (like 2001), plus a message that explains the problem.
func fail(w http.ResponseWriter, httpStatus, ocpiStatus int, msg string) {
	writeOCPI(w, httpStatus, ocpiResponse{
		StatusCode: ocpiStatus,
		StatusMsg:  msg,
		Timestamp:  credentials.NowUTC(),
	})
}

// requireToken wraps a handler and checks the authorization header first.
// A valid token lets the request through. Otherwise the caller gets 401 unauthorized.

// Two tokens are accepted: the bootstrap token from config,
// and the token we gave the partner during registration.

// A real ocpi server would stop accepting the bootstrap token after registration.
// i am keeping both so testing stays easy.

// the header holds the token Base64-encoded, as "Token <base64>".
// the store keeps the plain token, so we encode it before comparing.

func requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// the header, that the caller sent, like "token something".
		got := r.Header.Get("Authorization")

		// Check 1: the bootstrap token. config.AuthHeader() is already "Token <base64>", so better to compare directly.
		if got == config.AuthHeader() {
			next(w, r)
			return
		}

		// Check 2: the token i gave the partner at registration (empty if nobody has registered). First i encode it,then compare.
		if peer := credStore.PeerToken(); peer != "" {
			encoded := base64.StdEncoding.EncodeToString([]byte(peer))
			if got == "Token "+encoded {
				next(w, r)
				return
			}
		}

		// neither token matched, so reply 401 unauthorized.
		fail(w, http.StatusUnauthorized, credentials.StatusUnknownToken,
			"invalid or missing token")
	}
}

// readPaging reads ?offset=something and ?limit=something from the url.
// fefaults: offset 0 and limit 10. Values that are not valid numbers are ignored.
// the limit can never be bigger than maxLimit.
func readPaging(r *http.Request) (offset, limit int) {

	// starting with the defaults.
	offset, limit = 0, defaultLimit

	// using offset only if it is a number that is 0 or more.
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := parseInt(v); err == nil && n >= 0 {
			offset = n
		}
	}
	// will use limit only if it is a number above 0.
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parseInt(v); err == nil && n > 0 {
			limit = n
		}
	}
	// should know that never allow a page bigger than the maximum.
	if limit > maxLimit {
		limit = maxLimit
	}
	return offset, limit
}

// parseInt turns text like "25" into the number 25.
// it fails for empty text and for anything that is not only digits (so "-5" and "2.5" will fail).
// the inbuilt strconv.Atoi does a similar job. I am making this is a hand-written version.
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

// the error returned when text is not a number.
var errBadNumber = &numberError{}

// numberError is the error type behind errBadNumber.
type numberError struct{}

func (e *numberError) Error() string { return "not a number" }

// Locations: fetch from the CPO, convert, filter and page

// fetchCPOLocations asks the mock cpo for all its locations (GET /api/locations).
// then it returns them in the cpo's own format.
func fetchCPOLocations() ([]cpoLocation, error) {

	// I call the mock cpo here.
	resp, err := http.Get(config.CPOBaseURL() + "/api/locations")
	if err != nil {
		return nil, err
	}
	// closing the reply body when this function ends.
	defer resp.Body.Close()
	// next reading the json list into cpoLocation items.
	var locs []cpoLocation
	if err := json.NewDecoder(resp.Body).Decode(&locs); err != nil {
		return nil, err
	}
	return locs, nil
}

// toOCPILocation will convert a cpo location into ocpi format.
// the coordinates become text. The evse's are not copied.
func toOCPILocation(c cpoLocation) ocpiLocation {
	return ocpiLocation{
		ID:      c.ID,
		Name:    c.Name,
		Address: c.Address,
		City:    c.City,
		Country: c.Country,
		// ocpi wants latitude and longitude as text, so convert the numbers.
		Coordinates: ocpiCoords{
			Latitude:  formatFloat(c.Latitude),
			Longitude: formatFloat(c.Longitude),
		},

		// i noted that this is the current time, not the cpo's last_updated value (c.LastUpdated).
		LastUpdated: credentials.NowUTC(),
	}
}

// formatFloat turns a number like 6.9 into text with 6 decimals: "6.900000".
// ocpi sends coordinates as text. A negative number gets a "-" in front.
// it is written by hand. fmt.Sprintf("%.6f", f) does a similar job.
func formatFloat(f float64) string {

	// remember that whether the number is negative, then work with a positive one.
	neg := f < 0
	if neg {
		f = -f
	}
	// whole = the part before the point (6 in 6.9)
	whole := int64(f)
	// frac = the part after the point
	frac := int64((f - float64(whole)) * 1000000)
	// joining them with a dot. pad6 will make the decimals exactly 6 digits long.
	s := formatInt(whole) + "." + pad6(frac)

	// putting the minus sign back
	if neg {
		return "-" + s
	}
	return s
}

// formatInt will turn a whole number into text, so 69 becomes "69".
// i have written by hand for learning purpose. strconv.Itoa does the same thing.
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	// create the text from the right. Take the last digit (n%10), put it at the front, then drop it (n/10).
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// pad6 will add zeros on the left until the text is 6 characters long: so "5" become "000005".
// this keeps the decimal digits in the right place, example 0.05 gives "050000", not "50000".
func pad6(n int64) string {
	s := formatInt(n)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

// readDateRange will be reading the optional ?date_from=... and ?date_to=... from the url.
// the format is RFC 3339 UTC.
// if a date is left out, that side has no limit (the time stays at zero).
// if a date is given but badly written, ok is false and the caller replies with an error 2001.
// ignoring a bad date would return every location and look like success.
func readDateRange(r *http.Request) (from, to time.Time, ok bool) {
	// q is holding all the url query parameters.
	q := r.URL.Query()

	// date_from: if given, it must parse. Or else i give up with ok = false.
	if v := q.Get("date_from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		from = t
	}
	// date_to is following the same rule as date_from above.
	if v := q.Get("date_to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, false
		}
		to = t
	}
	return from, to, true
}

// withinDateRange says whether lastUpdated is between from and to (both ends included).
// a zero from or to means no limit on that side.
// if a filter is active and lastUpdated can't be read as a date, the location is left out.
func withinDateRange(lastUpdated string, from, to time.Time) bool {

	// there is no filter at all, so everything passes.
	if from.IsZero() && to.IsZero() {
		return true
	}
	// next i read the location's date. If that fails, i leave the location out.
	t, err := time.Parse(time.RFC3339, lastUpdated)
	if err != nil {
		return false
	}
	// too old - before date_from.
	if !from.IsZero() && t.Before(from) {
		return false
	}
	// too new - after date_to.
	if !to.IsZero() && t.After(to) {
		return false
	}
	return true
}

// listOCPILocations answers GET /ocpi/2.3.0/locations.
// Steps to do:
// 1)Get all locations from the mock cpo
// 2)Keep only those inside the date range (if given)
// 3)Cut out one page, using offset and limit
// 4)Convert that page to ocpi format and send it with the paging headers
func listOCPILocations(w http.ResponseWriter, r *http.Request) {
	all, err := fetchCPOLocations()
	if err != nil {
		// 502 bad gateway, the Mock CPO did not answer properly. 3001 is the ocpi server error code.
		fail(w, http.StatusBadGateway, 3001, "could not fetch locations from CPO")
		return
	}
	// next reading the optional date filter. okRange is false if a date is badly written.
	from, to, okRange := readDateRange(r)
	if !okRange {
		fail(w, http.StatusBadRequest, ocpiInvalidParams, "invalid date_from or date_to, expected RFC 3339 UTC like 2025-01-01T00:00:00Z")
		return
	}

	// next keeping only locations inside the date range. all[:0:0] is an empty list of the same type that shares no memory with all.
	filtered := all[:0:0]
	for _, c := range all {
		if withinDateRange(c.LastUpdated, from, to) {
			filtered = append(filtered, c)
		}
	}
	// from here on, "all" means the filtered list.
	all = filtered

	// next i will ask which page did the caller ask for?
	offset, limit := readPaging(r)
	// total = how many locations match, before paging
	total := len(all)

	// start and end mark where this page sits in the list.
	// an offset past the end gives an empty page, not an error.
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	// converting only the locations on this page.
	page := make([]ocpiLocation, 0, end-start)
	for _, c := range all[start:end] {
		page = append(page, toOCPILocation(c))
	}

	// X-Total-Count will say how many locations match in total. Link = url links of the next and previous pages.
	w.Header().Set("X-Total-Count", formatInt(int64(total)))
	w.Header().Set("Link", buildLink(r, offset, limit, total))
	ok(w, page)
}

// buildLink will build the link header with the url links of the next and previous pages.
// my example: <http://localhost:8080/ocpi/2.3.0/locations?offset=10&limit=10>; rel="next"

// it will give output "" when everything fits on one page.
// i should remember that date_from and date_to are not copied into these links.

func buildLink(r *http.Request, offset, limit, total int) string {
	// base is the address of this same endpoint, without the query part.
	base := "http://" + r.Host + r.URL.Path
	parts := []string{}

	// there is a next page if some locations come after this one.
	if offset+limit < total {
		parts = append(parts, "<"+base+"?offset="+formatInt(int64(offset+limit))+
			"&limit="+formatInt(int64(limit))+">; rel=\"next\"")
	}
	// there is a previous page if not at the start. Then need to step back one page, but not below 0.
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

//Versions & modules

// getVersions answers GET /ocpi/versions, which is the list of ocpi versions this server supports.
// it is deliberately NOT behind requireToken.
// a party that has not registered has no token yet, and this is the function needed to find out how to register.

func getVersions(w http.ResponseWriter, r *http.Request) {
	ok(w, []credentials.Version{
		{
			// using this version, and its url leads to the module list (getModules).
			Version: "2.3.0",
			URL:     config.PublicBaseURL() + "/ocpi/2.3.0",
		},
	})
}

// getModules answers GET /ocpi/2.3.0: the list of endpoints for this version.
// an emsp reads it to find out where credentials and locations live.
// Role of RECEIVER = this server accepts requests there.
// Role of SENDER = this server provides data there.

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

// token names used in the comments below (from the ocpi handshake):
// token_A = the bootstrap token the emsp starts with.
// token_B = the token the emsp gives, for me to call the eMSP.
// token_C = the new token to give to the emsp, for the emsp to call me.

// ---- Credentials -------------------------------------------------------

// ourCredentials makes the object that describes this server, which is the cpo with:
// token, address, role, company and endpoints.

// GET /credentials returns it as is. POST and PUT return it with a new token.
// to add a module later (tariffs, sessions, ...), add an entry to endpoints.
func ourCredentials() credentials.Credentials {
	return credentials.Credentials{
		// the bootstrap token, I am using 	a fixed demo value.
		Token: config.DefaultTokenRaw,
		// next this server's base address.
		URL: config.PublicBaseURL(),

		Roles: []credentials.CredentialsRole{
			{
				// this server will play the cpo role.
				Role: "CPO",
				// company details next to show to partners, i will be using these demo values.
				BusinessDetails: credentials.BusinessDetails{
					Name:    "Demo CPO India",
					Website: "https://example.com",
				},
				// next is the country_code + party_id to identify this cpo. Both will come from config.
				Party: credentials.Party{
					CountryCode: config.CountryCode,
					PartyID:     config.PartyID,
				},
				// below are the modules that this cpo offers, with their addresses.
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

// getCredentials answers GET /credentials. It shows this server's details. Also it needs a valid token.
func getCredentials(w http.ResponseWriter, r *http.Request) {
	ok(w, ourCredentials())
}

// postCredentials answers POST /credentials, which is basically first-time registration.
// if a partner is already registered, need to reply 409 with status 2001. The caller should use PUT instead.

func postCredentials(w http.ResponseWriter, r *http.Request) {
	if credStore.IsRegistered() {
		fail(w, http.StatusConflict, credentials.StatusAlreadyRegistered,
			"already registered; use PUT to rotate or DELETE to unregister")
		return
	}
	handleRegisterOrRotate(w, r, false)
}

// putCredentials answers PUT /credentials will replace the registration and gives a new token.
// if nobody is registered yet, then reply 405. The caller should use POST first.
func putCredentials(w http.ResponseWriter, r *http.Request) {
	if !credStore.IsRegistered() {
		fail(w, http.StatusMethodNotAllowed, credentials.StatusNotEnoughInfo,
			"not registered yet; use POST first")
		return
	}
	handleRegisterOrRotate(w, r, true)
}

// handleRegisterOrRotate is the shared code behind POST and PUT.
// rotating = false means first registration. rotating = true means a new token for an existing partner.
// First read the body, then check it, next check the partner offers "locations",
// then make a token for them, then reply with my credentials and that token.
func handleRegisterOrRotate(w http.ResponseWriter, r *http.Request, rotating bool) {

	// body is the partner's credentials, sent as json.
	var body credentials.Credentials
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, credentials.StatusNotEnoughInfo,
			"invalid JSON body")
		return
	}
	// next checking the required fields are present.
	if err := credentials.ValidatePeer(body); err != nil {
		fail(w, http.StatusBadRequest, credentials.StatusNotEnoughInfo, err.Error())
		return
	}

	// first need the partner to offer certain modules. Only "locations" for now.
	// then add more names to the list below if there is need for more modules.
	// if one is missing, reply with OCPI status 3003 (HTTP 200, as ocpi does for this error).

	for _, required := range []string{"locations"} {
		if !credentials.HasEndpoint(body, required) {
			fail(w, http.StatusOK, credentials.StatusMissingEndpoint,
				"peer is missing required endpoint: "+required)
			return
		}
	}

	// taking the partner's identity (country code + party ID) from its first role.
	party := credentials.Party{}
	if len(body.Roles) > 0 {
		party = body.Roles[0].Party
	}
	// next make a new token for the partner and store it with their details. This replaces any old one.
	ourToken, err := credStore.GenerateAndRemember(body, party)
	if err != nil {
		fail(w, http.StatusInternalServerError, 3001, "could not generate token")
		return
	}

	reply := ourCredentials()
	reply.Token = ourToken // token_c is used by the partner to call me.

	// using the the word used in the log message.
	verb := "registered"
	if rotating {
		verb = "rotated"
	}
	log.Printf("credentials %s with %s/%s", verb, party.CountryCode, party.PartyID)

	ok(w, reply)
}

// deleteCredentials answers DELETE /credentials. It unregisters the partner.
// doing this forgets their details and tokens. It always replies success, even if nobody was registered.
func deleteCredentials(w http.ResponseWriter, r *http.Request) {
	// next remember who it was (for the log), then need to clear the store.
	peer := credStore.Peer()
	credStore.Clear()

	// i want to log only if a partner was actually registered.
	if peer != nil && len(peer.Roles) > 0 {
		log.Printf("credentials removed for %s/%s",
			peer.Roles[0].Party.CountryCode, peer.Roles[0].Party.PartyID)
	}
	ok(w, nil)
}

// ---- Main --------------------------------------------------------------

func main() {
	// mux is the router. It decides which function handles which url.
	mux := http.NewServeMux()
	// discovery is made that no token is needed. These are the first calls a stranger makes.
	mux.HandleFunc("GET /ocpi/versions", getVersions)
	mux.HandleFunc("GET /ocpi/2.3.0", getModules)

	// locations: needs a token.
	mux.HandleFunc("GET /ocpi/2.3.0/locations", requireToken(listOCPILocations))

	// credentials (all need a token). Including "get" reads, "post" registers, "put" gets a new token, "delete" will unregister.
	mux.HandleFunc("GET /ocpi/2.3.0/credentials", requireToken(getCredentials))
	mux.HandleFunc("POST /ocpi/2.3.0/credentials", requireToken(postCredentials))
	mux.HandleFunc("PUT /ocpi/2.3.0/credentials", requireToken(putCredentials))
	mux.HandleFunc("DELETE /ocpi/2.3.0/credentials", requireToken(deleteCredentials))
	// next printing the address, then starting the server. ListenAndServe keeps running until i press Ctrl+C.
	// if it fails (example if the port is already in use), log.Fatal prints the error and exits.

	log.Printf("OCPI server running at http://localhost:%s", config.Port())
	log.Fatal(http.ListenAndServe(":"+config.Port(), mux))
}
