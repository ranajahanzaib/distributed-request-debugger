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
