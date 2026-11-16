package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"pulse/internal/telemetry"
)

type ingestStatus int

func (s ingestStatus) Error() string { return fmt.Sprintf("ingest returned HTTP %d", s) }

func retryable(err error) bool {
	var status ingestStatus
	if errors.As(err, &status) {
		return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
	}
	// The request may have reached the backend before the connection failed.
	return true
}

// The collector is the only producer. The sender can only free queue slots.
func enqueue(queue chan telemetry.Sample, sample telemetry.Sample) (dropped bool) {
	select {
	case queue <- sample:
		return false
	default:
	}
	select {
	case <-queue:
		dropped = true
	default:
	}
	queue <- sample
	return dropped
}

type deliveryCounts struct {
	retries int
	dropped int
}

func deliver(ctx context.Context, queue <-chan telemetry.Sample, size int, interval time.Duration, post func(context.Context, []telemetry.Sample) error) deliveryCounts {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	batch := make([]telemetry.Sample, 0, size)
	var counts deliveryCounts
	for {
		if ctx.Err() != nil {
			counts.dropped += len(batch) + len(queue)
			return counts
		}
		select {
		case <-ctx.Done():
			continue
		case sample, open := <-queue:
			if !open {
				flushBatch(ctx, batch, post, &counts)
				return counts
			}
			batch = append(batch, sample)
			if len(batch) < size {
				continue
			}
		case <-ticker.C:
		}
		flushBatch(ctx, batch, post, &counts)
		batch = batch[:0]
	}
}

func flushBatch(ctx context.Context, batch []telemetry.Sample, post func(context.Context, []telemetry.Sample) error, counts *deliveryCounts) {
	if len(batch) == 0 {
		return
	}
	for delay := 500 * time.Millisecond; ctx.Err() == nil; delay = min(delay*2, 10*time.Second) {
		err := post(ctx, batch)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			break
		}
		if !retryable(err) {
			counts.dropped += len(batch)
			slog.Warn("backend rejected telemetry batch", "error", err, "sender_dropped_samples", counts.dropped)
			return
		}
		wait := delay/2 + time.Duration(rand.Int63n(int64(delay/2)))
		slog.Warn("telemetry delivery failed; retry scheduled", "error", err, "retry_in", wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
			if ctx.Err() == nil {
				counts.retries++
			}
		}
	}
	counts.dropped += len(batch)
}
