package core

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Speed test of the server the bot runs on, used by /speedtest (core/speedtest.py).
//
// Measures against Cloudflare's public test endpoints: latency (median of five tiny requests on a warm connection),
// download (four parallel streams of 25 MB) and upload (four parallel posts of 10 MB).

const (
	SpeedtestURL  = "https://speed.cloudflare.com"
	speedStreams  = 4
	downloadBytes = 25_000_000
	uploadBytes   = 10_000_000
	pingRounds    = 5
)

// Speedtest returns the ping in ms and the download and upload speeds in Mbps.
func Speedtest(ctx context.Context, base string) (ping, download, upload float64, err error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client := &http.Client{}

	get := func(url string) (int64, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return io.Copy(io.Discard, resp.Body)
	}

	if _, err = get(base + "/__down?bytes=0"); err != nil { // opens the connection, not measured
		return
	}
	var pings []float64
	for i := 0; i < pingRounds; i++ {
		started := time.Now()
		if _, err = get(base + "/__down?bytes=0"); err != nil {
			return
		}
		pings = append(pings, float64(time.Since(started))/float64(time.Millisecond))
	}
	sort.Float64s(pings)
	ping = pings[len(pings)/2]

	parallel := func(job func() (int64, error)) (float64, error) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var total int64
		var first error
		started := time.Now()
		for i := 0; i < speedStreams; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				n, err := job()
				mu.Lock()
				defer mu.Unlock()
				if err != nil && first == nil {
					first = err
				}
				total += n
			}()
		}
		wg.Wait()
		if first != nil {
			return 0, first
		}
		return float64(total) * 8 / time.Since(started).Seconds() / 1_000_000, nil
	}

	download, err = parallel(func() (int64, error) { return get(fmt.Sprintf("%s/__down?bytes=%d", base, downloadBytes)) })
	if err != nil {
		return
	}
	payload := make([]byte, uploadBytes)
	upload, err = parallel(func() (int64, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/__up", bytes.NewReader(payload))
		if err != nil {
			return 0, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return int64(len(payload)), nil
	})
	return
}
