package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	httpSwagger "github.com/swaggo/http-swagger"

	_ "example.com/ocpi-poc/docs/cpo" // swagger files made by "swag init". Tis symbol _ means to import it only so it registers itself.

	"example.com/ocpi-poc/internal/config"
)

// @title           Mock CPO API
// @version         1.0
// @description     Fake charging station data for the OCPI learning project.
// @host            localhost:8081
// @BasePath        /

// connector is one physical plug on a charging point, like a Type 2 or CCS2 plug.
// I understood thaat standard = plug type, PowerType = AC or DC, MaxKW = top power in kilowatts.
type Connector struct {
	ID        string  `json:"id"`
	Standard  string  `json:"standard"`
	PowerType string  `json:"power_type"`
	MaxKW     float64 `json:"max_kw"`
}

// evse is one charging point: the unit that charges one car at a time.
// it can have several connectors, but only one can be used at a time.
type EVSE struct {
	UID        string      `json:"uid"`
	Status     string      `json:"status"`
	Connectors []Connector `json:"connectors"`
}

// location is a charging station site with one or more EVSEs. LastUpdated is text in RFC 3339 format, like 2025-01-01T00:00:00Z.
type Location struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Address     string  `json:"address"`
	City        string  `json:"city"`
	Country     string  `json:"country"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	EVSEs       []EVSE  `json:"evses"`
	LastUpdated string  `json:"last_updated"`
}

type errorReply struct {
	Error string `json:"error"`
}

// assuming these names for the first 25 sites.
// If n is bigger than this list, the extra sites are named "Site 26", "Site 27",....

func buildLocations(n int) []Location {
	// Display names for the first 25 sites. Extra sites fall back to "Site NN".
	names := []string{
		"Koramangala Fast Charge Hub", "Whitefield Tech Park Charger", "Hebbal Flyover Hub",
		"Yelahanka New Town", "Indiranagar 100 Feet Road", "Jayanagar 4th Block",
		"Electronic City Phase 1", "Marathahalli Bridge", "Banashankari 2nd Stage",
		"Rajajinagar Metro", "Malleshwaram 8th Cross", "Yeshwanthpur Junction",
		"Bellandur Lake View", "Sarjapur Road Hub", "HSR Layout Sector 2",
		"BTM Layout 2nd Stage", "JP Nagar 6th Phase", "Basavanagudi Circle",
		"Domlur Flyover", "Ulsoor Lake Side", "Hennur Main Road",
		"Bommanahalli Industrial", "Kengeri Satellite Town", "Devanahalli Airport Road",
		"Bhartiya City Nikoo Homes",
	}
	// starting with an empty list that has room for n items, so Go does not need to resize it.
	out := make([]Location, 0, n)
	// starting time for LastUpdated: 2025-01-01 00:00 UTC.
	// each location is 12 hours later than the previous one, so the dates are all different.
	// this lets a date filter return only some locations instead of all or none.
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < n; i++ {
		// using "site 01", "site 02", ... by default. Then I replace it with the real name if the list has one.
		name := fmt.Sprintf("Site %02d", i+1)
		if i < len(names) {
			name = names[i]
		}

		out = append(out, Location{
			ID:      fmt.Sprintf("LOC%02d", i+1), // %02d is adding a leading zero, so 1 becomes LOC01
			Name:    name,
			Address: fmt.Sprintf("%d Demo Street", 10+i),
			City:    "Bengaluru",
			Country: "IND",
			// adding fake coordinates near Bengaluru. I am taking each site is 0.01 degrees (around 1 km) from the previous one.
			Latitude:  12.900000 + float64(i)*0.01,
			Longitude: 77.500000 + float64(i)*0.01,
			// every location gets two charging points: A is free and B is busy, so both statuses appear in the fake data.
			EVSEs: []EVSE{
				{
					UID:    fmt.Sprintf("EVSE-%03d-A", i+1),
					Status: "AVAILABLE",
					Connectors: []Connector{
						{ID: "1", Standard: "IEC_62196_T2", PowerType: "AC_3_PHASE", MaxKW: 22},
					},
				},
				{
					UID:    fmt.Sprintf("EVSE-%03d-B", i+1),
					Status: "CHARGING",
					Connectors: []Connector{
						{ID: "1", Standard: "IEC_62196_T2_COMBO", PowerType: "DC", MaxKW: 60},
					},
				},
			},
			// First location = base time, second = base + 12 hours, and so on. Written as RFC 3339 text.
			LastUpdated: base.Add(time.Duration(i) * 12 * time.Hour).Format(time.RFC3339),
		})
	}
	return out
}

// all the fake locations. They are built once when the program starts, and both API handlers below read from this list.
var locations = buildLocations(25)

// writeJSON sends data back to the caller as JSON, with an HTTP status code (200, 404, ...)
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// listLocations godoc
// @Summary      List all locations
// @Description  Returns every charging location the CPO knows about
// @Tags         locations
// @Produce      json
// @Param        date_from query string false "Accepted but not applied yet; all locations are returned. Intended format: RFC 3339, for example 2025-01-01T00:00:00Z"
// @Param        date_to   query string false "Accepted but not applied yet; all locations are returned. Intended format: RFC 3339, for example 2025-01-01T00:00:00Z"

// @Success      200 {array} Location
// @Router       /api/locations [get]
func listLocations(w http.ResponseWriter, r *http.Request) {
	// read ?date_from=... and ?date_to=... from the url. Both are empty text if the caller leaves them out.
	dateFrom := r.URL.Query().Get("date_from")
	dateTo := r.URL.Query().Get("date_to")
	// for now I only print the dates in this window. Not filtering by them yet, so every call returns all 25 locations.
	fmt.Printf("listLocations: date_from=%q date_to=%q\n", dateFrom, dateTo)
	writeJSON(w, http.StatusOK, locations)
}

// getLocation godoc
// @Summary      Get one location
// @Description  Returns one location by its ID
// @Tags         locations
// @Produce      json
// @Param        id path string true "Location ID, for example LOC01"
// @Success      200 {object} Location
// @Failure      404 {object} errorReply
// @Router       /api/locations/{id} [get]
func getLocation(w http.ResponseWriter, r *http.Request) {
	// read {id} from the url. For /api/locations/LOC01, id is "LOC01".
	id := r.PathValue("id")

	// i am checking each location. Then return the first one whose ID matches and stop.
	for _, loc := range locations {
		if loc.ID == id {
			writeJSON(w, http.StatusOK, loc)
			return
		}
	}
	// if the loop found no match, I send a 404 (Not Found) error.
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "location not found"})
}

func main() {

	// I understood that mux is the router. It decides which function handles which URL.
	mux := http.NewServeMux()
	// Home page this is. I can open it in a browser to check that the server is up.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Mock CPO is running"))
	})
	// "GET" means only get requests match. {id} is a placeholder that matches any value like LOC01.
	mux.HandleFunc("GET /api/locations", listLocations)
	mux.HandleFunc("GET /api/locations/{id}", getLocation)
	// this is swagger web page. cpo must match the instance name used when the docs were generated.
	mux.Handle("GET /swagger/", httpSwagger.Handler(
		httpSwagger.InstanceName("cpo"),
	))

	// first print the addresses, then start the server. ListenAndServe keeps running until I press Ctrl+C.
	// if it fails (for example if the port is already in use), log.Fatal prints the error and exits.

	log.Println("Mock CPO running at http://localhost:8081")
	log.Println("Swagger page at http://localhost:8081/swagger/index.html")
	log.Fatal(http.ListenAndServe(config.CPOAddr(), mux))
}
