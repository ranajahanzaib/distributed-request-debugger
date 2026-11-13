package trace

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type Event struct {
	Name       string         `json:"name"`
	Time       time.Time      `json:"time"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
type Span struct {
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Service      string         `json:"service"`
	Name         string         `json:"name"`
	Kind         int            `json:"kind,omitempty"`
	Start        time.Time      `json:"start"`
	End          time.Time      `json:"end"`
	Status       string         `json:"status,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
	Children     []*Span        `json:"children,omitempty"`
	Events       []Event        `json:"events,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`
}

func (s Span) Duration() time.Duration {
	if s.End.Before(s.Start) {
		return 0
	}
	return s.End.Sub(s.Start)
}

type Trace struct {
	TraceID  string   `json:"trace_id"`
	Root     *Span    `json:"root,omitempty"`
	Spans    []*Span  `json:"spans"`
	Services []string `json:"services"`
	Warnings []string `json:"warnings,omitempty"`
}
type rawValue struct {
	String *string         `json:"stringValue"`
	Int    *string         `json:"intValue"`
	Bool   *bool           `json:"boolValue"`
	Double *float64        `json:"doubleValue"`
	Array  json.RawMessage `json:"arrayValue"`
	Kv     json.RawMessage `json:"kvlistValue"`
}
type kv struct {
	Key   string   `json:"key"`
	Value rawValue `json:"value"`
}
type rawSpan struct {
	TraceID    string      `json:"traceId"`
	SpanID     string      `json:"spanId"`
	Parent     string      `json:"parentSpanId"`
	Name       string      `json:"name"`
	Kind       int         `json:"kind"`
	Start      json.Number `json:"startTimeUnixNano"`
	End        json.Number `json:"endTimeUnixNano"`
	Attributes []kv        `json:"attributes"`
	Status     struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
	Events []struct {
		Name       string      `json:"name"`
		Time       json.Number `json:"timeUnixNano"`
		Attributes []kv        `json:"attributes"`
	} `json:"events"`
}
type batch struct {
	Resource struct {
		Attributes []kv `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Spans []rawSpan `json:"spans"`
	} `json:"scopeSpans"`
	Instrumentation []struct {
		Spans []rawSpan `json:"spans"`
	} `json:"instrumentationLibrarySpans"`
}
type response struct {
	Batches       []batch `json:"batches"`
	ResourceSpans []struct {
		Resource struct {
			Attributes []kv `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []rawSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

func Parse(data []byte) (*Trace, error) {
	var r response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode Tempo trace: %w", err)
	}
	t := &Trace{}
	seen := map[string]bool{}
	services := map[string]bool{}
	add := func(rs []kv, sp rawSpan) {
		if t.TraceID == "" {
			t.TraceID = sp.TraceID
		} else if sp.TraceID != "" && sp.TraceID != t.TraceID {
			t.Warnings = append(t.Warnings, "span from a different trace ID skipped")
			return
		}
		if sp.SpanID == "" {
			t.Warnings = append(t.Warnings, "span with missing ID skipped")
			return
		}
		if seen[sp.SpanID] {
			t.Warnings = append(t.Warnings, "duplicate span ID: "+sp.SpanID)
			return
		}
		seen[sp.SpanID] = true
		start, e1 := timestamp(sp.Start)
		end, e2 := timestamp(sp.End)
		if e1 != nil || e2 != nil || end.Before(start) {
			t.Warnings = append(t.Warnings, "invalid timestamps on span "+sp.SpanID)
			return
		}
		attrs := map[string]any{}
		for _, a := range append(append([]kv{}, rs...), sp.Attributes...) {
			attrs[a.Key] = value(a.Value)
		}
		svc, _ := attrs["service.name"].(string)
		if svc == "" {
			svc = "unknown"
		}
		services[svc] = true
		status := "ok"
		if sp.Status.Code == 2 {
			status = "error"
		}
		events := make([]Event, 0, len(sp.Events))
		for _, ev := range sp.Events {
			ts, err := timestamp(ev.Time)
			if err != nil {
				t.Warnings = append(t.Warnings, "invalid event timestamp on span "+sp.SpanID)
				continue
			}
			ea := map[string]any{}
			for _, a := range ev.Attributes {
				ea[a.Key] = value(a.Value)
			}
			events = append(events, Event{Name: ev.Name, Time: ts, Attributes: ea})
		}
		s := &Span{TraceID: sp.TraceID, SpanID: sp.SpanID, ParentSpanID: sp.Parent, Service: svc, Name: sp.Name, Kind: sp.Kind, Start: start, End: end, Status: status, Attributes: attrs, Events: events}
		t.Spans = append(t.Spans, s)
	}
	for _, b := range r.Batches {
		for _, ss := range b.ScopeSpans {
			for _, sp := range ss.Spans {
				add(b.Resource.Attributes, sp)
			}
		}
		for _, ss := range b.Instrumentation {
			for _, sp := range ss.Spans {
				add(b.Resource.Attributes, sp)
			}
		}
	}
	for _, rs := range r.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				add(rs.Resource.Attributes, sp)
			}
		}
	}
	byID := map[string]*Span{}
	for _, s := range t.Spans {
		byID[s.SpanID] = s
	}
	cut := map[string]bool{}
	state := map[string]uint8{}
	for _, start := range t.Spans {
		if state[start.SpanID] != 0 {
			continue
		}
		path := []*Span{}
		position := map[string]int{}
		current := start
		for current != nil {
			if at, ok := position[current.SpanID]; ok {
				cycle := path[at:]
				edge := cycle[0]
				cut[edge.SpanID] = true
				edge.Warnings = append(edge.Warnings, "parent cycle detected")
				t.Warnings = append(t.Warnings, "parent cycle detected at span "+edge.SpanID)
				break
			}
			if state[current.SpanID] == 2 {
				break
			}
			position[current.SpanID] = len(path)
			path = append(path, current)
			state[current.SpanID] = 1
			current = byID[current.ParentSpanID]
		}
		for _, span := range path {
			state[span.SpanID] = 2
		}
	}
	roots := []*Span{}
	for _, s := range t.Spans {
		if s.ParentSpanID == "" || cut[s.SpanID] {
			roots = append(roots, s)
			continue
		}
		if p := byID[s.ParentSpanID]; p != nil {
			p.Children = append(p.Children, s)
		} else {
			s.Warnings = append(s.Warnings, "missing parent span "+s.ParentSpanID)
			t.Warnings = append(t.Warnings, "missing parent for "+s.SpanID)
		}
	}
	if len(roots) > 0 {
		t.Root = roots[0]
		if len(roots) > 1 {
			t.Warnings = append(t.Warnings, "multiple root spans")
		}
	} else {
		t.Warnings = append(t.Warnings, "no root span; trace may contain a cycle or only partial context")
	}
	for s := range services {
		t.Services = append(t.Services, s)
	}
	if len(t.Spans) == 0 {
		return nil, fmt.Errorf("Tempo trace contains no valid spans")
	}
	return t, nil
}
func timestamp(n json.Number) (time.Time, error) {
	v, e := strconv.ParseInt(n.String(), 10, 64)
	if e != nil {
		return time.Time{}, e
	}
	return time.Unix(0, v).UTC(), nil
}
func value(v rawValue) any {
	switch {
	case v.String != nil:
		return *v.String
	case v.Int != nil:
		return *v.Int
	case v.Bool != nil:
		return *v.Bool
	case v.Double != nil:
		return *v.Double
	case len(v.Array) > 0:
		return string(v.Array)
	case len(v.Kv) > 0:
		return string(v.Kv)
	}
	return nil
}
