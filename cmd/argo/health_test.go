package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestHealthHandler(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		body string
	}{{nil, 200, "ok\n"}, {errors.New("down"), 503, "unavailable\n"}} {
		srv := httptest.NewServer(healthHandler(func(context.Context) error { return tc.err }))
		resp, err := http.Get(srv.URL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		if resp.StatusCode != tc.code || string(buf[:n]) != tc.body {
			t.Errorf("%v: %d %q", tc.err, resp.StatusCode, buf[:n])
		}
		if r, _ := http.Get(srv.URL + "/other"); r.StatusCode != 404 {
			t.Errorf("another path answered %d", r.StatusCode)
		}
		srv.Close()
	}
}

func TestServeAndCheckHealth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	healthy := true
	serveHealth(ctx, port, func(context.Context) error {
		if !healthy {
			return errors.New("down")
		}
		return nil
	})
	t.Setenv("HEALTH_PORT", strconv.Itoa(port))
	if code := checkHealth(); code != 0 {
		t.Fatalf("healthy bot: exit %d", code)
	}
	healthy = false
	if code := checkHealth(); code != 1 {
		t.Fatalf("unhealthy bot: exit %d", code)
	}
	t.Setenv("HEALTH_PORT", "")
	if code := checkHealth(); code != 1 {
		t.Fatalf("no port: exit %d", code)
	}
}
