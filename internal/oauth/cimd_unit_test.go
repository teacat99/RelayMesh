package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseCIMDBodyRejectsSecret(t *testing.T) {
	body := `{"client_id":"https://ex.example/m","client_name":"x","redirect_uris":["http://127.0.0.1/cb"],"token_endpoint_auth_method":"none","client_secret":"nope"}`
	_, err := parseCIMDBody([]byte(body), "https://ex.example/m")
	if err == nil {
		t.Fatal("expected reject client_secret")
	}
}

func TestFetchCIMDCacheHit(t *testing.T) {
	ClearCIMDCache()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	SetCIMDCacheClock(func() time.Time { return t0 })
	defer SetCIMDCacheClock(nil)
	SetCIMDLookupHost(func(ctx context.Context, host string) error { return nil })
	defer SetCIMDLookupHost(nil)

	var hits int32
	docJSON := `{"client_id":"https://cimd-cache.test/doc","client_name":"cache","redirect_uris":["http://127.0.0.1:9/cb"],"token_endpoint_auth_method":"none"}`
	SetCIMDHTTPDoer(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&hits, 1)
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(docJSON)),
			Header:     http.Header{"Content-Type": {"application/json"}},
		}, nil
	}))
	defer SetCIMDHTTPDoer(nil)

	url := "https://cimd-cache.test/doc"
	if _, err := FetchCIMD(context.Background(), url); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchCIMD(context.Background(), url); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("want 1 fetch got %d", hits)
	}

	SetCIMDCacheClock(func() time.Time { return t0.Add(16 * time.Minute) })
	if _, err := FetchCIMD(context.Background(), url); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("want cache expiry refetch got %d", hits)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestValidateCIMDTokenAuth(t *testing.T) {
	err := validateCIMDDocument(CIMDDocument{
		ClientID: "https://x/y", RedirectURIs: []string{"http://127.0.0.1/cb"}, TokenAuth: "client_secret_basic",
	})
	if err == nil {
		t.Fatal("want reject auth method")
	}
}

func TestCIMDMismatchClientID(t *testing.T) {
	raw, _ := json.Marshal(CIMDDocument{
		ClientID: "https://other/doc", ClientName: "n", RedirectURIs: []string{"http://127.0.0.1/cb"}, TokenAuth: "none",
	})
	_, err := parseCIMDBody(raw, "https://expected/doc")
	if err == nil {
		t.Fatal("want mismatch error")
	}
}
