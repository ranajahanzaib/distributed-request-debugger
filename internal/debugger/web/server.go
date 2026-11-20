package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/analysis"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/scenarios"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/tempo"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/trace"
)

type Server struct {
	Tempo       *tempo.Client
	DemoEnabled bool
	Runner      *scenarios.Runner
}

var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/health", s.health)
	m.HandleFunc("GET /api/traces", s.list)
	m.HandleFunc("GET /api/traces/{id}", s.detail)
	m.HandleFunc("GET /api/traces/{id}/diagnosis", s.diagnosis)
	m.HandleFunc("GET /api/comparisons", s.compare)
	m.HandleFunc("GET /api/demo/scenarios", s.scenarios)
	m.HandleFunc("POST /api/demo/run", s.runDemo)
	m.HandleFunc("GET /trace/{id}", s.tracePage)
	m.HandleFunc("GET /", s.home)
	return m
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	c, x := context.WithTimeout(r.Context(), 2*time.Second)
	defer x()
	e := s.Tempo.Health(c)
	state, status := "ok", 200
	if e != nil {
		state, status = "unavailable", 503
	}
	jsonOut(w, status, map[string]any{"status": state, "tempo_error": errText(e)})
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	c, x := context.WithTimeout(r.Context(), 6*time.Second)
	defer x()
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	d, _ := time.ParseDuration(r.URL.Query().Get("min_duration"))
	v, e := s.Tempo.Search(c, n, d)
	if e != nil {
		errOut(w, 503, "tempo_unavailable", e)
		return
	}
	jsonOut(w, 200, map[string]any{"traces": v})
}
func (s *Server) fetch(w http.ResponseWriter, r *http.Request) (*trace.Trace, bool) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		errOut(w, 400, "invalid_trace_id", fmt.Errorf("trace ID must be 32 hexadecimal characters"))
		return nil, false
	}
	c, x := context.WithTimeout(r.Context(), 8*time.Second)
	defer x()
	t, e := s.Tempo.Fetch(c, id)
	if e != nil {
		status, code := 503, "tempo_unavailable"
		if strings.Contains(e.Error(), "not found") {
			status, code = 404, "trace_not_found"
		}
		errOut(w, status, code, e)
		return nil, false
	}
	return t, true
}
func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	t, ok := s.fetch(w, r)
	if ok {
		jsonOut(w, 200, t)
	}
}
func (s *Server) diagnosis(w http.ResponseWriter, r *http.Request) {
	t, ok := s.fetch(w, r)
	if ok {
		jsonOut(w, 200, analysis.Analyze(t))
	}
}
func (s *Server) compare(w http.ResponseWriter, r *http.Request) {
	a, b := r.URL.Query().Get("before"), r.URL.Query().Get("after")
	if !idPattern.MatchString(a) || !idPattern.MatchString(b) {
		errOut(w, 400, "invalid_trace_id", fmt.Errorf("before and after must be trace IDs"))
		return
	}
	ta, ok := s.fetchID(w, r, a)
	if !ok {
		return
	}
	tb, ok := s.fetchID(w, r, b)
	if !ok {
		return
	}
	jsonOut(w, 200, Compare(analysis.Analyze(ta), analysis.Analyze(tb), ta, tb))
}
func (s *Server) fetchID(w http.ResponseWriter, r *http.Request, id string) (*trace.Trace, bool) {
	c, x := context.WithTimeout(r.Context(), 8*time.Second)
	defer x()
	t, e := s.Tempo.Fetch(c, id)
	if e != nil {
		errOut(w, 503, "trace_unavailable", e)
		return nil, false
	}
	return t, true
}
func (s *Server) scenarios(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]any{"enabled": s.DemoEnabled, "scenarios": []string{"healthy", "inventory-db-lock", "slow-payment", "upstream-timeout", "missing-propagation"}})
}
func (s *Server) runDemo(w http.ResponseWriter, r *http.Request) {
	if !s.DemoEnabled || s.Runner == nil {
		errOut(w, 403, "demo_disabled", fmt.Errorf("local demonstration controls are disabled"))
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" || !sameLocalOrigin(r.Header.Get("Origin"), r.Host) {
		errOut(w, 403, "origin_rejected", fmt.Errorf("demo requests must use this loopback page origin"))
		return
	}
	data, e := io.ReadAll(io.LimitReader(r.Body, 4097))
	if e != nil || len(data) > 4096 {
		errOut(w, 400, "invalid_request", fmt.Errorf("request body exceeds 4 KiB"))
		return
	}
	var request struct {
		Scenario string `json:"scenario"`
	}
	if e = json.Unmarshal(data, &request); e != nil {
		errOut(w, 400, "invalid_request", e)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	result, e := s.Runner.Run(ctx, request.Scenario)
	if e != nil {
		status := 502
		if ctx.Err() != nil {
			status = 504
		}
		errOut(w, status, "scenario_failed", e)
		return
	}
	jsonOut(w, 200, result)
}
func sameLocalOrigin(origin, host string) bool {
	u, e := url.Parse(origin)
	if e != nil || u.Host != host || u.User != nil || u.Path != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return (u.Scheme == "http" || u.Scheme == "https") && (h == "localhost" || h == "127.0.0.1" || h == "::1")
}
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	c, x := context.WithTimeout(r.Context(), 5*time.Second)
	defer x()
	ts, e := s.Tempo.Search(c, 30, 0)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if e != nil {
		fmt.Fprintf(w, "<h1>Tempo unavailable</h1><p>%s</p>", html.EscapeString(e.Error()))
		return
	}
	fmt.Fprint(w, "<!doctype html><meta name='viewport' content='width=device-width'><title>Request Debugger</title><style>body{font:15px system-ui;max-width:1100px;margin:36px auto;padding:0 20px;background:#f6f7f8;color:#17212b}table{border-collapse:collapse;width:100%;background:white}td,th{padding:10px;border-bottom:1px solid #ddd;text-align:left}code{font:12px monospace}button,select{padding:8px}</style><h1>Distributed Request Debugger</h1>")
	if s.DemoEnabled {
		fmt.Fprint(w, "<section><h2>Run local scenario</h2><select id='scenario'><option>healthy</option><option>inventory-db-lock</option><option>slow-payment</option><option>upstream-timeout</option><option>missing-propagation</option></select> <button id='run'>Run scenario</button><pre id='result' aria-live='polite'></pre></section><script>document.getElementById('run').onclick=async()=>{const o=document.getElementById('result');o.textContent='Running…';try{const r=await fetch('/api/demo/run',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({scenario:document.getElementById('scenario').value})});const b=await r.json();o.textContent=JSON.stringify(b,null,2);if(b.checkout&&b.checkout.trace_id)setTimeout(()=>location.reload(),1000)}catch(e){o.textContent=String(e)}};</script>")
	}
	fmt.Fprint(w, "<p>Recent traces in Tempo</p><table><tr><th>Trace</th><th>Service</th><th>Operation</th><th>Duration</th></tr>")
	for _, t := range ts {
		fmt.Fprintf(w, "<tr><td><a href='/trace/%s'><code>%s</code></a></td><td>%s</td><td>%s</td><td>%.2f ms</td></tr>", html.EscapeString(t.TraceID), html.EscapeString(t.TraceID), html.EscapeString(t.RootService), html.EscapeString(t.RootName), t.DurationMS)
	}
	if len(ts) == 0 {
		fmt.Fprint(w, "<tr><td colspan='4'>No traces found</td></tr>")
	}
	fmt.Fprint(w, "</table><section><h2>Compare traces</h2><label>Before <input id='before' inputmode='text' pattern='[0-9a-fA-F]{32}'></label> <label>After <input id='after' inputmode='text' pattern='[0-9a-fA-F]{32}'></label> <button id='compare'>Compare</button><pre id='comparison' aria-live='polite'></pre></section><script>document.getElementById('compare').onclick=async()=>{const out=document.getElementById('comparison');const q=new URLSearchParams({before:document.getElementById('before').value.trim(),after:document.getElementById('after').value.trim()});out.textContent='Loading…';try{const r=await fetch('/api/comparisons?'+q);const b=await r.json();out.textContent=JSON.stringify(b,null,2)}catch(e){out.textContent=String(e)}};const qp=new URLSearchParams(location.search);if(qp.get('before'))document.getElementById('before').value=qp.get('before');if(qp.get('after'))document.getElementById('after').value=qp.get('after');if(qp.get('before')&&qp.get('after'))document.getElementById('compare').click();</script><p><a href='/api/health'>Backend health</a> · <a href='http://localhost:3000'>Grafana</a></p>")
}
func (s *Server) tracePage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.fetch(w, r)
	if !ok {
		return
	}
	report := analysis.Analyze(t)
	duration := float64(1)
	base := time.Time{}
	if t.Root != nil {
		base = t.Root.Start
		if t.Root.Duration() > 0 {
			duration = float64(t.Root.Duration())
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta name='viewport' content='width=device-width'><title>Trace</title><style>body{font:15px system-ui;max-width:1100px;margin:32px auto;padding:0 18px;background:#f6f7f8;color:#17212b}.row{display:grid;grid-template-columns:240px 1fr 90px;gap:8px;margin:7px 0}.track{height:18px;background:#e5e9ed;position:relative}.bar{height:100%%;background:#3977a8;position:absolute}code{font:12px monospace}ul{padding-left:20px;line-height:1.5}</style><a href='/'>← Recent traces</a><h1>Trace diagnosis</h1><p><code>%s</code> · %.1f ms · %d spans · %d services</p>", html.EscapeString(t.TraceID), report.DurationMS, len(t.Spans), len(t.Services))
	if report.PrimaryFinding != nil {
		f := report.PrimaryFinding
		fmt.Fprintf(w, "<h2>%s</h2><p>%s (%s confidence, %.1f ms)</p>", html.EscapeString(f.Category), html.EscapeString(f.Summary), html.EscapeString(f.Confidence), f.DurationMS)
		if len(f.Evidence) > 0 {
			fmt.Fprint(w, "<h3>Evidence</h3><ul>")
			for _, ev := range f.Evidence {
				fmt.Fprintf(w, "<li>%s</li>", html.EscapeString(ev))
			}
			fmt.Fprint(w, "</ul>")
		}
		if len(f.NextSteps) > 0 {
			fmt.Fprint(w, "<h3>Next steps</h3><ul>")
			for _, step := range f.NextSteps {
				fmt.Fprintf(w, "<li>%s</li>", html.EscapeString(step))
			}
			fmt.Fprint(w, "</ul>")
		}
	}
	if len(report.Warnings) > 0 {
		fmt.Fprint(w, "<h3>Warnings</h3><ul>")
		for _, warn := range report.Warnings {
			fmt.Fprintf(w, "<li>%s</li>", html.EscapeString(warn))
		}
		fmt.Fprint(w, "</ul>")
	}
	fmt.Fprint(w, "<h2>Waterfall</h2>")
	for _, sp := range t.Spans {
		left := float64(sp.Start.Sub(base)) / duration * 100
		width := float64(sp.Duration()) / duration * 100
		if left < 0 {
			left = 0
		}
		if width < .5 {
			width = .5
		}
		fmt.Fprintf(w, "<div class='row'><div>%s · %s</div><div class='track'><div class='bar' style='left:%.3f%%;width:%.3f%%'></div></div><code>%.1f ms</code></div>", html.EscapeString(sp.Service), html.EscapeString(sp.Name), left, width, float64(sp.Duration())/1e6)
	}
	fmt.Fprintf(w, "<p><a href='/api/traces/%s/diagnosis'>Diagnosis JSON</a> · <a href='http://localhost:3000/explore'>Grafana Explore</a></p>", html.EscapeString(t.TraceID))
}
func Compare(a, b analysis.Report, ta, tb *trace.Trace) map[string]any {
	d := b.DurationMS - a.DurationMS
	p := 0.0
	if a.DurationMS != 0 {
		p = d / a.DurationMS * 100
	}
	return map[string]any{"before": a, "after": b, "duration_delta_ms": d, "duration_delta_percent": p, "changed_operations": delta(ta, tb)}
}
func delta(a, b *trace.Trace) []map[string]any {
	m, n := map[string]float64{}, map[string]float64{}
	for _, s := range a.Spans {
		k := s.Service + "." + s.Name
		v := float64(s.Duration()) / 1e6
		if v > m[k] {
			m[k] = v
		}
	}
	for _, s := range b.Spans {
		k := s.Service + "." + s.Name
		v := float64(s.Duration()) / 1e6
		if v > n[k] {
			n[k] = v
		}
	}
	o := []map[string]any{}
	for k, v := range n {
		if v != m[k] {
			o = append(o, map[string]any{"operation": k, "before_ms": m[k], "after_ms": v, "delta_ms": v - m[k]})
		}
	}
	return o
}
func jsonOut(w http.ResponseWriter, s int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(s)
	_ = json.NewEncoder(w).Encode(v)
}
func errOut(w http.ResponseWriter, s int, c string, e error) {
	jsonOut(w, s, map[string]string{"error": c, "message": e.Error()})
}
func errText(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
