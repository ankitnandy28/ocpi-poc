// package main starting the Mock eMSP server.
// an eMSP provides EV drivers with access to charging stations
// and handling billing, subscriptions, and roaming.
// this mock simulates an eMSP for testing the OCPI workflow.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	httpSwagger "github.com/swaggo/http-swagger"

	_ "example.com/ocpi-poc/docs/emsp"

	"example.com/ocpi-poc/internal/config"
)

// @title           Mock eMSP API
// @version         1.0
// @description     Fetches charging locations through the OCPI server.
// @host            localhost:8083
// @BasePath        /

type ocpiLocationsReply struct {
	StatusCode    int              `json:"status_code"`
	StatusMessage string           `json:"status_message"`
	Data          []map[string]any `json:"data"`
	Timestamp     string           `json:"timestamp"`
}

type emspReply struct {
	ReceivedFrom  string             `json:"received_from"`
	LocationCount int                `json:"location_count"`
	LocationIDs   []string           `json:"location_ids"`
	OCPIReply     ocpiLocationsReply `json:"ocpi_reply"`
}

type errorReply struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Could not write response: %v", err)
	}
}

// Step 1 of discovery: ask which OCPI versions are supported.
func discoverVersions() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(config.CPOBaseURL() + "/ocpi/versions")
	if err != nil {
		return "", fmt.Errorf("versions request failed: %w", err)
	}
	defer resp.Body.Close()

	var reply struct {
		Data []struct {
			Version string `json:"version"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return "", fmt.Errorf("invalid versions reply: %w", err)
	}
	if len(reply.Data) == 0 {
		return "", fmt.Errorf("no OCPI versions offered")
	}

	log.Printf("Discovered version %s at %s", reply.Data[0].Version, reply.Data[0].URL)
	return reply.Data[0].URL, nil
}

// Step 2 of discovery: ask which module endpoints exist (e.g. locations).
func discoverLocationsURL(versionURL string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(versionURL)
	if err != nil {
		return "", fmt.Errorf("version details request failed: %w", err)
	}
	defer resp.Body.Close()

	var reply struct {
		Data struct {
			Endpoints []struct {
				Identifier string `json:"identifier"`
				URL        string `json:"url"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return "", fmt.Errorf("invalid version details reply: %w", err)
	}

	for _, ep := range reply.Data.Endpoints {
		if ep.Identifier == "locations" {
			log.Printf("Discovered locations endpoint: %s", ep.URL)
			return ep.URL, nil
		}
	}
	return "", fmt.Errorf("locations endpoint not offered")
}

// authHeader builds the OCPI Authorization header value.
// OCPI 2.3.0 requires "Token " followed by the Base64-encoded token.
func authHeader(rawToken string) string {
	return "Token " + base64.StdEncoding.EncodeToString([]byte(rawToken))
}

// parseNextLink pulls the rel="next" URL out of an RFC 5988 Link header.
// Returns "" when there is no next page.
func parseNextLink(linkHeader string) string {
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start >= 0 && end > start {
			return part[start+1 : end]
		}
	}
	return ""
}

// Step 3: use the discovered URL to fetch locations.
func fetchOCPILocations() (*ocpiLocationsReply, string, error) {
	versionURL, err := discoverVersions()
	if err != nil {
		return nil, "", err
	}

	locationsURL, err := discoverLocationsURL(versionURL)
	if err != nil {
		return nil, "", err
	}

	client := &http.Client{Timeout: 10 * time.Second}

	merged := &ocpiLocationsReply{StatusCode: 1000, StatusMessage: "Success"}
	nextURL := locationsURL
	pageCount := 0

	// Keep fetching while the server offers a rel="next" link.
	for nextURL != "" && pageCount < 50 {
		pageCount++

		req, err := http.NewRequest("GET", nextURL, nil)
		if err != nil {
			return nil, "", fmt.Errorf("could not build request: %w", err)
		}
		// Prefer the token the handshake gave us; fall back to the
		// bootstrap token only before registration has happened.
		token := emspState.RegisteredTok
		if token == "" {
			token = config.TokenRaw()
		}
		req.Header.Set("Authorization", authHeader(token))
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("locations request failed: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, "", fmt.Errorf("locations endpoint returned HTTP %d", resp.StatusCode)
		}

		var page ocpiLocationsReply
		err = json.NewDecoder(resp.Body).Decode(&page)
		if err != nil {
			resp.Body.Close()
			return nil, "", fmt.Errorf("invalid locations reply: %w", err)
		}

		total := resp.Header.Get("X-Total-Count")
		linkHeader := resp.Header.Get("Link")
		next := parseNextLink(linkHeader)
		resp.Body.Close()

		if page.StatusCode != 1000 {
			return nil, "", fmt.Errorf("OCPI error %d: %s", page.StatusCode, page.StatusMessage)
		}

		log.Printf("Page %d: %d locations (X-Total-Count=%s)", pageCount, len(page.Data), total)

		merged.Data = append(merged.Data, page.Data...)
		merged.Timestamp = page.Timestamp

		nextURL = next
	}

	if nextURL != "" {
		return nil, "", fmt.Errorf("pagination did not finish after %d pages", pageCount)
	}

	return merged, locationsURL, nil
}

// getLocations godoc
// @Summary      Get locations through the OCPI server
// @Description  Calls the OCPI server and returns the locations received.
// @Tags         emsp-locations
// @Produce      json
// @Success      200 {object} emspReply
// @Failure      502 {object} errorReply
// @Failure      500 {object} errorReply
// @Router       /emsp/locations [get]
func getLocations(w http.ResponseWriter, r *http.Request) {
	reply, locationsURL, err := fetchOCPILocations()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorReply{
			Error: err.Error(),
		})
		return
	}

	ids := []string{}
	for _, location := range reply.Data {
		if id, ok := location["id"].(string); ok {
			ids = append(ids, id)
		}
	}

	log.Printf(
		"eMSP received %d locations: %v",
		len(reply.Data),
		ids,
	)

	writeJSON(w, http.StatusOK, emspReply{
		ReceivedFrom:  locationsURL,
		LocationCount: len(reply.Data),
		LocationIDs:   ids,
		OCPIReply:     *reply,
	})
}

// emspState holds what the handshake taught us, so handlers can read it.
// Package level, not inside main(), because /emsp/state is a different
// function and a local variable would be invisible to it.
var emspState knownEndpoints

func main() {
	mux := http.NewServeMux()

	// Match only the root path, not every unknown URL.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Mock eMSP is running"))
	})

	mux.HandleFunc("GET /emsp/locations", getLocations)
	mux.Handle("GET /swagger/", httpSwagger.WrapHandler)

	// A tiny inspector so you can see the registered state without logs.
	mux.HandleFunc("GET /emsp/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"base_url":         emspState.BaseURL,
			"bootstrap_token":  emspState.BootstrapTok,
			"registered_token": emspState.RegisteredTok,
			"locations_url":    emspState.LocationsURL,
		})
	})

	log.Println("Mock eMSP running at " + config.EMSPBaseURL())
	log.Println("Swagger: " + config.EMSPBaseURL() + "/swagger/index.html")

	// ---- registration with the CPO (Option 1: every start) -------------
	var err error
	emspState, err = registerWith(config.OCPIServerURL(), config.TokenRaw())
	if err != nil {
		// One retry: on a cold start the OCPI server may need a moment.
		log.Printf("mock-emsp: registration failed (%v); retrying in 2s", err)
		time.Sleep(2 * time.Second)

		emspState, err = registerWith(config.OCPIServerURL(), config.TokenRaw())
		if err != nil {
			log.Fatalf("mock-emsp: registration failed after retry: %v", err)
		}
	}

	log.Printf("mock-emsp: registered with CPO")
	log.Printf("mock-emsp:   bootstrap token: %s", emspState.BootstrapTok)
	log.Printf("mock-emsp:   registered token: %s", emspState.RegisteredTok)
	log.Printf("mock-emsp:   locations URL:    %s", emspState.LocationsURL)

	log.Fatal(http.ListenAndServe(config.EMSPAddr(), mux))
}
