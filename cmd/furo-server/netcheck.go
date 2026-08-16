package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func lookupWithTimeout(host string, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

// publicIP asks api.ipify.org; returns "" on any failure (check is advisory).
func publicIP(timeout time.Duration) string {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
