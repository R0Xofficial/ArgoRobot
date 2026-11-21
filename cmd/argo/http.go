package main

import (
	"net/http"
	"time"
)

func httpClient() http.Client {
	return http.Client{Timeout: 0, Transport: &http.Transport{
		MaxIdleConns:        20,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		Proxy:               http.ProxyFromEnvironment,
	}}
}
