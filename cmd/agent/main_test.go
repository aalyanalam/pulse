package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"pulse/internal/telemetry"
	"testing"
	"time"
)

type handlerTransport struct{ handler http.Handler }

func (tr handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	w := httptest.NewRecorder()
	tr.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestCollectAndSend(t *testing.T) {
	app := &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(telemetry.Stats{Version: "v1", AppProcessID: "process-1", Requests: 7})
	})}}
	stats, err := collect(context.Background(), app, "http://sampleapp")
	if err != nil {
		t.Fatal(err)
	}
	api := &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b telemetry.Batch
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Error(err)
		}
		if err := b.Validate(); err != nil {
			t.Error(err)
		}
		if len(b.Samples) != 1 || b.Samples[0].Requests != 7 || b.Samples[0].AppProcessID != "process-1" {
			t.Error("measurement lost")
		}
		w.WriteHeader(202)
	})}}
	err = send(context.Background(), api, "http://backend", []telemetry.Sample{{NodeID: "n", SessionID: "s", Sequence: 1, CollectedAt: time.Now(), Stats: stats}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCollectFailure(t *testing.T) {
	server := &http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })}}
	if _, err := collect(context.Background(), server, "http://sampleapp"); err == nil {
		t.Fatal("accepted upstream error")
	}
}
