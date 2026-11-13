package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	dbschema "github.com/ranajahanzaib/distributed-request-debugger/internal/db"
	debuggerscenarios "github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/scenarios"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/tempo"
	debuggerweb "github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/web"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type Checkout struct {
	CustomerID string `json:"customer_id"`
	SKU        string `json:"sku"`
	Quantity   int    `json:"quantity"`
	Key        string `json:"idempotency_key"`
}
type result struct {
	RequestID  string `json:"request_id"`
	OrderID    string `json:"order_id"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	TraceID    string `json:"trace_id"`
	Error      string `json:"error,omitempty"`
}
type svc struct {
	name   string
	db     *pgxpool.Pool
	client *http.Client
	log    *slog.Logger
}

func Run(ctx context.Context, name string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	var db *pgxpool.Pool
	if name == "orders" || name == "inventory" || name == "debugger" {
		defaultDB := "postgres://debugger:debugger_local@postgres:5432/debugger?sslmode=disable"
		if name == "orders" {
			defaultDB = "postgres://orders_app:orders_local@postgres:5432/debugger?sslmode=disable"
		}
		if name == "inventory" {
			defaultDB = "postgres://inventory_app:inventory_local@postgres:5432/debugger?sslmode=disable"
		}
		c, e := pgxpool.ParseConfig(env("DATABASE_URL", defaultDB))
		if e != nil {
			return e
		}
		db, e = pgxpool.NewWithConfig(ctx, c)
		if e != nil {
			return e
		}
		defer db.Close()
		if name == "orders" {
			if e = dbschema.MigrateOrders(ctx, db); e != nil {
				return e
			}
		}
		if name == "inventory" {
			if e = dbschema.MigrateInventory(ctx, db); e != nil {
				return e
			}
		}
	}
	s := &svc{name, db, &http.Client{Timeout: 12 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)}, logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if db != nil && db.Ping(r.Context()) != nil {
			http.Error(w, "db unavailable", 503)
			return
		}
		w.Write([]byte("ok"))
	})
	switch name {
	case "gateway":
		mux.HandleFunc("POST /api/checkout", s.checkout)
	case "auth":
		mux.HandleFunc("POST /authorize", s.auth)
	case "orders":
		mux.HandleFunc("POST /orders", s.create)
		mux.HandleFunc("POST /orders/{id}/finalize", s.finalize)
	case "inventory":
		mux.HandleFunc("POST /reserve", s.reserve)
		mux.HandleFunc("POST /release", s.release)
	case "payment":
		mux.HandleFunc("POST /charge", s.payment)
	case "debugger":
		tc := tempo.New(env("TEMPO_URL", "http://tempo:3200"))
		runner := &debuggerscenarios.Runner{DB: db, Tempo: tc, GatewayURL: env("GATEWAY_URL", "http://gateway:8080"), HTTP: &http.Client{Timeout: 25 * time.Second}, Hold: duration("LOCK_HOLD_DURATION", 7500*time.Millisecond)}
		mux.Handle("/", (&debuggerweb.Server{Tempo: tc, DemoEnabled: env("DEMO_MODE", "") == "true", Runner: runner}).Handler())
	default:
		return fmt.Errorf("unknown service: %s", name)
	}
	server := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: otelhttp.NewHandler(metrics(mux, name), name), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		c, x := context.WithTimeout(context.Background(), 5*time.Second)
		defer x()
		_ = server.Shutdown(c)
	}()
	logger.Info("listening", "service", name, "address", server.Addr)
	e := server.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func (s *svc) auth(w http.ResponseWriter, r *http.Request) {
	var q Checkout
	if decode(r, &q) != nil || q.CustomerID != "customer-demo" {
		http.Error(w, "unauthorized", 401)
		return
	}
	write(w, map[string]bool{"authorized": true})
}
func (s *svc) create(w http.ResponseWriter, r *http.Request) {
	var q Checkout
	if decode(r, &q) != nil {
		http.Error(w, "invalid payload", 400)
		return
	}
	tag, e := s.db.Exec(r.Context(), "INSERT INTO orders.orders(id,customer_id,sku,quantity,status,idempotency_key) VALUES($1,$2,$3,$4,'pending',$5) ON CONFLICT(idempotency_key) DO NOTHING", q.Key, q.CustomerID, q.SKU, q.Quantity, q.Key)
	if e != nil {
		http.Error(w, "order persistence failed", 500)
		return
	}
	var id string
	e = s.db.QueryRow(r.Context(), "SELECT id FROM orders.orders WHERE idempotency_key=$1", q.Key).Scan(&id)
	if e != nil {
		http.Error(w, "order lookup failed", 500)
		return
	}
	var customer, sku, status string
	var quantity int
	e = s.db.QueryRow(r.Context(), "SELECT customer_id,sku,quantity,status FROM orders.orders WHERE id=$1", id).Scan(&customer, &sku, &quantity, &status)
	if e != nil || customer != q.CustomerID || sku != q.SKU || quantity != q.Quantity {
		http.Error(w, "idempotency key conflicts with existing order", 409)
		return
	}
	if tag.RowsAffected() == 0 && status == "pending" {
		http.Error(w, "matching checkout is already in progress", 409)
		return
	}
	write(w, map[string]any{"order_id": id, "status": status, "existing": tag.RowsAffected() == 0})
}
func (s *svc) finalize(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Status string `json:"status"`
	}
	if decode(r, &q) != nil || (q.Status != "paid" && q.Status != "failed") {
		http.Error(w, "invalid status", 400)
		return
	}
	tag, e := s.db.Exec(r.Context(), "UPDATE orders.orders SET status=$1,updated_at=now() WHERE id=$2", q.Status, r.PathValue("id"))
	if e != nil || tag.RowsAffected() != 1 {
		http.Error(w, "order update failed", 500)
		return
	}
	write(w, map[string]string{"status": q.Status})
}
func (s *svc) reserve(w http.ResponseWriter, r *http.Request) {
	var q Checkout
	if decode(r, &q) != nil || q.Key == "" || len(q.Key) > 128 || q.Quantity < 1 {
		http.Error(w, "invalid payload", 400)
		return
	}
	ctx, span := otel.Tracer("inventory").Start(r.Context(), "inventory.db.reserve_stock")
	defer span.End()
	span.SetAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.operation.name", "UPDATE"), attribute.String("db.operation.logical", "inventory.reserve_stock"))
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		span.RecordError(err)
		http.Error(w, "database unavailable", 503)
		return
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		span.RecordError(err)
		http.Error(w, "database unavailable", 503)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, "INSERT INTO inventory.reservations(idempotency_key,sku,quantity,status) VALUES($1,$2,$3,'reserved') ON CONFLICT(idempotency_key) DO NOTHING", q.Key, q.SKU, q.Quantity)
	if err != nil {
		span.RecordError(err)
		http.Error(w, "reservation setup failed", 500)
		return
	}
	if tag.RowsAffected() == 0 {
		var sku, status string
		var quantity int
		err = tx.QueryRow(ctx, "SELECT sku,quantity,status FROM inventory.reservations WHERE idempotency_key=$1", q.Key).Scan(&sku, &quantity, &status)
		if err != nil || sku != q.SKU || quantity != q.Quantity {
			http.Error(w, "idempotency key conflicts with existing reservation", 409)
			return
		}
		if status != "reserved" {
			http.Error(w, "reservation was already released", 409)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			http.Error(w, "reservation confirmation failed", 500)
			return
		}
		write(w, map[string]any{"reserved": true, "duplicate": true})
		return
	}
	var pid int
	if err = conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		span.RecordError(err)
		http.Error(w, "database unavailable", 503)
		return
	}
	obs := make(chan waitEvidence, 1)
	done := make(chan struct{})
	stop := make(chan struct{})
	if env("WAIT_OBSERVER_ENABLED", "true") == "true" {
		go s.observe(ctx, pid, obs, stop, done)
	} else {
		close(done)
		obs <- waitEvidence{}
	}
	t := time.Now()
	var left int
	e := tx.QueryRow(ctx, "UPDATE inventory.stock SET available=available-$1,updated_at=now() WHERE sku=$2 AND available >= $1 RETURNING available", q.Quantity, q.SKU).Scan(&left)
	dur := time.Since(t)
	close(stop)
	<-done
	evidence := <-obs
	if e != nil {
		if e == pgx.ErrNoRows {
			http.Error(w, "insufficient stock or unknown sku", 409)
		} else {
			span.RecordError(e)
			span.SetStatus(codes.Error, e.Error())
			http.Error(w, "reservation failed", 500)
		}
		return
	}
	if e = tx.Commit(ctx); e != nil {
		span.RecordError(e)
		http.Error(w, "reservation commit failed", 500)
		return
	}
	span.SetAttributes(attribute.Int64("db.operation.duration_ms", dur.Milliseconds()), attribute.Int("db.wait.observation_count", evidence.Samples))
	if evidence.Samples > 0 {
		span.SetAttributes(attribute.String("db.wait.type", evidence.Type), attribute.String("db.wait.event", evidence.Event), attribute.String("db.wait.blocking_pids", evidence.Blockers), attribute.Int64("db.wait.observed_estimate_ms", int64(evidence.Samples)*40))
	}
	write(w, map[string]any{"reserved": true, "duration_ms": dur.Milliseconds(), "wait_samples": evidence.Samples, "wait_type": evidence.Type, "wait_event": evidence.Event, "blocking_pids": evidence.Blockers, "backend_pid": pid})
}

func (s *svc) release(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Key string `json:"idempotency_key"`
	}
	if decode(r, &q) != nil || q.Key == "" {
		http.Error(w, "invalid payload", 400)
		return
	}
	tx, e := s.db.Begin(r.Context())
	if e != nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var sku string
	var qty int
	e = tx.QueryRow(r.Context(), "UPDATE inventory.reservations SET status='released',updated_at=now() WHERE idempotency_key=$1 AND status='reserved' RETURNING sku,quantity", q.Key).Scan(&sku, &qty)
	if e == pgx.ErrNoRows {
		write(w, map[string]bool{"released": false})
		return
	}
	if e != nil {
		http.Error(w, "reservation release failed", 500)
		return
	}
	if _, e = tx.Exec(r.Context(), "UPDATE inventory.stock SET available=available+$1,updated_at=now() WHERE sku=$2", qty, sku); e != nil {
		http.Error(w, "stock compensation failed", 500)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		http.Error(w, "reservation release failed", 500)
		return
	}
	write(w, map[string]bool{"released": true})
}

type waitEvidence struct {
	Samples               int
	Type, Event, Blockers string
}

func (s *svc) observe(ctx context.Context, pid int, result chan<- waitEvidence, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	evidence := waitEvidence{}
	defer func() { result <- evidence }()
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-tick.C:
			var typ, event string
			var blockers []int
			e := s.db.QueryRow(ctx, "SELECT COALESCE(wait_event_type,''),COALESCE(wait_event,''),pg_blocking_pids(pid) FROM pg_stat_activity WHERE pid=$1", pid).Scan(&typ, &event, &blockers)
			if e == nil && typ != "" {
				if typ == "Lock" && len(blockers) > 0 {
					evidence.Samples++
					evidence.Type = typ
					evidence.Event = event
					evidence.Blockers = fmt.Sprint(blockers)
					s.log.Info("postgres wait observed", "pid", pid, "wait_type", typ, "wait_event", event, "blocking_pids", blockers)
				}
			}
		}
	}
}
func (s *svc) payment(w http.ResponseWriter, r *http.Request) {
	var q Checkout
	if decode(r, &q) != nil {
		http.Error(w, "invalid payload", 400)
		return
	}
	if env("DEMO_MODE", "") == "true" && (strings.HasPrefix(q.Key, "slow-payment-") || strings.HasPrefix(q.Key, "upstream-timeout-")) {
		delay := duration("PAYMENT_DELAY", 5*time.Second)
		if strings.HasPrefix(q.Key, "upstream-timeout-") {
			delay = 20 * time.Second
		}
		select {
		case <-r.Context().Done():
			http.Error(w, "timeout", 504)
			return
		case <-time.After(delay):
		}
	}
	if env("DEMO_MODE", "") == "true" && strings.Contains(q.Key, "fail-payment") {
		http.Error(w, "payment declined", 402)
		return
	}
	write(w, map[string]bool{"paid": true})
}
func (s *svc) checkout(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx, span := otel.Tracer("gateway").Start(r.Context(), "checkout.process")
	defer span.End()
	tid := ""
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		tid = sc.TraceID().String()
		w.Header().Set("X-Trace-ID", tid)
	}
	var q Checkout
	if decode(r, &q) != nil || q.CustomerID == "" || q.SKU == "" || q.Quantity < 1 || q.Quantity > 20 || q.Key == "" {
		http.Error(w, "invalid checkout payload", 400)
		return
	}
	if !validKey(q.Key) {
		http.Error(w, "idempotency_key exceeds 128 characters", 400)
		return
	}
	if env("DEMO_MODE", "") == "true" && strings.HasPrefix(q.Key, "upstream-timeout-") {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	body, _ := json.Marshal(q)
	req := fmt.Sprintf("request-%d", start.UnixNano())
	for _, u := range []string{env("AUTH_URL", "http://auth:8080") + "/authorize"} {
		var e error
		if env("DEMO_MODE", "") == "true" && strings.HasPrefix(q.Key, "missing-propagation-") {
			e = postWith(ctx, &http.Client{Timeout: 12 * time.Second}, u, body, 200, nil)
		} else {
			e = s.post(ctx, u, body, 200, nil)
		}
		if e != nil {
			span.RecordError(e)
			span.SetStatus(codes.Error, e.Error())
			writeStatus(w, 502, result{RequestID: req, Status: "failed", DurationMS: time.Since(start).Milliseconds(), TraceID: tid, Error: e.Error()})
			return
		}
	}
	var order struct {
		ID       string `json:"order_id"`
		Status   string `json:"status"`
		Existing bool   `json:"existing"`
	}
	if e := s.post(ctx, env("ORDERS_URL", "http://orders:8080")+"/orders", body, 200, &order); e != nil {
		span.RecordError(e)
		span.SetStatus(codes.Error, e.Error())
		http.Error(w, "order creation failed: "+e.Error(), 502)
		return
	}
	if order.Existing && order.Status != "pending" {
		write(w, result{RequestID: req, OrderID: order.ID, Status: order.Status, DurationMS: time.Since(start).Milliseconds(), TraceID: tid})
		return
	}
	inv := map[string]any{}
	e := s.post(ctx, env("INVENTORY_URL", "http://inventory:8080")+"/reserve", body, 200, &inv)
	if e != nil {
		compctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		compensation, _ := json.Marshal(map[string]string{"idempotency_key": q.Key})
		_ = s.post(compctx, env("INVENTORY_URL", "http://inventory:8080")+"/release", compensation, 200, nil)
		_ = s.post(compctx, env("ORDERS_URL", "http://orders:8080")+"/orders/"+order.ID+"/finalize", []byte("{\"status\":\"failed\"}"), 200, nil)
		span.RecordError(e)
		span.SetStatus(codes.Error, e.Error())
		code := 502
		if errors.Is(e, context.DeadlineExceeded) {
			code = 504
		}
		writeStatus(w, code, result{RequestID: req, OrderID: order.ID, Status: "failed", DurationMS: time.Since(start).Milliseconds(), TraceID: tid, Error: e.Error()})
		return
	}
	e = s.post(ctx, env("PAYMENT_URL", "http://payment:8080")+"/charge", body, 200, nil)
	status := "paid"
	if e != nil {
		status = "failed"
		span.RecordError(e)
		span.SetStatus(codes.Error, e.Error())
		compctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		compensation, _ := json.Marshal(map[string]string{"idempotency_key": q.Key})
		_ = s.post(compctx, env("INVENTORY_URL", "http://inventory:8080")+"/release", compensation, 200, nil)
		_ = s.post(compctx, env("ORDERS_URL", "http://orders:8080")+"/orders/"+order.ID+"/finalize", []byte("{\"status\":\"failed\"}"), 200, nil)
		cancel()
		code := 502
		if errors.Is(e, context.DeadlineExceeded) {
			code = 504
		}
		writeStatus(w, code, result{RequestID: req, OrderID: order.ID, Status: status, DurationMS: time.Since(start).Milliseconds(), TraceID: tid, Error: e.Error()})
		return
	}
	if finalizeErr := s.post(ctx, env("ORDERS_URL", "http://orders:8080")+"/orders/"+order.ID+"/finalize", []byte(fmt.Sprintf("{\"status\":%q}", status)), 200, nil); finalizeErr != nil {
		span.RecordError(finalizeErr)
		span.SetStatus(codes.Error, finalizeErr.Error())
		http.Error(w, "order finalization failed", 502)
		return
	}
	code := 200
	if e != nil {
		code = 502
	}
	writeStatus(w, code, result{RequestID: req, OrderID: order.ID, Status: status, DurationMS: time.Since(start).Milliseconds(), TraceID: tid, Error: errString(e)})
}
func (s *svc) post(ctx context.Context, url string, b []byte, want int, out any) error {
	return postWith(ctx, s.client, url, b, want, out)
}
func postWith(ctx context.Context, client *http.Client, url string, b []byte, want int, out any) error {
	req, e := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return fmt.Errorf("request body exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}
func write(w http.ResponseWriter, v any) {
	writeStatus(w, http.StatusOK, v)
}
func writeStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
func validKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
func env(k, d string) string {
	if x := os.Getenv(k); x != "" {
		return x
	}
	return d
}
func duration(k string, d time.Duration) time.Duration {
	if x := os.Getenv(k); x != "" {
		if v, e := time.ParseDuration(x); e == nil {
			return v
		}
	}
	return d
}

func metrics(next http.Handler, service string) http.Handler {
	meter := otel.Meter("distributed-request-debugger/http")
	requests, _ := meter.Int64Counter("http.server.request.count")
	failures, _ := meter.Int64Counter("http.server.request.error.count")
	duration, _ := meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusResponseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		attrs := metric.WithAttributes(attribute.String("service.name", service), attribute.String("http.route", route), attribute.Int("http.response.status_code", sw.status))
		requests.Add(r.Context(), 1, attrs)
		duration.Record(r.Context(), time.Since(start).Seconds(), attrs)
		if sw.status >= 500 {
			failures.Add(r.Context(), 1, attrs)
		}
		fields := []any{"service", service, "route", route, "status", sw.status, "duration_ms", time.Since(start).Milliseconds()}
		if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
			fields = append(fields, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
		}
		slog.Default().Info("http request completed", fields...)
	})
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
