// Tests for the ocpi server. They cover two things:
// One is requireToken: which tokens are accepted and which are rejected
// Two is paging: offset, limit and the next/prev links
// i will run them with go test ./...
package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"example.com/ocpi-poc/internal/config"
	"example.com/ocpi-poc/internal/credentials"
)

// tests for requireToken (the token check)

// below function is a request with the correct bootstrap token, it must reach the inner handler and get 200.
func TestRequireToken_CorrectTokenPasses(t *testing.T) {
	// a simple inner handler that replies 200.  This 200 is only seen if requireToken lets the request through.
	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status_code":1000}`))
	})

	// will create a fake request. Nothing is sent over the network.
	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	// the correct header: "Token...." plus the base64 encoded bootstrap token.
	req.Header.Set("Authorization", config.AuthHeader())
	rec := httptest.NewRecorder() // rec records the reply, so i can check it afterwards.

	handler(rec, req) // calling the handler directly.

	if rec.Code != http.StatusOK {
		// Fatalf stops this test straight away and will print this message.
		t.Fatalf("want 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// a request with no authorization header must get 401 and an ocpi error body.
func TestRequireToken_MissingToken(t *testing.T) {
	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		// if this line runs, requireToken will let the request through and so the test fails.
		t.Fatal("inner handler must NOT be called without a token")
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil) // no Authorization header on purpose.
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}

	var reply ocpiResponse // also i am checking the json body, not only the http status.
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("reply is not valid JSON: %v", err)
	}

	// will check the ocpi status code in the body.
	// credentials.StatusUnknownToken is the code this project sends for a missing or invalid token.
	if reply.StatusCode != credentials.StatusUnknownToken {
		t.Errorf("want OCPI status 2001, got %d", reply.StatusCode)
	}
}

func TestRequireToken_WrongToken(t *testing.T) { // a made-up token must get 401, and the inner handler must not run.
	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		// if this line runs, the wrong token got through, so the test fails.
		t.Fatal("inner handler must NOT be called with a wrong token")
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)

	// a token the server has never seen.
	req.Header.Set("Authorization", "Token definitely-not-the-token")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

// the plain not encoded token must be rejected. The server only accepts the base64 encoded form.
func TestRequireToken_RawTokenIsRejected(t *testing.T) {
	// the inner handler would reply 200, so a 200 here will mean the plain token was wrongly accepted.
	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", "Token "+config.TokenRaw()) // plain token, NOT base64 encoded
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("raw token must be rejected; got %d", rec.Code)
	}
}

// below function will test for readPaging (offset and limit from the url)

// i am checking readPaging against many url queries. Each case below is one row.
func TestReadPaging(t *testing.T) {

	// below is a table of test cases. Each row has a name, the URL query text,
	// ,and the offset and limit expected from readPaging to return.

	cases := []struct {
		name       string
		query      string
		wantOffset int
		wantLimit  int
	}{
		{"no params uses defaults", "", 0, defaultLimit},
		{"offset only", "?offset=20", 20, defaultLimit},
		{"limit only", "?limit=5", 0, 5},
		{"both", "?offset=10&limit=5", 10, 5},
		{"limit above max is capped", "?limit=5000", 0, maxLimit},
		{"negative offset ignored", "?offset=-3", 0, defaultLimit},
		{"zero limit ignored", "?limit=0", 0, defaultLimit},
		{"garbage is ignored", "?offset=abc&limit=xyz", 0, defaultLimit},
	}

	// running each row as its own sub-test. If one fails, its name will be shown in the output.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// fake request whose url ends with the query from this row, such as ?offset=10&limit=5.
			req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations"+tc.query, nil)
			offset, limit := readPaging(req) // calling the function i am testing.

			if offset != tc.wantOffset { // comparing the actual values with the expected ones.
				t.Errorf("offset: want %d, got %d", tc.wantOffset, offset)
			}
			if limit != tc.wantLimit {
				t.Errorf("limit: want %d, got %d", tc.wantLimit, limit)
			}
		})
	}
}

// next function to run tests for the paging rules (next and prev links).
// Checking the paging rules on a list of 25 locations, like example which items are on each page,
// ,and when the "next" and "prev" links should appear.

// i know that this test uses its own copy of the paging logic (makePage below).
// it does not call listOCPILocations or buildLink.
func TestListOCPILocations_PageWindow(t *testing.T) {
	// getRel will find the url marked rel="next" or rel="prev" in a Link header. It give output "" if there is none.
	getRel := func(linkHeader, rel string) string {
		for _, part := range strings.Split(linkHeader, ",") { // each comma separated part is one link.
			part = strings.TrimSpace(part)
			if !strings.Contains(part, `rel="`+rel+`"`) {
				continue
			}
			// the url sits between < and >.
			s := strings.Index(part, "<")
			e := strings.Index(part, ">")
			if s >= 0 && e > s {
				return part[s+1 : e]
			}
		}
		return ""
	}

	// i need 25 locations with only an ID (LOC1, LOC2, ...). This is enough to test paging.
	all := make([]ocpiLocation, 0, 25)
	for i := 0; i < 25; i++ {
		all = append(all, ocpiLocation{ID: "LOC" + strconv.Itoa(i+1)})
	}

	// makePage will output one page of the list and the link header that goes with it.
	// offset means how many items to skip. limit is the page size.
	makePage := func(offset, limit int) (page []ocpiLocation, link string) {
		start := offset // this will work out where page starts and ends. An offset past the end gives an empty page.
		if start > len(all) {
			start = len(all)
		}
		end := start + limit
		if end > len(all) {
			end = len(all)
		}
		links := []string{} // the link header can hold two links, so i will collect them in a list first.

		// next is  the address of the locations endpoint, without a query.
		base := "http://localhost:8080/ocpi/2.3.0/locations"
		if end < len(all) { // i know that items remain after this page, so i will offer a "next" link.
			links = append(links, "<"+base+"?offset="+strconv.Itoa(end)+"&limit="+strconv.Itoa(limit)+`>; rel="next"`)
		}
		// now not on the first page, so i offer a "prev" link. Only i step back one page, but not below 0.
		if start > 0 {
			prev := start - limit
			if prev < 0 {
				prev = 0
			}
			links = append(links, "<"+base+"?offset="+strconv.Itoa(prev)+"&limit="+strconv.Itoa(limit)+`>; rel="prev"`)
		}

		// finally i join the links with commas, like a real link header.
		return all[start:end], strings.Join(links, ", ")
	}

	// Page 1 of 3 (offset 0, limit 10). It is expected 10 items, a next link and no prev link.
	page, link := makePage(0, 10)
	if len(page) != 10 {
		t.Fatalf("first page: want 10, got %d", len(page))
	}
	if getRel(link, "next") == "" {
		t.Error("first page must offer rel=next")
	}
	if getRel(link, "prev") != "" {
		t.Error("first page must NOT offer rel=prev")
	}

	// page 3 of 3 (offset 20, limit 10). Now only 5 items are left. I expect no next link and a prev link.
	page, link = makePage(20, 10)
	if len(page) != 5 {
		t.Fatalf("last page: want 5, got %d", len(page))
	}
	if getRel(link, "next") != "" {
		t.Error("last page must NOT offer rel=next")
	}
	if getRel(link, "prev") == "" {
		t.Error("last page must offer rel=prev")
	}
}

// tests for the peer token
// the peer token is something the server gives the partner (emsp) during registration.

// after registration, the token i gave to the partner must be accepted.
func TestRequireToken_PeerTokenAccepted(t *testing.T) {
	// a made-up token, standing in for the one which handshake would generate.
	peer := "generated-peer-token-from-handshake"

	// next storing it, as if a partner had just registered.
	credStore.SavePeer(peer, credentials.Credentials{Token: peer}, credentials.Party{})

	// when the test ends, empty the store. credStore is shared, so this keeps other tests unaffected.
	t.Cleanup(func() { credStore.Clear() })

	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	// i am encoding the token as a real partner would, before sending it.
	req.Header.Set("Authorization", "Token "+base64.StdEncoding.EncodeToString([]byte(peer)))

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("peer token must be accepted; got %d", rec.Code)
	}
}

// registering a partner must not stop the bootstrap token from working. This server keeps both valid to make testing easier.
func TestRequireToken_StillAcceptsBootstrapAfterRegistration(t *testing.T) {
	peer := "another-peer-token" // registering a partner first.
	credStore.SavePeer(peer, credentials.Credentials{Token: peer}, credentials.Party{})
	t.Cleanup(func() { credStore.Clear() })

	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", config.AuthHeader())
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap token must still work; got %d", rec.Code)
	}
}

// after the registration is cleared (as DELETE /credentials does), the peer token should stop working.
func TestRequireToken_PeerTokenStopsWorkingAfterClear(t *testing.T) {
	peer := "temporary-peer-token"
	// making the header once, so i can use the same one before and after the clear.
	authHeader := "Token " + base64.StdEncoding.EncodeToString([]byte(peer))

	credStore.SavePeer(peer, credentials.Credentials{Token: peer}, credentials.Party{})
	t.Cleanup(func() { credStore.Clear() })

	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Step 1 below - while registered, the token works.
	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", authHeader)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("peer token must be accepted before clear; got %d", rec.Code)
	}

	// Step 2 - is to unregister the partner. This forgets the peer token.
	credStore.Clear()
	// Step 3 is using the same header again. It has to now get 401. I make a new req and rec because the old ones are already used.
	req = httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", authHeader)
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cleared peer token must be rejected; got %d", rec.Code)
	}
}
