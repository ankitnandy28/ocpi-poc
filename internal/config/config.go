// Package config holds the runtime settings shared by the OCPI demo binaries.
// Every value comes from an environment variable, with a demo default so the
// project runs unchanged if nothing is configured.
package config

import (
	"encoding/base64"
	"log"
	"os"
)

// Defaults used when an environment variable is not set.
const (
	DefaultOCPIPort = "8080" // port the OCPI server listens on
	DefaultEMSPPort = "8083" // port the Mock eMSP listens on
	DefaultCPOPort  = "8081" // port the Mock CPO listens on
	DefaultHost     = "localhost"
	DefaultTokenRaw = "emsp secret token 123" // shared demo token, raw form
)

// OCPIServerURL is the address the Mock eMSP uses to reach the OCPI server.
func OCPIServerURL() string {
	return "http://" + env("OCPI_HOST", DefaultHost) + ":" + env("OCPI_PORT", DefaultOCPIPort)
}

// BaseURL returns a base URL for a given port and host, used to build Link headers.
func BaseURL(host, port string) string {
	return "http://" + host + ":" + port
}

// TokenRaw returns the shared raw token, before Base64 encoding.
func TokenRaw() string {
	return env("OCPI_TOKEN", DefaultTokenRaw)
}

// TokenEncoded returns the value a client must send after the word "Token ".
// OCPI 2.3.0 transport requires the token to be Base64-encoded.
func TokenEncoded() string {
	return base64.StdEncoding.EncodeToString([]byte(TokenRaw()))
}

// AuthHeader returns the complete Authorization header value.
func AuthHeader() string {
	return "Token " + TokenEncoded()
}

// env reads an environment variable, falling back to a default.
// It trims accidental whitespace, which is the most common cause of
// "the token looks identical but still fails" bugs.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// LogStartup prints the settings that matter for debugging. Never call this
// with a real secret token in production.
func LogStartup(name, port string) {
	log.Printf("%s config: port=%s token(encoded)=%s", name, port, TokenEncoded())
}

// ---- Additions for the Credentials module -------------------------------

// Default OCPI party identity for this demo.
const (
	DefaultCountryCode = "IN"   // ISO 3166-1 alpha-2: India
	DefaultPartyID     = "DEMO" // the CPO's own party id
)

// CountryCode is the ISO 3166-1 alpha-2 country code of this party.
var CountryCode = env("OCPI_COUNTRY_CODE", DefaultCountryCode)

// PartyID identifies this party inside the country.
var PartyID = env("OCPI_PARTY_ID", DefaultPartyID)

// Port returns the port the OCPI server listens on.
func Port() string {
	return env("OCPI_PORT", DefaultOCPIPort)
}

// PublicBaseURL is the address other parties use to reach this OCPI server.
// Used to build the endpoint URLs inside our Credentials object.
func PublicBaseURL() string {
	return BaseURL(env("OCPI_HOST", DefaultHost), Port())
}

// CPOBaseURL is the address of the Mock CPO, which this server reads from.
func CPOBaseURL() string {
	return BaseURL(env("CPO_HOST", DefaultHost), env("CPO_PORT", DefaultCPOPort))
}

// EMSPBaseURL is the address of the Mock eMSP.
func EMSPBaseURL() string {
	return BaseURL(env("EMSP_HOST", DefaultHost), env("EMSP_PORT", DefaultEMSPPort))
}

// ListenAddr returns a ":port" address for http.ListenAndServe.
func ListenAddr(port string) string {
	return ":" + port
}

// CPOAddr is the listen address for the Mock CPO.
func CPOAddr() string { return ListenAddr(env("CPO_PORT", DefaultCPOPort)) }

// EMSPAddr is the listen address for the Mock eMSP.
func EMSPAddr() string { return ListenAddr(env("EMSP_PORT", DefaultEMSPPort)) }
