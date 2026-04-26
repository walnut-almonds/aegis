package lockclient

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
)

// AcquireOptions configures the behaviour of lock acquisition.
type AcquireOptions struct {
	// Key is the resource name to lock (required).
	Key string
	// TTLSec is the lock time-to-live in seconds (required).
	TTLSec int
	// AutoRenew automatically extends the lock in the background until Released.
	AutoRenew bool
	// RenewThreshold controls when to renew: renew when remaining time < RenewThreshold * TTL.
	// Defaults to 0.5 (renew at 50% of TTL remaining).
	RenewThreshold float64
	// RetryCount is the number of additional attempts if Acquire fails. 0 = no retry.
	RetryCount int
	// RetryInterval is the initial wait between retries. Defaults to 500ms.
	RetryInterval time.Duration
	// RetryMaxInterval caps the backoff delay. When > 0, exponential backoff with
	// full jitter is enabled: delay = rand(0, min(RetryInterval * 2^n, RetryMaxInterval)).
	// When 0, a fixed RetryInterval is used.
	RetryMaxInterval time.Duration
}

// LockResponse is the raw response from the transport layer.
type LockResponse struct {
	Key       string
	Token     string
	ExpiredAt time.Time
}

// Lock represents an acquired distributed lock.
// Use Release to free the lock; Extend to manually prolong it.
type Lock struct {
	Key       string
	ExpiredAt time.Time

	token     string
	ttlSec    int
	transport lockTransport
	stopRenew context.CancelFunc
}

// Token returns the opaque token that proves ownership of this lock.
func (l *Lock) Token() string { return l.token }

// Release releases the lock and stops any running auto-renew goroutine.
func (l *Lock) Release(ctx context.Context) error {
	if l.stopRenew != nil {
		l.stopRenew()
	}
	return l.transport.release(ctx, l.Key, l.token)
}

// Extend manually prolongs the lock TTL by ttlSec seconds.
func (l *Lock) Extend(ctx context.Context, ttlSec int) error {
	return l.transport.extend(ctx, l.Key, l.token, ttlSec)
}

// Client is the high-level distributed lock client interface.
type Client interface {
	// Acquire obtains a lock according to opts.
	// The returned *Lock must be Released when the critical section is done.
	Acquire(ctx context.Context, opts AcquireOptions) (*Lock, error)
	// WithLock acquires a lock, runs fn, then releases the lock.
	// The lock is auto-renewed for the duration of fn.
	WithLock(
		ctx context.Context,
		key string,
		ttl time.Duration,
		fn func(ctx context.Context) error,
	) error
	// Close closes the underlying connection.
	Close() error
}

// lockTransport is the low-level interface implemented by http/grpc transports.
type lockTransport interface {
	acquire(ctx context.Context, key, token string, ttlSec int) (*LockResponse, error)
	release(ctx context.Context, key, token string) error
	extend(ctx context.Context, key, token string, ttlSec int) error
	close() error
}

// baseClient wraps a lockTransport and implements the full Client interface.
type baseClient struct {
	transport lockTransport
}

func newClient(t lockTransport) Client {
	return &baseClient{transport: t}
}

func (c *baseClient) Close() error {
	return c.transport.close()
}

func (c *baseClient) Acquire(ctx context.Context, opts AcquireOptions) (*Lock, error) {
	// Apply defaults.
	if opts.RenewThreshold <= 0 || opts.RenewThreshold >= 1 {
		opts.RenewThreshold = 0.5
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = 500 * time.Millisecond
	}

	// SDK-generated token — callers never need to supply one.
	token := uuid.NewString()

	var (
		res *LockResponse
		err error
	)
	for attempt := 0; attempt <= opts.RetryCount; attempt++ {
		if attempt > 0 {
			delay := retryDelay(opts.RetryInterval, opts.RetryMaxInterval, attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		res, err = c.transport.acquire(ctx, opts.Key, token, opts.TTLSec)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	lock := &Lock{
		Key:       res.Key,
		token:     res.Token,
		ExpiredAt: res.ExpiredAt,
		ttlSec:    opts.TTLSec,
		transport: c.transport,
	}

	if opts.AutoRenew {
		renewCtx, cancel := context.WithCancel(context.Background())
		lock.stopRenew = cancel
		go c.autoRenew(renewCtx, lock, opts)
	}

	return lock, nil
}

// retryDelay computes the wait before the next attempt.
// If maxInterval > 0: exponential backoff with full jitter —
//
//	delay = rand(0, min(base * 2^(attempt-1), maxInterval))
//
// Otherwise: fixed base interval.
func retryDelay(base, maxInterval time.Duration, attempt int) time.Duration {
	if maxInterval <= 0 {
		return base
	}
	exp := base
	for i := 1; i < attempt; i++ {
		exp *= 2
		if exp > maxInterval {
			exp = maxInterval
			break
		}
	}
	// Full jitter: uniform random in [0, exp)
	return time.Duration(rand.Int64N(int64(exp)))
}

func (c *baseClient) autoRenew(ctx context.Context, lock *Lock, opts AcquireOptions) {
	renewIn := time.Duration(float64(opts.TTLSec) * opts.RenewThreshold * float64(time.Second))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(renewIn):
			if err := c.transport.extend(ctx, lock.Key, lock.token, opts.TTLSec); err != nil {
				// Lock may have expired or been forcibly released; stop renewing.
				return
			}
			lock.ExpiredAt = time.Now().Add(time.Duration(opts.TTLSec) * time.Second)
		}
	}
}

func (c *baseClient) WithLock(
	ctx context.Context,
	key string,
	ttl time.Duration,
	fn func(ctx context.Context) error,
) error {
	ttlSec := int(ttl.Seconds())
	if ttlSec <= 0 {
		ttlSec = 30
	}
	lock, err := c.Acquire(ctx, AcquireOptions{
		Key:       key,
		TTLSec:    ttlSec,
		AutoRenew: true,
	})
	if err != nil {
		return fmt.Errorf("acquire lock %q: %w", key, err)
	}
	defer func() {
		_ = lock.Release(context.Background())
	}()
	return fn(ctx)
}
