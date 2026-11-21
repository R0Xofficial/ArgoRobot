package core

import (
	"os"
	"testing"
	"time"
)

// Without a reachable Telegram the connection fails with an error instead of hanging or panicking.
func TestConnectMTProtoOffline(t *testing.T) {
	if os.Getenv("ARGO_SLOW_TESTS") == "" {
		t.Skip("slow: set ARGO_SLOW_TESTS=1")
	}
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)
	start := time.Now()
	c, err := connectMTProto(12345, "0123456789abcdef0123456789abcdef", "1:token")
	if err == nil {
		c.Close()
		t.Fatal("connected without a network")
	}
	t.Log("failed after", time.Since(start), err)
}
