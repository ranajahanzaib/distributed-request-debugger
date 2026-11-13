package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/tempo"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTraceAPIAndDiagnosis(t *testing.T) {
	fixture, e := os.ReadFile("../../../tests/fixtures/traces/lock-wait.json")
	if e != nil {
		t.Fatal(e)
	}
	client := tempo.New("http://tempo.test")
	client.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		status := 200
		body := fixture
		if r.URL.Path == "/ready" {
			body = []byte("ready")
		} else if r.URL.Path == "/api/search" {
			body = []byte("{\"traces\":[]}")
		} else if r.URL.Path != "/api/traces/11111111111111111111111111111111" {
			status = 404
			body = []byte("not found")
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	h := (&Server{Tempo: client}).Handler()
	for _, path := range []string{"/api/health", "/api/traces/11111111111111111111111111111111", "/api/traces/11111111111111111111111111111111/diagnosis", "/trace/11111111111111111111111111111111"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Errorf("%s: status %d body %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/traces/not-a-trace", nil))
	if rec.Code != 400 {
		t.Fatalf("invalid ID status=%d", rec.Code)
	}
}
