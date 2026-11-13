//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/analysis"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/tempo"
)

type checkoutResult struct {
	TraceID    string `json:"trace_id"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	OrderID    string `json:"order_id"`
}

func requireEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("DDR_INTEGRATION") != "1" {
		t.Skip("set DDR_INTEGRATION=1 and start Compose first")
	}
}
func runCheckout(t *testing.T, key string) (checkoutResult, int) {
	t.Helper()
	body := fmt.Sprintf(`{"customer_id":"customer-demo","sku":"SKU-001","quantity":1,"idempotency_key":%q}`, key)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", env("GATEWAY_URL", "http://127.0.0.1:8080")+"/api/checkout", strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	var out checkoutResult
	if e = json.NewDecoder(resp.Body).Decode(&out); e != nil {
		t.Fatal(e)
	}
	return out, resp.StatusCode
}
func getTrace(t *testing.T, id string) *analysis.Report {
	t.Helper()
	c := tempo.New(env("TEMPO_URL", "http://127.0.0.1:3200"))
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		tr, e := c.Fetch(ctx, id)
		cancel()
		if e == nil {
			r := analysis.Analyze(tr)
			return &r
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("trace %s did not become available in Tempo", id)
	return nil
}
func TestHealthyEndToEndPropagation(t *testing.T) {
	requireEnabled(t)
	out, status := runCheckout(t, fmt.Sprintf("healthy-%d", time.Now().UnixNano()))
	if status != 200 || out.Status != "paid" || len(out.TraceID) != 32 {
		t.Fatalf("checkout status=%d result=%+v", status, out)
	}
	r := getTrace(t, out.TraceID)
	for _, name := range []string{"gateway", "auth", "orders", "inventory", "payment"} {
		found := false
		for _, svc := range r.Services {
			if svc == name {
				found = true
			}
		}
		if !found {
			t.Errorf("trace missing service %s; got %v", name, r.Services)
		}
	}
}
func TestRealInventoryLock(t *testing.T) {
	requireEnabled(t)
	cmd := exec.Command("bash", "../../scripts/demo-lock.sh")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if e := cmd.Run(); e != nil {
		t.Fatalf("lock demo failed: %v: %s", e, stderr.String())
	}
	var out struct {
		Checkout      checkoutResult `json:"checkout"`
		WaitConfirmed bool           `json:"wait_confirmed"`
		WaitType      string         `json:"wait_type"`
	}
	if e := json.Unmarshal(stdout.Bytes(), &out); e != nil {
		t.Fatalf("invalid scenario response %q: %v", stdout.String(), e)
	}
	if !out.WaitConfirmed || out.WaitType != "Lock" {
		t.Fatalf("runner did not return PostgreSQL wait evidence: %+v", out)
	}
	r := getTrace(t, out.Checkout.TraceID)
	if r.PrimaryFinding == nil || r.PrimaryFinding.Category != "database_lock_wait" || r.PrimaryFinding.Confidence != "confirmed" {
		t.Fatalf("lock finding missing: %+v", r.PrimaryFinding)
	}
}
func TestSlowPaymentNotDatabaseLock(t *testing.T) {
	requireEnabled(t)
	out, status := runCheckout(t, fmt.Sprintf("slow-payment-%d", time.Now().UnixNano()))
	if status != 200 || out.DurationMS < 4000 {
		t.Fatalf("expected slow payment checkout; status=%d result=%+v", status, out)
	}
	r := getTrace(t, out.TraceID)
	if r.PrimaryFinding != nil && r.PrimaryFinding.Category == "database_lock_wait" {
		t.Fatalf("slow payment mislabeled as DB contention: %+v", r.PrimaryFinding)
	}
}
func TestUpstreamTimeout(t *testing.T) {
	requireEnabled(t)
	_, status := runCheckout(t, fmt.Sprintf("upstream-timeout-%d", time.Now().UnixNano()))
	if status != 504 {
		t.Fatalf("timeout status=%d, want 504", status)
	}
}
func TestComparisonReportsMeasuredDifference(t *testing.T) {
	requireEnabled(t)
	a, sa := runCheckout(t, fmt.Sprintf("compare-healthy-%d", time.Now().UnixNano()))
	b, sb := runCheckout(t, fmt.Sprintf("compare-slow-payment-%d", time.Now().UnixNano()))
	if sa != 200 || sb != 200 {
		t.Fatalf("comparison scenarios: %d %d", sa, sb)
	}
	ra, rb := getTrace(t, a.TraceID), getTrace(t, b.TraceID)
	if rb.DurationMS <= ra.DurationMS {
		t.Fatalf("comparison did not show measured latency delta: %.2f <= %.2f", rb.DurationMS, ra.DurationMS)
	}
}
func TestTempoUnavailableDoesNotBlockCheckout(t *testing.T) {
	requireEnabled(t)
	c := tempo.New("http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := c.Health(ctx); e == nil {
		t.Fatal("expected Tempo connection error")
	}
	out, status := runCheckout(t, fmt.Sprintf("tempo-down-%d", time.Now().UnixNano()))
	if status != 200 || out.Status != "paid" {
		t.Fatalf("checkout depended on Tempo: status=%d result=%+v", status, out)
	}
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func TestServicesUseNonSuperuserDatabaseRole(t *testing.T) {
	requireEnabled(t)
	pool, err := pgxpool.New(context.Background(), env("DEBUGGER_DATABASE_URL", "postgres://debugger:debugger_local@127.0.0.1:5432/debugger?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var superuser bool
	if err = pool.QueryRow(context.Background(), "SELECT rolsuper FROM pg_roles WHERE rolname=current_user").Scan(&superuser); err != nil {
		t.Fatal(err)
	}
	if superuser {
		t.Fatal("debugger database role must not be a superuser")
	}
}
