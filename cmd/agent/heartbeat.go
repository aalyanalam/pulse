package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func heartbeats(ctx context.Context, client *http.Client, backendURL, nodeID, appURL string) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		healthy := applicationHealthy(ctx, client, appURL)
		req, err := http.NewRequestWithContext(ctx, "POST", backendURL+"/api/v1/nodes/"+url.PathEscape(nodeID)+"/heartbeat", strings.NewReader(fmt.Sprintf(`{"application_healthy":%t}`, healthy)))
		if err == nil {
			var response *http.Response
			response, err = client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				response.Body.Close()
				if response.StatusCode != http.StatusOK {
					err = ingestStatus(response.StatusCode)
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("heartbeat failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Bound application probes so a hung application cannot suppress agent heartbeats.
func applicationHealthy(parent context.Context, client *http.Client, appURL string) bool {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", appURL+"/healthz", nil)
	if err != nil {
		return false
	}
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
