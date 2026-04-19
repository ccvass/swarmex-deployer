package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"os"
	"os/signal"
	"syscall"

	"github.com/docker/docker/client"

	"github.com/ccvass/swarmex/swarmex-deployer"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	promURL := os.Getenv("PROMETHEUS_URL")
	if promURL == "" {
		promURL = "http://prometheus:9090"
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error("failed to create Docker client", "error", err)
		os.Exit(1)
	}
	defer cli.Close()

	dep := deployer.New(cli, promURL, logger)

	// HTTP API to trigger deployments
	http.Handle("/metrics", promhttp.Handler())
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	http.HandleFunc("/deploy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		serviceID := r.URL.Query().Get("service")
		image := r.URL.Query().Get("image")
		if serviceID == "" || image == "" {
			http.Error(w, "service and image params required", http.StatusBadRequest)
			return
		}
		if err := dep.StartDeployment(r.Context(), serviceID, image); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "deployment started for %s with image %s", serviceID, image)
	})

	go func() {
		logger.Info("API endpoint", "addr", ":8080")
		http.ListenAndServe(":8080", nil)
	}()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("swarmex-deployer starting", "prometheus", promURL)
	<-ctx.Done()
	logger.Info("shutdown complete")
}
