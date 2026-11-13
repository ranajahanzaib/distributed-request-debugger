package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/analysis"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/tempo"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/trace"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/debugger/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	client := tempo.New(env("TEMPO_URL", "http://127.0.0.1:3200"))
	switch os.Args[1] {
	case "serve":
		mux := (&web.Server{Tempo: client}).Handler()
		addr := env("DEBUGGER_ADDR", "127.0.0.1:8081")
		fmt.Println("debugger listening on", addr)
		if e := http.ListenAndServe(addr, mux); e != nil {
			fatal(e)
		}
	case "diagnose":
		f := flag.NewFlagSet("diagnose", flag.ExitOnError)
		id := f.String("trace-id", "", "trace ID")
		asJSON := f.Bool("json", false, "write JSON")
		_ = f.Parse(os.Args[2:])
		if *id == "" {
			fatal(fmt.Errorf("--trace-id is required"))
		}
		t := fetch(client, *id)
		report := analysis.Analyze(t)
		output(report, *asJSON)
	case "compare":
		f := flag.NewFlagSet("compare", flag.ExitOnError)
		before := f.String("before", "", "baseline trace ID")
		after := f.String("after", "", "comparison trace ID")
		_ = f.Parse(os.Args[2:])
		if *before == "" || *after == "" {
			fatal(fmt.Errorf("--before and --after are required"))
		}
		a, b := fetch(client, *before), fetch(client, *after)
		ra, rb := analysis.Analyze(a), analysis.Analyze(b)
		out := web.Compare(ra, rb, a, b)
		pretty(out)
	case "demo":
		runDemo(os.Args[2:], client)
	default:
		usage()
		os.Exit(2)
	}
}
func runDemo(args []string, c *tempo.Client) {
	if len(args) < 2 || args[0] != "run" {
		fatal(fmt.Errorf("usage: debugger demo run <healthy|inventory-db-lock|slow-payment|upstream-timeout|missing-propagation>"))
	}
	scenario := args[1]
	allowed := map[string]bool{"healthy": true, "inventory-db-lock": true, "slow-payment": true, "upstream-timeout": true, "missing-propagation": true}
	if !allowed[scenario] {
		fatal(fmt.Errorf("unsupported scenario %q", scenario))
	}
	body := fmt.Sprintf(`{"scenario":%q}`, scenario)
	debuggerURL := env("DEBUGGER_URL", "http://127.0.0.1:8081")
	reqCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(reqCtx, http.MethodPost, debuggerURL+"/api/demo/run", strings.NewReader(body))
	if e != nil {
		fatal(e)
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.HasPrefix(debuggerURL, "http://127.0.0.1") {
		req.Header.Set("Origin", "http://127.0.0.1:8081")
	} else {
		req.Header.Set("Origin", "http://localhost:8081")
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		fatal(e)
	}
	defer resp.Body.Close()
	var result map[string]any
	if e = json.NewDecoder(resp.Body).Decode(&result); e != nil {
		fatal(e)
	}
	if resp.StatusCode >= 400 {
		fatal(fmt.Errorf("scenario API returned %s: %v", resp.Status, result))
	}
	pretty(result)
}
func fetch(c *tempo.Client, id string) *trace.Trace {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t, e := c.Fetch(ctx, id)
	if e != nil {
		fatal(e)
	}
	return t
}
func fetchRetry(c *tempo.Client, id string, limit time.Duration) *trace.Trace {
	deadline := time.Now().Add(limit)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t, e := c.Fetch(ctx, id)
		cancel()
		if e == nil {
			return t
		}
		if time.Now().After(deadline) {
			fatal(e)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
func output(v any, asJSON bool) {
	if asJSON {
		pretty(v)
		return
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		fatal(e)
	}
	fmt.Println(string(b))
}
func pretty(v any) {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		fatal(e)
	}
	fmt.Println(string(b))
}
func env(k, d string) string {
	if x := os.Getenv(k); x != "" {
		return x
	}
	return d
}
func fatal(e error) { fmt.Fprintln(os.Stderr, "debugger:", e); os.Exit(1) }
func usage()        { fmt.Fprintln(os.Stderr, "usage: debugger <serve|diagnose|compare|demo>") }
