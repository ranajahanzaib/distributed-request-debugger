package scenarios

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/analysis"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/tempo"
)

type Checkout struct {
	RequestID  string `json:"request_id"`
	OrderID    string `json:"order_id"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	TraceID    string `json:"trace_id"`
	Error      string `json:"error,omitempty"`
}
type Result struct {
	Scenario      string           `json:"scenario"`
	Checkout      Checkout         `json:"checkout"`
	WaitConfirmed bool             `json:"wait_confirmed"`
	WaitType      string           `json:"wait_type,omitempty"`
	WaitEvent     string           `json:"wait_event,omitempty"`
	BlockingPIDs  []int            `json:"blocking_pids,omitempty"`
	WaitSamples   int              `json:"wait_samples,omitempty"`
	Diagnosis     *analysis.Report `json:"diagnosis,omitempty"`
	Warnings      []string         `json:"warnings,omitempty"`
}
type waitEvidence struct {
	Type, Event string
	Blockers    []int
	Samples     int
}
type Runner struct {
	DB         *pgxpool.Pool
	Tempo      *tempo.Client
	GatewayURL string
	HTTP       *http.Client
	Hold       time.Duration
}

func (r *Runner) Run(ctx context.Context, name string) (Result, error) {
	if name != "healthy" && name != "inventory-db-lock" && name != "slow-payment" && name != "upstream-timeout" && name != "missing-propagation" {
		return Result{}, fmt.Errorf("scenario %q is not allowed", name)
	}
	result := Result{Scenario: name}
	key := strings.ReplaceAll(name, "_", "-") + "-" + fmt.Sprint(time.Now().UnixNano())
	if name == "inventory-db-lock" {
		checkout, evidence, e := r.runLocked(ctx, key)
		result.Checkout = checkout
		if e != nil {
			return result, e
		}
		result.WaitConfirmed = true
		result.WaitType = evidence.Type
		result.WaitEvent = evidence.Event
		result.BlockingPIDs = evidence.Blockers
		result.WaitSamples = evidence.Samples
	} else {
		prefix := map[string]string{"healthy": "healthy", "slow-payment": "slow-payment", "upstream-timeout": "upstream-timeout", "missing-propagation": "missing-propagation"}[name]
		out, e := r.checkout(ctx, prefix+"-"+fmt.Sprint(time.Now().UnixNano()))
		result.Checkout = out
		if e != nil {
			return result, e
		}
	}
	if result.Checkout.TraceID != "" {
		if report, e := r.diagnose(ctx, result.Checkout.TraceID); e == nil {
			result.Diagnosis = report
		} else {
			result.Warnings = append(result.Warnings, "checkout completed but trace diagnosis is not yet available: "+e.Error())
		}
	}
	return result, nil
}
func (r *Runner) runLocked(ctx context.Context, key string) (Checkout, waitEvidence, error) {
	if r.DB == nil {
		return Checkout{}, waitEvidence{}, fmt.Errorf("database connection unavailable")
	}
	conn, e := r.DB.Acquire(ctx)
	if e != nil {
		return Checkout{}, waitEvidence{}, e
	}
	defer conn.Release()
	tx, e := conn.Begin(ctx)
	if e != nil {
		return Checkout{}, waitEvidence{}, e
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()
	var sku string
	if e = tx.QueryRow(ctx, "SELECT sku FROM inventory.stock WHERE sku=$1 FOR UPDATE", "SKU-001").Scan(&sku); e != nil {
		return Checkout{}, waitEvidence{}, e
	}
	checkoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	type response struct {
		Body Checkout
		Err  error
	}
	done := make(chan response, 1)
	go func() { out, err := r.checkout(checkoutCtx, key); done <- response{out, err} }()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	observed := waitEvidence{}
	for observed.Samples == 0 {
		select {
		case <-ctx.Done():
			cancel()
			return Checkout{}, waitEvidence{}, ctx.Err()
		case <-deadline.C:
			cancel()
			return Checkout{}, waitEvidence{}, fmt.Errorf("inventory query did not enter a confirmed PostgreSQL lock wait")
		case <-ticker.C:
			var pid int
			var blockers []int
			e = r.DB.QueryRow(ctx, "SELECT pid,wait_event_type,COALESCE(wait_event,''),pg_blocking_pids(pid) FROM pg_stat_activity WHERE wait_event_type='Lock' AND cardinality(pg_blocking_pids(pid))>0 AND query LIKE 'UPDATE inventory.stock%' ORDER BY query_start LIMIT 1").Scan(&pid, &observed.Type, &observed.Event, &blockers)
			if e == nil && len(blockers) > 0 {
				observed.Blockers = blockers
				observed.Samples++
				_ = pid
			}
		}
	}
	if observed.Type != "Lock" || len(observed.Blockers) == 0 {
		return Checkout{}, waitEvidence{}, fmt.Errorf("PostgreSQL wait did not include blocking PID evidence")
	}
	holdDuration := r.Hold
	if holdDuration <= 0 {
		holdDuration = 7500 * time.Millisecond
	}
	hold := time.NewTimer(holdDuration)
	select {
	case <-ctx.Done():
		hold.Stop()
		cancel()
		return Checkout{}, waitEvidence{}, ctx.Err()
	case <-hold.C:
	}
	if e = tx.Commit(ctx); e != nil {
		cancel()
		return Checkout{}, waitEvidence{}, e
	}
	committed = true
	select {
	case <-ctx.Done():
		cancel()
		return Checkout{}, waitEvidence{}, ctx.Err()
	case res := <-done:
		return res.Body, observed, res.Err
	}
}
func (r *Runner) checkout(ctx context.Context, key string) (Checkout, error) {
	payload := struct {
		CustomerID string `json:"customer_id"`
		SKU        string `json:"sku"`
		Quantity   int    `json:"quantity"`
		Key        string `json:"idempotency_key"`
	}{"customer-demo", "SKU-001", 1, key}
	body, _ := json.Marshal(payload)
	client := r.HTTP
	if client == nil {
		client = &http.Client{Timeout: 25 * time.Second}
	}
	gatewayURL := r.GatewayURL
	if gatewayURL == "" {
		gatewayURL = "http://gateway:8080"
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/api/checkout", bytes.NewReader(body))
	if e != nil {
		return Checkout{}, e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(req)
	if e != nil {
		return Checkout{}, e
	}
	defer resp.Body.Close()
	var out Checkout
	e = json.NewDecoder(resp.Body).Decode(&out)
	if e != nil {
		return out, e
	}
	if resp.StatusCode >= 500 && out.Status == "" {
		return out, fmt.Errorf("checkout returned %s", resp.Status)
	}
	return out, nil
}
func (r *Runner) diagnose(ctx context.Context, id string) (*analysis.Report, error) {
	end := time.Now().Add(8 * time.Second)
	var last error
	for time.Now().Before(end) {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		t, e := r.Tempo.Fetch(c, id)
		cancel()
		if e == nil {
			v := analysis.Analyze(t)
			return &v, nil
		}
		last = e
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil, last
}
