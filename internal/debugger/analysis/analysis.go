package analysis

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/trace"
)

type Finding struct {
	Category   string   `json:"category"`
	Summary    string   `json:"summary"`
	Service    string   `json:"service,omitempty"`
	Operation  string   `json:"operation,omitempty"`
	SpanIDs    []string `json:"span_ids"`
	DurationMS float64  `json:"duration_ms"`
	Confidence string   `json:"confidence"`
	Evidence   []string `json:"evidence"`
	NextSteps  []string `json:"next_steps,omitempty"`
}
type Report struct {
	SchemaVersion  string    `json:"schema_version"`
	TraceID        string    `json:"trace_id"`
	DurationMS     float64   `json:"duration_ms"`
	Services       []string  `json:"services"`
	SpansAnalyzed  int       `json:"spans_analyzed"`
	TraceComplete  bool      `json:"trace_complete"`
	Findings       []Finding `json:"findings"`
	PrimaryFinding *Finding  `json:"primary_finding,omitempty"`
	Warnings       []string  `json:"warnings,omitempty"`
}

func Analyze(t *trace.Trace) Report {
	r := Report{SchemaVersion: "1.0", TraceID: t.TraceID, Services: t.Services, SpansAnalyzed: len(t.Spans), TraceComplete: len(t.Warnings) == 0, Warnings: append([]string{}, t.Warnings...)}
	if t.Root != nil && t.Root.Service == "gateway" {
		present := map[string]bool{}
		for _, service := range t.Services {
			present[service] = true
		}
		for _, service := range []string{"auth", "orders", "inventory", "payment"} {
			if !present[service] {
				r.TraceComplete = false
				r.Warnings = append(r.Warnings, "expected checkout service absent from this trace: "+service)
			}
		}
	}
	if t.Root != nil {
		r.DurationMS = float64(t.Root.Duration()) / float64(time.Millisecond)
	}
	for _, s := range t.Spans {
		if dur := s.Duration(); dur >= 100*time.Millisecond && (s.Attributes["db.system"] != nil || s.Attributes["db.system.name"] != nil) {
			if typ, _ := s.Attributes["db.wait.type"].(string); typ == "Lock" {
				evidence := []string{fmt.Sprintf("PostgreSQL reported wait_event_type=%s", typ)}
				if event, ok := s.Attributes["db.wait.event"].(string); ok && event != "" {
					evidence = append(evidence, "PostgreSQL wait_event="+event)
				}
				if pids, ok := s.Attributes["db.wait.blocking_pids"].(string); ok && pids != "" {
					evidence = append(evidence, "pg_blocking_pids reported "+pids)
				}
				if count := number(s.Attributes["db.wait.observation_count"]); !math.IsNaN(count) {
					evidence = append(evidence, fmt.Sprintf("Observer recorded %.0f lock-wait samples", count))
				}
				if est := number(s.Attributes["db.wait.observed_estimate_ms"]); !math.IsNaN(est) {
					evidence = append(evidence, fmt.Sprintf("Sampled wait estimate %.0f ms at a 40 ms polling interval; this is approximate", est))
				}
				r.Findings = append(r.Findings, Finding{Category: "database_lock_wait", Summary: "PostgreSQL lock contention observed during the inventory operation", Service: s.Service, Operation: s.Name, SpanIDs: []string{s.SpanID}, DurationMS: float64(dur) / 1e6, Confidence: "confirmed", Evidence: evidence, NextSteps: []string{"Inspect transaction boundaries that hold inventory row locks", "Reduce the time between acquiring and releasing the inventory lock"}})
			} else {
				r.Findings = append(r.Findings, Finding{Category: "slow_database_operation", Summary: "A database operation occupies a significant interval", Service: s.Service, Operation: s.Name, SpanIDs: []string{s.SpanID}, DurationMS: float64(dur) / 1e6, Confidence: "inferred", Evidence: []string{"The database span duration is measured; its wait cause is unknown"}, NextSteps: []string{"Inspect database wait events and query execution details"}})
			}
		}
	}
	if len(r.Findings) == 0 {
		var slow *trace.Span
		for _, s := range t.Spans {
			if s.Kind == 3 {
				if slow == nil || s.Duration() > slow.Duration() {
					slow = s
				}
			}
		}
		if slow != nil && slow.Duration() >= 200*time.Millisecond {
			peer := slow.Service
			for _, key := range []string{"peer.service", "server.address", "net.peer.name"} {
				if v, ok := slow.Attributes[key].(string); ok && v != "" {
					peer = v
					break
				}
			}
			category := "slow_downstream_service"
			if strings.Contains(strings.ToLower(peer), "payment") {
				category = "external_dependency_latency"
			}
			r.Findings = append(r.Findings, Finding{Category: category, Summary: "A downstream call is the largest observed span; its duration includes remote work and transport and does not prove network latency", Service: peer, Operation: slow.Name, SpanIDs: []string{slow.SpanID}, DurationMS: float64(slow.Duration()) / 1e6, Confidence: "inferred", Evidence: []string{"The downstream client span duration is measured"}})
		}
	}
	if len(r.Warnings) > 0 {
		r.Findings = append(r.Findings, Finding{Category: "incomplete_trace", Summary: "The trace has missing or malformed span relationships", Confidence: "insufficient_evidence", Evidence: append([]string{}, t.Warnings...)})
	}
	if len(r.Findings) > 0 {
		sort.SliceStable(r.Findings, func(i, j int) bool {
			rank := func(f Finding) int {
				if f.Confidence == "confirmed" {
					return 0
				}
				if f.Confidence == "inferred" {
					return 1
				}
				return 2
			}
			if rank(r.Findings[i]) != rank(r.Findings[j]) {
				return rank(r.Findings[i]) < rank(r.Findings[j])
			}
			return r.Findings[i].DurationMS > r.Findings[j].DurationMS
		})
		r.PrimaryFinding = &r.Findings[0]
	}
	return r
}

// Exclusive computes parent time not covered by the union of its direct children.
func Exclusive(s *trace.Span) time.Duration {
	if s == nil {
		return 0
	}
	type interval struct{ a, b time.Time }
	xs := make([]interval, 0, len(s.Children))
	for _, c := range s.Children {
		a := maxTime(c.Start, s.Start)
		b := minTime(c.End, s.End)
		if b.After(a) {
			xs = append(xs, interval{a, b})
		}
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].a.Before(xs[j].a) })
	covered := time.Duration(0)
	if len(xs) > 0 {
		a, b := xs[0].a, xs[0].b
		for _, x := range xs[1:] {
			if x.a.After(b) {
				covered += b.Sub(a)
				a, b = x.a, x.b
			} else if x.b.After(b) {
				b = x.b
			}
		}
		covered += b.Sub(a)
	}
	total := s.Duration() - covered
	if total < 0 {
		return 0
	}
	return total
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func number(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return math.NaN()
}
