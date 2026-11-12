// Registration: the Mock emsp does the ocpi "credentials handshake" with the ocpi server.
// The handshake swaps tokens, so the emsp can later call the api with a token of its own.
//
// Five calls totally, in this order:
//  1. GET  {base}/ocpi/versions - find the module list for version 2.3.0
//  2. GET  {module list URL} - find the credentials and locations URLs
//  3. GET  {credentials URL} - read the CPO's details
//  4. POST {credentials URL} - send our details and get our own token back
//                             (if already registered, PUT is used instead)
//  5. GET  {locations URL} - prove the new token works

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"example.com/ocpi-poc/internal/config"
	"example.com/ocpi-poc/internal/credentials"
)

// credentialsStatusAlreadyRegistered is the code the cpo returns (with HTTP 409)
// when we try to register but are already registered.
// ocpi has no standard code for this, so this project uses 2001.
// the value comes from the credentials package, so the cpo and the emsp always use the same number.
const credentialsStatusAlreadyRegistered = credentials.StatusAlreadyRegistered

// types for the json we send and receive during the handshake.
// they live in this file on purpose: the emsp only has to match the ocpi json format,
// the emsp must speak the ocpi wire format, but does not need to share the cpo's internal types.

// versionsReply is the answer to GET /ocpi/versions: the list of ocpi versions the cpo supports.
type versionsReply struct {
	Status     string    `json:"status"`
	StatusCode int       `json:"status_code"`
	Data       []version `json:"data"`
}

// version is one supported version, with the url of its module list.
type version struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// moduleListReply is for replies where "data" is a list, like the locations from
// GET /ocpi/2.3.0/locations. json.RawMessage keeps each item as raw json, because of not reading the details here.
type moduleListReply struct {
	StatusCode int               `json:"status_code"`
	Status     string            `json:"status_message,omitempty"`
	Timestamp  string            `json:"timestamp,omitempty"`
	Data       []json.RawMessage `json:"data"`
}

// endpointModules is the "data" part of the credentials and modulelist replies.
// not every field is filled in every time. A field is empty if that reply does not include it.
type endpointModules struct {
	Token       string     `json:"token"`
	URL         string     `json:"url"`
	Roles       []role     `json:"roles"`
	Endpoints   []endpoint `json:"endpoints"`
	CountryCode string     `json:"country_code"`
	PartyID     string     `json:"party_id"`
}

// role says what a party does in ocpi (here "emsp" in this code) and who the party is.
type role struct {
	Role      string        `json:"role"`
	Party     partyIdentity `json:"party"`
	Token     string        `json:"token"`
	Endpoints []endpoint    `json:"endpoints"`
}

// endpoint is one named API address, like identifier "locations" with its url.
type endpoint struct {
	Identifier string `json:"identifier"`
	URL        string `json:"url"`
}

// partyIdentity is a company's id in ocpi: a country code plus a party ID, like IN + EMSP.
type partyIdentity struct {
	CountryCode string `json:"country_code"`
	PartyID     string `json:"party_id"`
}

// credentialsReply is the answer to get, post and put on the /credentials endpoint
type credentialsReply struct {
	Status     string          `json:"status"`
	StatusCode int             `json:"status_code"`
	Data       endpointModules `json:"data"`
}

// knownEndpoints stores what the handshake learned: the base URL, both tokens and the locations url.
// later i add code (such as fetching locations) that reads it instead of re-reading the config.
type knownEndpoints struct {
	BaseURL       string
	BootstrapTok  string // what we send before registering
	RegisteredTok string // what the CPO gave us; valid after step 3
	LocationsURL  string // from the CPO's module list
}

// registerWith runs the whole handshake against the ocpi server.
// on success it returns a filled in knownEndpoints.
// on failure it returns an error that names the step that failed, like example "step 3, GET credentials".
func registerWith(baseURL, bootstrapToken string) (knownEndpoints, error) {
	// starting the result with what is already known. The other fields are filled in as the steps succeed.
	out := knownEndpoints{BaseURL: baseURL, BootstrapTok: bootstrapToken}
	// giving up on any request after 10 seconds, so a stuck server cannot hang the program.
	client := &http.Client{Timeout: 10 * time.Second}

	// Step 1: ask which ocpi versions the cpo offers

	// NOTE: the versions list is at {base}/ocpi/versions, nut under a version number.
	// {base}/ocpi/2.3.0/versions will give 404 error.

	// fetching the list and decoding the json reply into vr.
	var vr versionsReply
	if err := getJSON(client, baseURL+"/ocpi/versions", bootstrapToken, &vr); err != nil {
		return out, fmt.Errorf("step 1, GET /ocpi/versions: %w", err)
	}
	// ocpi status 1000 means success. Any other number is an error.
	if vr.StatusCode != 1000 {
		return out, fmt.Errorf("step 1, GET /ocpi/versions: OCPI status %d (%s)", vr.StatusCode, vr.Status)
	}
	// find version 2.3.0 in the list. Its url leads to the module list (step 2).
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

	// Step 2: read the module list for this version
	// the module list tells us the addresses of the credentials and locations endpoints.

	// only the part of the reply we need,including a list of endpoints, each with a name and a url
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
	// fetch the module list and then decoding it into ml.
	var ml moduleList
	if err := getJSON(client, moduleBase, bootstrapToken, &ml); err != nil {
		return out, fmt.Errorf("step 2, GET %s: %w", moduleBase, err)
	}
	if ml.StatusCode != 1000 {
		return out, fmt.Errorf("step 2, GET module list: OCPI status %d (%s)", ml.StatusCode, ml.Status)
	}
	var credentialsURL string
	// need to pick out the two addresses, by their names.
	for _, e := range ml.Data.Endpoints {
		switch e.Identifier {
		case "credentials":
			credentialsURL = e.URL
		case "locations":
			out.LocationsURL = e.URL
		}
	}
	// Stop early, with a clear message, if either address is missing.
	if credentialsURL == "" {
		return out, fmt.Errorf("step 2: CPO did not advertise a credentials endpoint")
	}
	if out.LocationsURL == "" {
		return out, fmt.Errorf("step 2: CPO did not advertise a locations endpoint")
	}

	// Step 3: reading cpo's credentials, using the bootstrap token
	// this checks that the bootstrap token is accepted. We don't use the details yet, only the status.

	var cr credentialsReply
	if err := getJSON(client, credentialsURL, bootstrapToken, &cr); err != nil {
		return out, fmt.Errorf("step 3, GET credentials: %w", err)
	}
	if cr.StatusCode != 1000 {
		return out, fmt.Errorf("step 3, GET credentials: OCPI status %d (%s)", cr.StatusCode, cr.Status)
	}

	// Step 4: create the details about us to be sent
	ourBody := map[string]any{
		"token": "TOKEN_B",
		// own base address.
		"url": config.EMSPBaseURL(),
		// say that we are an EMSP. country_code + party_id identify us (IN + EMSP).
		"roles": []map[string]any{{
			"role": "EMSP",
			"business_details": map[string]any{
				"name": "Mock eMSP (demo)",
			},
			"party": map[string]any{
				"country_code": "IN",
				"party_id":     "EMSP",
			},
			// cpo checks if mentioning a locations endpoint.
			// it answers with status 3003 if not mentioning (missing endpoint).

			"endpoints": []map[string]any{{
				"identifier": "locations",
				"role":       "RECEIVER",
				"url":        config.EMSPBaseURL() + "/ocpi/2.3.0/Locations",
			}},
		}},
	}

	// Register with POST, or get a new token with PUT if already registered
	// i try POST first. If already registered (from an earlier run), cpo replies with HTTP 409 and status 2001.
	// then i send PUT instead. The replies are ocpi json, so we check status_code, not just the http status.

	var postReply credentialsReply
	errPost := postJSON(client, credentialsURL, bootstrapToken, ourBody, &postReply)

	switch {
	// first time registration worked. Now save token that the cpo gave.
	case errPost == nil && postReply.StatusCode == 1000:
		out.RegisteredTok = postReply.Data.Token

	case errPost == nil && postReply.StatusCode == credentialsStatusAlreadyRegistered:
		// Since already registered from an earlier run, i send PUT to get a fresh token.
		var putReply credentialsReply
		if err := putJSON(client, credentialsURL, bootstrapToken, ourBody, &putReply); err != nil {
			return out, fmt.Errorf("step 4, PUT credentials: %w", err)
		}
		if putReply.StatusCode != 1000 {
			return out, fmt.Errorf("step 4, PUT credentials: OCPI status %d (%s)",
				putReply.StatusCode, putReply.Status)
		}
		out.RegisteredTok = putReply.Data.Token

	// the POST call itself failed (for example a network error, or a reply that is not json)
	case errPost != nil:
		return out, fmt.Errorf("step 4, post credentials: %w", errPost)

	// the POST got an ocpi status which is not expected. So better to report it.
	default:
		return out, fmt.Errorf("step 4, post credentials: ocpi status %d (%s)",
			postReply.StatusCode, postReply.Status)
	}

	// a "success" reply without a token is useless, so i will treat it as an error.
	if out.RegisteredTok == "" {
		return out, fmt.Errorf("step 4: cpo returned an empty token")
	}

	// Step 5 is to check that the new token works.
	// call the locations endpoint with the registered token (must know that not the bootstrap one).
	var lr moduleListReply
	if err := getJSON(client, out.LocationsURL, out.RegisteredTok, &lr); err != nil {
		return out, fmt.Errorf("step 5, GET locations as registered peer: %w", err)
	}
	if lr.StatusCode != 1000 {
		return out, fmt.Errorf("step 5: registered token rejected, OCPI status %d (%s)", lr.StatusCode, lr.Status)
	}

	// all five steps passed, "out" now holds both tokens and the locations url.
	return out, nil
}

// Small helper functions for sending requests

// getJSON sends a GET request and decodes the json reply into dst.
// important to know that i have to pass the plain token (for example config.TokenRaw()), not an encoded one.
// authHeader (in main.go) turns it into "Token " + Base64(token).
// know that encoding in one place means no caller can forget it.

func getJSON(c *http.Client, url, rawToken string, dst any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(rawToken))
	return doJSON(c, req, dst)
}

// postJSON sends a POST request with a json body and decodes the reply into dst.
func postJSON(c *http.Client, url, rawToken string, body, dst any) error {
	return sendJSON(c, http.MethodPost, url, rawToken, body, dst)
}

// putJSON sends a PUT request. Used to get a new token when the cpo already knows us.
func putJSON(c *http.Client, url, rawToken string, body, dst any) error {
	return sendJSON(c, http.MethodPut, url, rawToken, body, dst)
}

// sendJSON is the shared code behind postJSON and putJSON.
// it turns body into json, adds the headers, sends the request and decodes the reply into dst.
func sendJSON(c *http.Client, method, url, rawToken string, body, dst any) error {
	// turning the Go value into json text.
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding request body: %w", err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(rawToken))
	// telling server that the body is json.
	req.Header.Set("Content-Type", "application/json")
	return doJSON(c, req, dst)
}

// doJSON sends the request and decodes the json reply into dst.
// it does not treat 4_ _ or 5_ _ as errors, because ocpi errors arrive as json
// (2001, 3003, ...) and the caller reads status_code to decide what to do.
// it fails only if the reply is not json. The error then shows the http status and the start of the body

func doJSON(c *http.Client, req *http.Request, dst any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	// better to close the reply body when this function ends.
	defer resp.Body.Close()

	// read the whole body, so i can decode it and also show it in an error message.
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// a 404 whose body is not json (such as "page not found") usually means the route is not registered on the server.
	// need to give a clear message for this case.
	if resp.StatusCode == http.StatusNotFound && len(raw) > 0 && raw[0] != '{' {
		return fmt.Errorf("http 404 from %s %s: is the route registered? (body: %s)",
			req.Method, req.URL.Path, truncate(string(raw), 120))
	}

	// decoding the json into dst. This fails only if the body is not valid json.
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("http %d from %s %s: not an ocpi reply: %w (body: %s)",
			resp.StatusCode, req.Method, req.URL.Path, err, truncate(string(raw), 200))
	}
	return nil
}

// truncate cuts s down to at most n characters and adds "......" if it was cut.
// i know that it is used to keep error messages short.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
