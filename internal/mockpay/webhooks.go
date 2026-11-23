package mockpay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sheshakanthra/sluice/internal/mockpay/mpdb"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// retrySchedule is the delay before each delivery attempt (API.md §5):
// 0, 1, 2, 4, 8, 16 s; six attempts, then the delivery is dead.
var retrySchedule = []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

const (
	dispatchBatch = 64
	// leaseFor keeps a claimed row away from other dispatchers while its HTTP
	// call is in flight. Longer than WebhookTimeout.
	leaseFor = 30 * time.Second
	// minReorderDelay is how long a reordered event is held back so that
	// later events overtake it.
	minReorderDelay = 500 * time.Millisecond
)

// enqueueWebhook writes the event for in's new state to the outbox
// (webhook_deliveries) inside the caller's transaction. Faults: dup inserts
// a second row with the same event id; reorder holds this event back so a
// later one is delivered first; MaxDelay adds a random delay.
func (s *Server) enqueueWebhook(ctx context.Context, q *mpdb.Queries, in payments.Intent, f faults) error {
	typ := payments.EventTypeFor(in.Status)
	if typ == "" {
		return nil
	}
	now := s.o.Now()
	ev := payments.WebhookEvent{ID: newID("evt_"), Type: typ, Created: now.Unix(), Data: payments.WebhookData{Intent: in}}
	ev.Data.Intent.CheckoutURL = ""
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode webhook: %w", err)
	}
	//nolint:gosec // G404: fault injection, not security
	dup := f.dup || (s.o.WebhookDupRate > 0 && rand.Float64() < s.o.WebhookDupRate)
	reorder := f.reorder || (s.o.WebhookReorder > 0 && rand.Float64() < s.o.WebhookReorder) //nolint:gosec // G404
	delay := s.randomDelay()
	if reorder {
		s.metrics.faults.WithLabelValues(FaultReorderWebhook).Inc()
		delay += max(2*s.o.WebhookMaxDelay, minReorderDelay)
	}
	copies := 1
	if dup {
		s.metrics.faults.WithLabelValues(FaultDupWebhook).Inc()
		copies = 2
	}
	for i := range copies {
		if err := q.InsertDelivery(ctx, mpdb.InsertDeliveryParams{
			EventID: ev.ID, IntentID: in.ID, Type: typ, Payload: payload,
			NextAttemptAt: now.Add(delay + time.Duration(i)*10*time.Millisecond),
		}); err != nil {
			return fmt.Errorf("enqueue webhook: %w", err)
		}
	}
	return nil
}

func (s *Server) randomDelay() time.Duration {
	if s.o.WebhookMaxDelay <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(s.o.WebhookMaxDelay) + 1)) //nolint:gosec // G404: simulated delay
}

// RunDispatcher delivers due webhooks every DispatchInterval until ctx is
// done. Several dispatchers may run against one database.
func (s *Server) RunDispatcher(ctx context.Context) error {
	t := time.NewTicker(s.o.DispatchInterval)
	defer t.Stop()
	for {
		for {
			n, err := s.DispatchOnce(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				s.o.Log.ErrorContext(ctx, "webhook dispatch", slog.String("error", err.Error()))
				break
			}
			if n < dispatchBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// DispatchOnce claims up to one batch of due deliveries and sends them
// concurrently (at most DispatchWorkers at a time). It returns how many
// were claimed.
func (s *Server) DispatchOnce(ctx context.Context) (int, error) {
	now := s.o.Now()
	rows, err := s.q.ClaimDueDeliveries(ctx, mpdb.ClaimDueDeliveriesParams{
		LeaseUntil: now.Add(leaseFor), MaxAttempts: int32(len(retrySchedule)), Now: now, BatchSize: dispatchBatch,
	})
	if err != nil {
		return 0, fmt.Errorf("claim deliveries: %w", err)
	}
	sem := make(chan struct{}, s.o.DispatchWorkers)
	var wg sync.WaitGroup
	for _, d := range rows {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			s.deliver(ctx, d)
		})
	}
	wg.Wait()
	return len(rows), nil
}

func (s *Server) deliver(ctx context.Context, d mpdb.WebhookDelivery) {
	err := s.post(ctx, d)
	// Record the outcome even if ctx is ending, so a delivered event is not
	// sent again after a restart.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err == nil {
		s.metrics.deliveries.WithLabelValues("delivered").Inc()
		if err := s.q.MarkDelivered(rctx, d.ID); err != nil {
			s.o.Log.ErrorContext(rctx, "mark delivered", slog.String("error", err.Error()))
		}
		return
	}
	next := int(d.Attempt) + 1
	result := "failed"
	nextAt := s.o.Now()
	if next < len(retrySchedule) {
		nextAt = nextAt.Add(retrySchedule[next])
	} else {
		result = "dead"
	}
	s.metrics.deliveries.WithLabelValues(result).Inc()
	msg := err.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if err := s.q.MarkDeliveryFailed(rctx, mpdb.MarkDeliveryFailedParams{ID: d.ID, NextAttemptAt: nextAt, LastError: &msg}); err != nil {
		s.o.Log.ErrorContext(rctx, "mark delivery failed", slog.String("error", err.Error()))
	}
	s.o.Log.WarnContext(ctx, "webhook delivery failed", slog.String("event_id", d.EventID),
		slog.Int("attempt", next), slog.String("result", result), slog.String("error", msg))
}

// post sends one signed delivery. Only a 2xx counts as delivered.
func (s *Server) post(ctx context.Context, d mpdb.WebhookDelivery) error {
	ctx, cancel := context.WithTimeout(ctx, s.o.WebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.o.WebhookURL, bytes.NewReader(d.Payload))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(payments.SignatureHeader, payments.Sign(s.o.WebhookSecret, s.o.Now(), d.Payload))
	req.Header.Set(payments.EventIDHeader, d.EventID)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
