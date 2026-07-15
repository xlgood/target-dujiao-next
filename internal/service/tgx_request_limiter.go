package service

import (
	"context"
	"sync"
	"time"
)

// TGX requests from checkout, manual refresh, and scheduled sync share this
// limiter so a busy checkout cannot bypass the configured upstream limit.
var sharedTGXRequestLimiter = struct {
	sync.Mutex
	next map[uint]time.Time
}{next: make(map[uint]time.Time)}

func waitForTGXRequest(ctx context.Context, connectionID uint, perSecond int) error {
	if perSecond < 1 {
		perSecond = tgxInventoryRateLimitDefault
	}
	interval := time.Second / time.Duration(perSecond)
	sharedTGXRequestLimiter.Lock()
	now := time.Now()
	start := sharedTGXRequestLimiter.next[connectionID]
	if start.Before(now) {
		start = now
	}
	sharedTGXRequestLimiter.next[connectionID] = start.Add(interval)
	sharedTGXRequestLimiter.Unlock()
	if delay := time.Until(start); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func waitTGXRetryBackoff(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
