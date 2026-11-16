package main

import (
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	"pulse/internal/telemetry"
)

func TestDeliveryRecoversAndDrainsQueue(t *testing.T) {
	queue := make(chan telemetry.Sample, 2)
	queue <- telemetry.Sample{SessionID: "agent-session", Sequence: 1}
	queue <- telemetry.Sample{SessionID: "agent-session", Sequence: 2}
	started := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan deliveryCounts, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var sent [][]telemetry.Sample
	go func() {
		done <- deliver(ctx, queue, 2, time.Hour, func(ctx context.Context, batch []telemetry.Sample) error {
			sent = append(sent, append([]telemetry.Sample(nil), batch...))
			if len(sent) == 1 {
				close(started)
				select {
				case <-resume:
				case <-ctx.Done():
				}
				return io.EOF
			}
			return nil
		})
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("full batch was not sent")
	}
	for i := int64(3); i <= 5; i++ {
		if dropped := enqueue(queue, telemetry.Sample{Sequence: i}); dropped != (i == 5) {
			t.Errorf("sample %d: dropped = %t", i, dropped)
		}
	}
	close(queue)
	close(resume)
	counts := <-done
	if counts.retries != 1 || counts.dropped != 0 || len(sent) != 3 {
		t.Fatalf("delivery totals %+v, attempts %v", counts, sent)
	}
	if !reflect.DeepEqual(sent[0], sent[1]) || sent[2][0].Sequence != 4 || sent[2][1].Sequence != 5 {
		t.Fatalf("retry or overflow changed the expected samples: %v", sent)
	}
}

func TestDeliveryCancellationCountsPendingSamples(t *testing.T) {
	queue := make(chan telemetry.Sample, 2)
	queue <- telemetry.Sample{Sequence: 1}
	queue <- telemetry.Sample{Sequence: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counts := deliver(ctx, queue, 1, time.Hour, func(ctx context.Context, batch []telemetry.Sample) error {
		cancel()
		return ctx.Err()
	})
	if counts.dropped != 2 || counts.retries != 0 {
		t.Fatalf("delivery totals %+v", counts)
	}
}

func TestDeliveryFlushesPartialBatch(t *testing.T) {
	queue := make(chan telemetry.Sample, 1)
	queue <- telemetry.Sample{Sequence: 1}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var sequences []int64
	deliver(ctx, queue, 2, 10*time.Millisecond, func(ctx context.Context, batch []telemetry.Sample) error {
		for _, sample := range batch {
			sequences = append(sequences, sample.Sequence)
		}
		cancel()
		return nil
	})
	if !reflect.DeepEqual(sequences, []int64{1}) {
		t.Fatalf("interval flush sent %v", sequences)
	}
}
