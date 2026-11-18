// Mock eMSP: it will be a fake e-mobility service provider used to test the ocpi workflow.
// a real eMSP gives ev drivers access to charging stations and handles their billing and subscriptions.
// this mock asks the ocpi server for charging locations, the way a real eMSP would.

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

	_ "example.com/ocpi-poc/docs/emsp" // swagger files made by "swag init". _ means importing only so it registers itself.

	"example.com/ocpi-poc/internal/config"
)

// @title           Mock eMSP API
// @version         1.0
// @description     Fetches charging locations through the OCPI server.
// @host            localhost:8083
// @BasePath        /

// ocpiLocationsReply is the standard ocpi wrapper around a list of locations.
// ocpi wraps every reply in these four fields.
type ocpiLocationsReply struct {
	// 1000 means success in OCPI, any other number is error.
	StatusCode    int    `json:"status_code"`
	StatusMessage string `json:"status_message"`
	// The locations. I consider each one as generic map, so no need to model every ocpi field.
	Data []map[string]any `json:"data"`
	// when the CPO created this reply, as text.
	Timestamp string `json:"timestamp"`
}

// emspReply is what this emsp sends back to its own caller. It is a short summary plus the full ocpi reply.
type emspReply struct {
	// the url the locations were fetched from.
	ReceivedFrom string `json:"received_from"`
	// how many locations received.
	LocationCount int `json:"location_count"`
	// just the IDs of those locations for a quick check.
	LocationIDs []string `json:"location_ids"`
	// the complete ocpi reply, which is unchanged.
	OCPIReply ocpiLocationsReply `json:"ocpi_reply"`
}

// errorReply is the json body sent back when something fails, like {"error": "..."}

type errorReply struct {
	Error string `json:"error"`
}

// writeJSON sends data back as json with the given http status code like 200, 502,...
// if the json encoding fails, it logs the error instead of crashing.

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Could not write response: %v", err)
	}
}

// Step 1 of discovery is to ask the cpo which ocpi versions it supports.
func discoverVersions() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	// requesting versions from ocpi server
	resp, err := client.Get(config.CPOBaseURL() + "/ocpi/versions")
	if err != nil {
		return "", fmt.Errorf("versions request failed: %w", err)
	}
	defer resp.Body.Close()

	// the part of the reply which is needed: a list of versions, each with a url.
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

	// need to using the first version offered. Its url leads to the list of endpoints.
	log.Printf("Discovered version %s at %s", reply.Data[0].Version, reply.Data[0].URL)
	return reply.Data[0].URL, nil
}

// Step 2 of discovery is to open the version details and find the "locations" endpoint.
func discoverLocationsURL(versionURL string) (string, error) {
	// same 10-second limit as before.
	client := &http.Client{Timeout: 10 * time.Second}
	// fetch the list of endpoints for this version like credentials, locations, etc
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
	// look for the endpoint whose identifier is "locations" and return its url.
	for _, ep := range reply.Data.Endpoints {
		if ep.Identifier == "locations" {
			log.Printf("Discovered locations endpoint: %s", ep.URL)
			return ep.URL, nil
		}
	}
	return "", fmt.Errorf("locations endpoint not offered")
}

// authHeader builds the value for the Authorization header that ocpi requests need.
// this poc sends "Token....." followed by the base64 encoded token.
// I check the OCPI spec for the version before relying on this.
func authHeader(rawToken string) string {
	return "Token " + base64.StdEncoding.EncodeToString([]byte(rawToken))
}

// parseNextLink pulls the rel="next" url out of an RFC 5988 link header.
// returns "" when there is no next page.

// example header: <https://cpo/locations?offset=50>; rel="next", <https://cpo/locations?offset=100>; rel="last"
func parseNextLink(linkHeader string) string {
	// each comma separated part is one link. I want the one marked rel="next".
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, `rel="next"`) {
			continue
		}

		// the url sits between < and >
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start >= 0 && end > start {
			return part[start+1 : end]
		}
	}
	return ""
}

// Step 3 is to fetch every page of locations from the CPO, following the "next" links.
func fetchOCPILocations() (*ocpiLocationsReply, string, error) {
	// i did steps 1 and 2 to find url of the locations endpoint.
	versionURL, err := discoverVersions()
	if err != nil {
		return nil, "", err
	}
	locationsURL, err := discoverLocationsURL(versionURL)
	if err != nil {
		return nil, "", err
	}

	// client for the page requests. It is same 10-second limit.
	client := &http.Client{Timeout: 10 * time.Second}

	merged := &ocpiLocationsReply{StatusCode: 1000, StatusMessage: "Success"}
	nextURL := locationsURL
	pageCount := 0
	// keep fetching pages while server offers a rel="next" link.
	// stop after 50 pages as a safety net against a server that loops forever.

	for nextURL != "" && pageCount < 50 {
		pageCount++

		// making request for the current page.
		req, err := http.NewRequest("GET", nextURL, nil)
		if err != nil {
			return nil, "", fmt.Errorf("could not build request: %w", err)
		}
		// using  the token the handshake gave.
		// fall back to the bootstrap token only if the handshake has not run yet.

		token := emspState.RegisteredTok
		if token == "" {
			token = config.TokenRaw()
		}
		req.Header.Set("Authorization", authHeader(token))
		// sending the request. err is set only if the network call itself failed, not for http errors.
		resp, err := client.Do(req)
		if err != nil {
			return nil, "", fmt.Errorf("locations request failed: %w", err)
		}

		// any reply other than 200 (for example 401 or 500) means something is wrong. Then i stop and report it.
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, "", fmt.Errorf("locations endpoint returned HTTP %d", resp.StatusCode)
		}

		// read json body into a page of locations.
		var page ocpiLocationsReply
		err = json.NewDecoder(resp.Body).Decode(&page)
		if err != nil {
			resp.Body.Close()
			return nil, "", fmt.Errorf("invalid locations reply: %w", err)
		}

		// X-Total-Count is header with total number of locations. Only need to log it.
		total := resp.Header.Get("X-Total-Count")
		linkHeader := resp.Header.Get("Link")
		// i find the link to the next page. It is "" on the last page.
		next := parseNextLink(linkHeader)
		resp.Body.Close()

		// ocpi status 1000 means success. Any other code is an error from the CPO, even if http status was 200.

		if page.StatusCode != 1000 {
			return nil, "", fmt.Errorf("OCPI error %d: %s", page.StatusCode, page.StatusMessage)
		}

		// now i show progress in the log.
		log.Printf("Page %d: %d locations (X-Total-Count=%s)", pageCount, len(page.Data), total)
		// adding this page's locations to the combined list.
		merged.Data = append(merged.Data, page.Data...)
		// keep the timestamp from the last page read.
		merged.Timestamp = page.Timestamp
		// move on to the next page. An empty value ends loop.
		nextURL = next
	}
	// if a next link is still pending, it means loop stopped at the 50 page limit.
	if nextURL != "" {
		return nil, "", fmt.Errorf("pagination did not finish after %d pages", pageCount)
	}
	// i return all the locations combined plus the url they came from.
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
	// fetch all the locations from the cpo.
	reply, locationsURL, err := fetchOCPILocations()
	if err != nil {
		// 502 (bad geteway) means this emsp got a bad answer from the upstream server, the ocpi server.
		writeJSON(w, http.StatusBadGateway, errorReply{
			Error: err.Error(),
		})
		return
	}

	// i want to collect just the ids for a short summary.
	// starting from an empty list (not nil) makes json show [] instead of null when there are none.

	ids := []string{}
	for _, location := range reply.Data {
		// each location is a generic map. Take its "id" if it is text.
		if id, ok := location["id"].(string); ok {
			ids = append(ids, id)
		}
	}

	// I log the count and the ids for checking.
	log.Printf(
		"eMSP received %d locations: %v",
		len(reply.Data),
		ids,
	)

	// send summary and the full ocpi reply back to the caller.
	writeJSON(w, http.StatusOK, emspReply{
		ReceivedFrom:  locationsURL,
		LocationCount: len(reply.Data),
		LocationIDs:   ids,
		OCPIReply:     *reply,
	})
}

// emspState stores what the registration handshake learned, like tokens and urls.
// it is package level, not inside main(), so handlers like /emsp/state can read it.
var emspState knownEndpoints

func main() {
	// mux is the router which decides which function handles which url.
	mux := http.NewServeMux()
	// the endpoint that returns locations received through the ocpi server.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Mock eMSP is running"))
	})

	mux.HandleFunc("GET /emsp/locations", getLocations)
	// swagger web page
	mux.Handle("GET /swagger/", httpSwagger.WrapHandler)

	// inspector page shows the tokens and URLs from registration, as json. Helped me for debugging.
	// noting that it is left out of swagger.

	mux.HandleFunc("GET /emsp/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"base_url":         emspState.BaseURL,
			"bootstrap_token":  emspState.BootstrapTok,
			"registered_token": emspState.RegisteredTok,
			"locations_url":    emspState.LocationsURL,
		})
	})

	// printing the addresses before the handshake starts
	log.Println("Mock eMSP running at " + config.EMSPBaseURL())
	log.Println("Swagger: " + config.EMSPBaseURL() + "/swagger/index.html")

	// REGISTRATION WITH THE OCPI SERVER. THIS RUNS EVERY TIME THE PROGRAM STARTS.

	// err holds result of the registration attempt
	var err error
	emspState, err = registerWith(config.OCPIServerURL(), config.TokenRaw())
	if err != nil {
		// one retry: on a cold start the ocpi server may need a moment.
		// i am showing what the handshake gave us.
		log.Printf("mock-emsp: registration failed (%v); retrying in 2s", err)
		time.Sleep(2 * time.Second)

		emspState, err = registerWith(config.OCPIServerURL(), config.TokenRaw())
		if err != nil {
			// start serving http. This line keeps running until i press Ctrl+C.
			log.Fatalf("mock-emsp: registration failed after retry: %v", err)
		}
	}

	log.Printf("mock-emsp: registered with CPO")
	log.Printf("mock-emsp:   bootstrap token: %s", emspState.BootstrapTok)
	log.Printf("mock-emsp:   registered token: %s", emspState.RegisteredTok)
	log.Printf("mock-emsp:   locations URL:    %s", emspState.LocationsURL)

	log.Fatal(http.ListenAndServe(config.EMSPAddr(), mux))
}
