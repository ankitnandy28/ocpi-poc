// package config holds the settings shared by all three programs (ocpi server, mock cpo and mock emsp).
// each setting can be changed with an environment variable.
// if the variable is not set, a demo default is used so that the project will run without any setup.

// NOTING: environment variables like OCPI_HOST, OCPI_PORT, CPO_HOST, CPO_PORT, EMSP_HOST, EMSP_PORT,
// OCPI_TOKEN, OCPI_COUNTRY_CODE, OCPI_PARTY_ID.

package config

import (
	"encoding/base64"
	"log"
	"os"
)

// default values. Each one is used when its environment variable is not set.
const (
	DefaultOCPIPort = "8080"                  // port of the ocpi server
	DefaultEMSPPort = "8083"                  // port of the mock emsp
	DefaultCPOPort  = "8081"                  // port of mock cpo
	DefaultHost     = "localhost"             //all programs run on this computer
	DefaultTokenRaw = "emsp secret token 123" // demo token, plain text (not encoded). Used by all programs
)

// OCPIServerURL is address of ocpi server, like example http://localhost:8080.
// the mock emsp uses it to reach the ocpi server.
func OCPIServerURL() string {
	return "http://" + env("OCPI_HOST", DefaultHost) + ":" + env("OCPI_PORT", DefaultOCPIPort)
}

// baseurl joins a host and a port into a web address.
// The other url functions in this file use it.
func BaseURL(host, port string) string {
	return "http://" + host + ":" + port
}

// tokenraw returns the demo token as plain text, before base64 encoding.
// it come from ocpi token, or DefaultTokenRaw if that is not set.
func TokenRaw() string {
	return env("OCPI_TOKEN", DefaultTokenRaw)
}

// tokenencoded returns the token in base64 form. This is what comes after the word "Token....." in authorization header.
// i want to always send the token base64-encoded.
func TokenEncoded() string {
	return base64.StdEncoding.EncodeToString([]byte(TokenRaw()))
}

// authheader will return the full authorization header value. It is "Token.... " followed by the encoded token.
func AuthHeader() string {
	return "Token " + TokenEncoded()
}

// env will read an environment variable. If it is not set or if it is empty, function will return fallback instead.
// Example: env("OCPI_PORT", "8080") gives "8080" unless OCPI_PORT is set.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// logStartup will print the port and the encoded token, to help with debugging.
// I SHOULD KNOW THAT: Base64 is not encryption,anyone can decode it. So better never use this with a real secret token.
func LogStartup(name, port string) {

	// name is the program's name, for example "ocpi server", so i can tell the log lines apart.
	log.Printf("%s config: port=%s token(encoded)=%s", name, port, TokenEncoded())
}

// party identity and server addresses used by the credentials handshake

// i use default identity of the cpo in this demo. I know that Ocpi identifies a company by a country code plus a party ID.
const (
	DefaultCountryCode = "IN"   // India, as a 2 letter iso country code
	DefaultPartyID     = "DEMO" // the cpo's own party id
)

// countryCode is the 2 letter country code of the cpo ("IN")
// it is read once when this program starts.
var CountryCode = env("OCPI_COUNTRY_CODE", DefaultCountryCode)

// party is the cpo's id inside its country. Together with countrycode, it identifies the cpo.
// it is read once also when the programme starts.
var PartyID = env("OCPI_PARTY_ID", DefaultPartyID)

// port function will return the port number of ocpi server in form of text (here "8080").
func Port() string {
	return env("OCPI_PORT", DefaultOCPIPort)
}

// publicbaseurl function is the address other programs use to reach the ocpi server.
// the ocpi server puts it inside the endpoint url links it implements like versions, credentials, locations.
func PublicBaseURL() string {
	return BaseURL(env("OCPI_HOST", DefaultHost), Port())
}

// CPOBaseURL is address of the mock cpo, here example http://localhost:8081.
// the ocpi server reads the fake locations from it. The mock emsp uses it to find the ocpi versions.
func CPOBaseURL() string {
	return BaseURL(env("CPO_HOST", DefaultHost), env("CPO_PORT", DefaultCPOPort))
}

// EMSPBaseURL is the address of mock emsp, here example http://localhost:8083.
// the emsp sends it to the ocpi server during registration.
func EMSPBaseURL() string {
	return BaseURL(env("EMSP_HOST", DefaultHost), env("EMSP_PORT", DefaultEMSPPort))
}

// ListenAddr turns a port into the form http.ListenAndServe needs. Example here "8080" becomes ":8080"
// a leading ":" means something like "listen on this port on all network interfaces of this computer".
func ListenAddr(port string) string {
	return ":" + port
}

// CPOAddr is where the Mock CPO listens, here it is example ":8081".
func CPOAddr() string { return ListenAddr(env("CPO_PORT", DefaultCPOPort)) }

// EMSPAddr is where the Mock eMSP listens, for example ":8083".
func EMSPAddr() string { return ListenAddr(env("EMSP_PORT", DefaultEMSPPort)) }
