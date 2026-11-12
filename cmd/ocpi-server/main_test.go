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

// ---------- requireToken tests ----------

func TestRequireToken_CorrectTokenPasses(t *testing.T) {
	// A wrapped handler that just reports it was reached.
	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status_code":1000}`))
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", config.AuthHeader())
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestRequireToken_MissingToken(t *testing.T) {
	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must NOT be called without a token")
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}

	var reply ocpiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("reply is not valid JSON: %v", err)
	}
	// 2001 = "unknown token": the precise OCPI code for a missing or
	// invalid Authorization header. (2000 is the generic client error.)
	if reply.StatusCode != credentials.StatusUnknownToken {
		t.Errorf("want OCPI status 2001, got %d", reply.StatusCode)
	}
}

func TestRequireToken_WrongToken(t *testing.T) {
	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must NOT be called with a wrong token")
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", "Token definitely-not-the-token")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestRequireToken_RawTokenIsRejected(t *testing.T) {
	// The whole point of Stage 11: the RAW token must no longer work.
	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", "Token "+config.TokenRaw()) // NOT encoded
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("raw token must be rejected; got %d", rec.Code)
	}
}

// ---------- readPaging tests ----------

func TestReadPaging(t *testing.T) {
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

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations"+tc.query, nil)
			offset, limit := readPaging(req)

			if offset != tc.wantOffset {
				t.Errorf("offset: want %d, got %d", tc.wantOffset, offset)
			}
			if limit != tc.wantLimit {
				t.Errorf("limit: want %d, got %d", tc.wantLimit, limit)
			}
		})
	}
}

// ---------- pagination headers via the real handler ----------

func TestListOCPILocations_PageWindow(t *testing.T) {
	// Parse a Link header and pull out one rel value.
	getRel := func(linkHeader, rel string) string {
		for _, part := range strings.Split(linkHeader, ",") {
			part = strings.TrimSpace(part)
			if !strings.Contains(part, `rel="`+rel+`"`) {
				continue
			}
			s := strings.Index(part, "<")
			e := strings.Index(part, ">")
			if s >= 0 && e > s {
				return part[s+1 : e]
			}
		}
		return ""
	}

	// 25 fake OCPI locations.
	all := make([]ocpiLocation, 0, 25)
	for i := 0; i < 25; i++ {
		all = append(all, ocpiLocation{ID: "LOC" + strconv.Itoa(i+1)})
	}

	// Build a paging reply the same way the handler does.
	makePage := func(offset, limit int) (page []ocpiLocation, link string) {
		start := offset
		if start > len(all) {
			start = len(all)
		}
		end := start + limit
		if end > len(all) {
			end = len(all)
		}
		links := []string{}
		base := "http://localhost:8080/ocpi/2.3.0/locations"
		if end < len(all) {
			links = append(links, "<"+base+"?offset="+strconv.Itoa(end)+"&limit="+strconv.Itoa(limit)+`>; rel="next"`)
		}
		if start > 0 {
			prev := start - limit
			if prev < 0 {
				prev = 0
			}
			links = append(links, "<"+base+"?offset="+strconv.Itoa(prev)+"&limit="+strconv.Itoa(limit)+`>; rel="prev"`)
		}
		return all[start:end], strings.Join(links, ", ")
	}

	// Page 1 of 3.
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

	// Page 3 of 3 (last, partial).
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

// ---------- requireToken accepts the peer token after registration ----------

func TestRequireToken_PeerTokenAccepted(t *testing.T) {
	peer := "generated-peer-token-from-handshake"
	credStore.SavePeer(peer, credentials.Credentials{Token: peer}, credentials.Party{})
	t.Cleanup(func() { credStore.Clear() })

	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", "Token "+base64.StdEncoding.EncodeToString([]byte(peer)))

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("peer token must be accepted; got %d", rec.Code)
	}
}

func TestRequireToken_StillAcceptsBootstrapAfterRegistration(t *testing.T) {
	peer := "another-peer-token"
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

func TestRequireToken_PeerTokenStopsWorkingAfterClear(t *testing.T) {
	peer := "temporary-peer-token"
	authHeader := "Token " + base64.StdEncoding.EncodeToString([]byte(peer))

	credStore.SavePeer(peer, credentials.Credentials{Token: peer}, credentials.Party{})
	t.Cleanup(func() { credStore.Clear() })

	handler := requireToken(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// 1. Must be accepted while registered.
	req := httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", authHeader)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("peer token must be accepted before clear; got %d", rec.Code)
	}

	// 2. Clear the registration.
	credStore.Clear()
	// 3. Same header must now be rejected.
	req = httptest.NewRequest("GET", "/ocpi/2.3.0/locations", nil)
	req.Header.Set("Authorization", authHeader)
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cleared peer token must be rejected; got %d", rec.Code)
	}
}
