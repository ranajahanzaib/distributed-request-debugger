package tempo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/trace"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

var traceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

func New(base string) *Client {
	return &Client{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Second}}
}
func (c *Client) Fetch(ctx context.Context, id string) (*trace.Trace, error) {
	if !traceIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid trace ID")
	}
	u := c.BaseURL + "/api/traces/" + id
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", "application/json")
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return nil, fmt.Errorf("Tempo unavailable: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("trace not found")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Tempo returned %s", resp.Status)
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if e != nil || len(data) > 16<<20 {
		return nil, fmt.Errorf("Tempo trace response exceeds 16 MiB or could not be read")
	}
	var raw json.RawMessage
	if e = json.Unmarshal(data, &raw); e != nil {
		return nil, fmt.Errorf("invalid Tempo response: %w", e)
	}
	t, e := trace.Parse(raw)
	if e != nil {
		return nil, e
	}
	if t.TraceID == "" {
		t.TraceID = id
	}
	return t, nil
}

type SearchResult struct {
	TraceID     string  `json:"traceID"`
	RootService string  `json:"rootServiceName"`
	RootName    string  `json:"rootTraceName"`
	Start       string  `json:"startTime"`
	DurationMS  float64 `json:"durationMs"`
}

func (c *Client) Search(ctx context.Context, limit int, minDuration time.Duration) ([]SearchResult, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("start", strconv.FormatInt(time.Now().Add(-24*time.Hour).Unix(), 10))
	q.Set("end", strconv.FormatInt(time.Now().Unix(), 10))
	q.Set("q", `{resource.service.name="gateway" && name != "GET /healthz"}`)
	if minDuration > 0 {
		q.Set("q", fmt.Sprintf(`{resource.service.name="gateway" && name != "GET /healthz" && duration > %s}`, minDuration))
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/search?"+q.Encode(), nil)
	if e != nil {
		return nil, e
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return nil, fmt.Errorf("Tempo unavailable: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Tempo returned %s", resp.Status)
	}
	var body struct {
		Traces []SearchResult `json:"traces"`
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if e != nil || len(data) > 2<<20 {
		return nil, fmt.Errorf("Tempo search response exceeds 2 MiB or could not be read")
	}
	if e = json.Unmarshal(data, &body); e != nil {
		return nil, e
	}
	return body.Traces, nil
}
func (c *Client) Health(ctx context.Context) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/ready", nil)
	if e != nil {
		return e
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Tempo returned %s", resp.Status)
	}
	return nil
}
