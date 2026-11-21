package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// A tiny health endpoint for monitoring (UptimeRobot, a Docker HEALTHCHECK, a load balancer). It is off unless
// HEALTH_PORT is set. GET /health answers 200 "ok" when the database answers, else 503. It tells nothing else.

// healthHandler answers 200 when ping succeeds within two seconds.
func healthHandler(ping func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintln(w, "unavailable")
			return
		}
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// serveHealth listens on the port until ctx ends. A port that can't be opened is logged and the bot goes on without it.
func serveHealth(ctx context.Context, port int, ping func(context.Context) error) {
	if port <= 0 {
		return
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		slog.Warn("The health endpoint could not start", "port", port, "err", err)
		return
	}
	srv := &http.Server{Handler: healthHandler(ping), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("Health endpoint", "port", port)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("The health endpoint stopped", "err", err)
		}
	}()
}

// checkHealth is `argo -health`: it asks the running bot's endpoint and exits 0 when it says ok. For a Docker
// HEALTHCHECK, because the image has no curl.
func checkHealth() int {
	port, _ := strconv.Atoi(os.Getenv("HEALTH_PORT"))
	if port <= 0 {
		fmt.Fprintln(os.Stderr, "HEALTH_PORT is not set")
		return 1
	}
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy:", resp.Status)
		return 1
	}
	return 0
}
