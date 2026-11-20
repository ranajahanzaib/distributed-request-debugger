package trace

import (
	"os"
	"testing"
	"time"
)

func TestParseBuildsTreeAndPreservesEvents(t *testing.T) {
	b, e := os.ReadFile("../../../tests/fixtures/traces/lock-wait.json")
	if e != nil {
		t.Fatal(e)
	}
	got, e := Parse(b)
	if e != nil {
		t.Fatal(e)
	}
	if got.TraceID != "11111111111111111111111111111111" || len(got.Spans) != 3 || got.Root == nil {
		t.Fatalf("unexpected parsed trace: %#v", got)
	}
	if len(got.Root.Children) != 1 || len(got.Root.Children[0].Children) != 1 {
		t.Fatal("parent-child links not reconstructed")
	}
	db := got.Root.Children[0].Children[0]
	if db.Duration() != 7500*time.Millisecond || len(db.Events) != 1 || db.Attributes["db.wait.type"] != "Lock" {
		t.Fatalf("span fields lost: %#v", db)
	}
}
func TestParseMarksMissingParent(t *testing.T) {
	raw := []byte(`{"batches":[{"resource":{"attributes":[]},"scopeSpans":[{"spans":[{"traceId":"a","spanId":"b","parentSpanId":"missing","name":"x","startTimeUnixNano":"10","endTimeUnixNano":"20"}]}]}]}`)
	got, e := Parse(raw)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Warnings) == 0 || len(got.Spans[0].Warnings) == 0 {
		t.Fatal("missing parent should be reported")
	}
}

func TestParseCutsParentCycleAndReportsWarning(t *testing.T) {
	raw := []byte(`{"batches":[{"resource":{"attributes":[]},"scopeSpans":[{"spans":[{"traceId":"1","spanId":"a","parentSpanId":"b","name":"a","startTimeUnixNano":"1","endTimeUnixNano":"2"},{"traceId":"1","spanId":"b","parentSpanId":"a","name":"b","startTimeUnixNano":"1","endTimeUnixNano":"2"}]}]}]}`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) == 0 || got.Root == nil {
		t.Fatalf("cycle was not handled: %#v", got)
	}
}

func TestParseBase64IDs(t *testing.T) {
	// Base64 encoding of 16-byte traceId and 8-byte spanIds (as emitted by protobuf JSON / Tempo)
	// 0x13f4d3228539a8e9e1d40a8de37edee0 in base64 is "E/TTIoU5qOnh1AqN437e4A=="
	// 0x6cd06e5c0868665a in base64 is "bNBuXAhoZlo="
	// 0x7b61e472f12af128 in base64 is "e2HkcvEq8Sg="
	raw := []byte(`{
		"batches": [{
			"resource": {
				"attributes": [{"key": "service.name", "value": {"stringValue": "gateway"}}]
			},
			"scopeSpans": [{
				"spans": [
					{
						"traceId": "E/TTIoU5qOnh1AqN437e4A==",
						"spanId": "bNBuXAhoZlo=",
						"name": "root",
						"kind": 2,
						"startTimeUnixNano": "1000",
						"endTimeUnixNano": "2000"
					},
					{
						"traceId": "E/TTIoU5qOnh1AqN437e4A==",
						"spanId": "e2HkcvEq8Sg=",
						"parentSpanId": "bNBuXAhoZlo=",
						"name": "child",
						"kind": 1,
						"startTimeUnixNano": "1100",
						"endTimeUnixNano": "1900"
					}
				]
			}]
		}]
	}`)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceID != "13f4d3228539a8e9e1d40a8de37edee0" {
		t.Fatalf("expected hex trace ID, got %q", got.TraceID)
	}
	if len(got.Spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(got.Spans))
	}
	if got.Spans[0].SpanID != "6cd06e5c0868665a" {
		t.Fatalf("expected hex span ID, got %q", got.Spans[0].SpanID)
	}
	if got.Spans[1].ParentSpanID != "6cd06e5c0868665a" {
		t.Fatalf("expected hex parent span ID, got %q", got.Spans[1].ParentSpanID)
	}
	if len(got.Root.Children) != 1 {
		t.Fatalf("expected parent-child relationship to hold after normalization")
	}
}
