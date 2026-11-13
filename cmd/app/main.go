package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ranajahanzaib/distributed-request-debugger/internal/app"
	"github.com/ranajahanzaib/distributed-request-debugger/internal/telemetry"
)

func main() {
	name := os.Getenv("SERVICE_NAME")
	if name == "" {
		log.Fatal("SERVICE_NAME required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdown, err := telemetry.Init(ctx, name)
	if err != nil {
		log.Printf("telemetry initialization failed: %v", err)
	}
	metricShutdown, metricErr := telemetry.InitMetrics(ctx, name)
	if metricErr != nil {
		log.Printf("metrics initialization failed: %v", metricErr)
	}
	if err = app.Run(ctx, name); err != nil {
		log.Fatal(err)
	}
	if metricShutdown != nil {
		_ = metricShutdown(context.Background())
	}
	if shutdown != nil {
		_ = shutdown(context.Background())
	}
}
