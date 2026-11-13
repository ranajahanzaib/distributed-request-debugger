package analysis

import (
	"os"
	"testing"
	"time"

	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/trace"
)

func TestExclusiveUsesUnionOfOverlappingChildren(t *testing.T) {
	base := time.Unix(0, 0)
	p := &trace.Span{Start: base, End: base.Add(10 * time.Second), Children: []*trace.Span{{Start: base.Add(time.Second), End: base.Add(6 * time.Second)}, {Start: base.Add(4 * time.Second), End: base.Add(8 * time.Second)}}}
	if got := Exclusive(p); got != 3*time.Second {
		t.Fatalf("Exclusive=%s, want 3s", got)
	}
}
func TestAnalyzeConfirmsOnlyExplicitLockEvidence(t *testing.T) {
	b, e := os.ReadFile("../../../tests/fixtures/traces/lock-wait.json")
	if e != nil {
		t.Fatal(e)
	}
	tr, e := trace.Parse(b)
	if e != nil {
		t.Fatal(e)
	}
	r := Analyze(tr)
	if r.PrimaryFinding == nil || r.PrimaryFinding.Category != "database_lock_wait" || r.PrimaryFinding.Confidence != "confirmed" {
		t.Fatalf("unexpected diagnosis %#v", r.PrimaryFinding)
	}
	tr.Spans[2].Attributes["db.wait.type"] = ""
	r = Analyze(tr)
	if r.PrimaryFinding == nil || r.PrimaryFinding.Category != "slow_database_operation" || r.PrimaryFinding.Confidence != "inferred" {
		t.Fatalf("slow DB without wait should be inferred: %#v", r.PrimaryFinding)
	}
}
