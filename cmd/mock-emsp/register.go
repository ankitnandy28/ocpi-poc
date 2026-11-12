// Registration: the Mock eMSP performs the OCPI credentials handshake so it
// can call the CPO's API with a token of its own.
//
// Four calls, in this order:
//  1. GET  {base}/ocpi/2.3.0/versions            -> find the module list
//  2. GET  {base}/ocpi/2.3.0/credentials         -> read the CPO's details
//  3. POST {base}/ocpi/2.3.0/credentials         -> send our details
//  4. GET  {base}/ocpi/2.3.0/locations           -> prove the token works
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"example.com/ocpi-poc/internal/config"
	"example.com/ocpi-poc/internal/credentials"
)

// credentialsStatusAlreadyRegistered is this CPO's signal that a peer is
// already registered. OCPI 2.3.0 has no dedicated code for this, so the CPO
// uses 2001 with HTTP 409. Both sides read this one constant, so they cannot
// drift apart.

const credentialsStatusAlreadyRegistered = credentials.StatusAlreadyRegistered

// wire types for the four calls. Kept local to this file: the eMSP must speak
// the OCPI wire format, but does not need to share the CPO's internal types.
type versionsReply struct {
	Status     string    `json:"status"`
	StatusCode int       `json:"status_code"`
	Data       []version `json:"data"`
}

type version struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// moduleOcpiReply is for endpoints whose data is a single object — e.g.
// GET /ocpi/2.3.0, which returns {version, endpoints}.
type moduleOcpiReply struct {
	StatusCode int             `json:"status_code"`
	Status     string          `json:"status_message,omitempty"`
	Timestamp  string          `json:"timestamp,omitempty"`
	Data       endpointModules `json:"data"`
}

// moduleListReply is for endpoints whose data is an ARRAY — e.g.
// GET /ocpi/2.3.0/locations, which returns a list of Location objects.
type moduleListReply struct {
	StatusCode int               `json:"status_code"`
	Status     string            `json:"status_message,omitempty"`
	Timestamp  string            `json:"timestamp,omitempty"`
	Data       []json.RawMessage `json:"data"`
}

type endpointModules struct {
	Token       string     `json:"token"`
	URL         string     `json:"url"`
	Roles       []role     `json:"roles"`
	Endpoints   []endpoint `json:"endpoints"`
	CountryCode string     `json:"country_code"`
	PartyID     string     `json:"party_id"`
}

type role struct {
	Role      string        `json:"role"`
	Party     partyIdentity `json:"party"`
	Token     string        `json:"token"`
	Endpoints []endpoint    `json:"endpoints"`
}

type endpoint struct {
	Identifier string `json:"identifier"`
	URL        string `json:"url"`
}

type partyIdentity struct {
	CountryCode string `json:"country_code"`
	PartyID     string `json:"party_id"`
}

// credentialsReply is the answer to both GET and POST /credentials.
type credentialsReply struct {
	Status     string          `json:"status"`
	StatusCode int             `json:"status_code"`
	Data       endpointModules `json:"data"`
}

// knownEndpoints holds the bootstrap and registered tokens plus the base URL,
// so later steps can call the CPO without re-reading config.
type knownEndpoints struct {
	BaseURL       string
	BootstrapTok  string // what we send before registering
	RegisteredTok string // what the CPO gave us; valid after step 3
	LocationsURL  string // from the CPO's module list
}

// registerWith runs the full handshake against the OCPI server.
// It returns once the peer token works, or an error naming the failed step.
func registerWith(baseURL, bootstrapToken string) (knownEndpoints, error) {
	out := knownEndpoints{BaseURL: baseURL, BootstrapTok: bootstrapToken}
	client := &http.Client{Timeout: 10 * time.Second}

	// ---- step 1: which versions does the CPO offer? ----------------------
	// NOTE: the versions list lives at {base}/ocpi/versions, NOT under the
	// version prefix. /ocpi/2.3.0/versions is a 404 on every OCPI server.
	var vr versionsReply
	if err := getJSON(client, baseURL+"/ocpi/versions", bootstrapToken, &vr); err != nil {
		return out, fmt.Errorf("step 1, GET /ocpi/versions: %w", err)
	}
	if vr.StatusCode != 1000 {
		return out, fmt.Errorf("step 1, GET /ocpi/versions: OCPI status %d (%s)", vr.StatusCode, vr.Status)
	}
	var moduleBase string
	for _, v := range vr.Data {
		if v.Version == "2.3.0" {
			moduleBase = v.URL
			break
		}
	}
	if moduleBase == "" {
		return out, fmt.Errorf("step 1: server does not offer version 2.3.0")
	}

	// ---- step 2: read the module list for 2.3.0 --------------------------
	// This is where the credentials and locations endpoints are advertised.
	type moduleList struct {
		Status     string `json:"status"`
		StatusCode int    `json:"status_code"`
		Data       struct {
			Endpoints []struct {
				Identifier string `json:"identifier"`
				URL        string `json:"url"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	var ml moduleList
	if err := getJSON(client, moduleBase, bootstrapToken, &ml); err != nil {
		return out, fmt.Errorf("step 2, GET %s: %w", moduleBase, err)
	}
	if ml.StatusCode != 1000 {
		return out, fmt.Errorf("step 2, GET module list: OCPI status %d (%s)", ml.StatusCode, ml.Status)
	}
	var credentialsURL string
	for _, e := range ml.Data.Endpoints {
		switch e.Identifier {
		case "credentials":
			credentialsURL = e.URL
		case "locations":
			out.LocationsURL = e.URL
		}
	}
	if credentialsURL == "" {
		return out, fmt.Errorf("step 2: CPO did not advertise a credentials endpoint")
	}
	if out.LocationsURL == "" {
		return out, fmt.Errorf("step 2: CPO did not advertise a locations endpoint")
	}

	// ---- step 3: read the CPO's credentials with the bootstrap token -----
	var cr credentialsReply
	if err := getJSON(client, credentialsURL, bootstrapToken, &cr); err != nil {
		return out, fmt.Errorf("step 3, GET credentials: %w", err)
	}
	if cr.StatusCode != 1000 {
		return out, fmt.Errorf("step 3, GET credentials: OCPI status %d (%s)", cr.StatusCode, cr.Status)
	}

	// ---- step 4: send our details, receive the registered token ----------
	ourBody := map[string]any{
		"token": "TOKEN_B",
		"url":   config.EMSPBaseURL(),
		"roles": []map[string]any{{
			"role": "EMSP",
			"business_details": map[string]any{
				"name": "Mock eMSP (demo)",
			},
			"party": map[string]any{
				"country_code": "IN",
				"party_id":     "EMSP",
			},
			// The CPO checks for this and answers 3003 (missing endpoint)
			// if the registering party advertises no locations endpoint.
			"endpoints": []map[string]any{{
				"identifier": "locations",
				"role":       "RECEIVER",
				"url":        config.EMSPBaseURL() + "/ocpi/2.3.0/Locations",
			}},
		}},
	}

	// ---- step 4: register, or rotate if the CPO already knows us ---------
	// POST is not idempotent here: a second POST while registered returns
	// 409 with 2001, and we rotate with PUT instead.

	// ("already registered; use PUT to rotate or DELETE to unregister").
	// Both replies are OCPI envelopes on the wire, so we check status_code.
	var postReply credentialsReply
	errPost := postJSON(client, credentialsURL, bootstrapToken, ourBody, &postReply)

	switch {
	case errPost == nil && postReply.StatusCode == 1000:
		out.RegisteredTok = postReply.Data.Token

	case errPost == nil && postReply.StatusCode == credentialsStatusAlreadyRegistered:
		// Cold-start retry: we are registered from a previous run. Rotate.
		var putReply credentialsReply
		if err := putJSON(client, credentialsURL, bootstrapToken, ourBody, &putReply); err != nil {
			return out, fmt.Errorf("step 4, PUT credentials: %w", err)
		}
		if putReply.StatusCode != 1000 {
			return out, fmt.Errorf("step 4, PUT credentials: OCPI status %d (%s)",
				putReply.StatusCode, putReply.Status)
		}
		out.RegisteredTok = putReply.Data.Token

	case errPost != nil:
		return out, fmt.Errorf("step 4, POST credentials: %w", errPost)

	default:
		return out, fmt.Errorf("step 4, POST credentials: OCPI status %d (%s)",
			postReply.StatusCode, postReply.Status)
	}

	if out.RegisteredTok == "" {
		return out, fmt.Errorf("step 4: CPO returned an empty token")
	}

	// ---- step 5: prove the registered token actually works ---------------
	var lr moduleListReply
	if err := getJSON(client, out.LocationsURL, out.RegisteredTok, &lr); err != nil {
		return out, fmt.Errorf("step 5, GET locations as registered peer: %w", err)
	}
	if lr.StatusCode != 1000 {
		return out, fmt.Errorf("step 5: registered token rejected, OCPI status %d (%s)", lr.StatusCode, lr.Status)
	}

	return out, nil
}

// ---- small HTTP helpers -------------------------------------------------

// getJSON sends a GET with an OCPI Authorization header.
//
// IMPORTANT: rawToken is the plain token (e.g. config.TokenRaw()). OCPI 2.3.0
// requires the header value to be "Token " + Base64(rawToken). Encoding here,
// once, means no caller can get it wrong.
func getJSON(c *http.Client, url, rawToken string, dst any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(rawToken))
	return doJSON(c, req, dst)
}

// postJSON sends a POST with a JSON body and an OCPI Authorization header.
func postJSON(c *http.Client, url, rawToken string, body, dst any) error {
	return sendJSON(c, http.MethodPost, url, rawToken, body, dst)
}

// putJSON sends a PUT. Used to rotate the token when the CPO already knows us.
func putJSON(c *http.Client, url, rawToken string, body, dst any) error {
	return sendJSON(c, http.MethodPut, url, rawToken, body, dst)
}

func sendJSON(c *http.Client, method, url, rawToken string, body, dst any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request body: %w", err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	return doJSON(c, req, dst)
}

// doJSON sends the request and decodes the reply, turning a non-2xx HTTP
// status into an error that includes the body. Without this, a 401 from the
// CPO would look like "invalid JSON" and send you hunting in the wrong place.
func doJSON(c *http.Client, req *http.Request, dst any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Decode the OCPI envelope whatever the HTTP status: OCPI errors are
	// structured (2001, 2002, 3003 ...) and the caller needs status_code to
	// act on them. Only fail here if the body is not JSON at all.
	if resp.StatusCode == http.StatusNotFound && len(raw) > 0 && raw[0] != '{' {
		return fmt.Errorf("HTTP 404 from %s %s: is the route registered? (body: %s)",
			req.Method, req.URL.Path, truncate(string(raw), 120))
	}

	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("HTTP %d from %s %s: not an OCPI reply: %w (body: %s)",
			resp.StatusCode, req.Method, req.URL.Path, err, truncate(string(raw), 200))
	}
	return nil
}

// EncodeToken turns a raw token into the Base64 form the Authorization header
// needs. Put it here so the eMSP never has to think about the encoding again.
type EncodeToken string

func (t EncodeToken) String() string {
	return "Token " + base64.StdEncoding.EncodeToString([]byte(string(t)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
