package download

import (
	"context"
	"time"
)

// retryPolicy is how a network operation is retried: every download and
// every small fetch.
type retryPolicy struct {
	retries int           // attempts after the first
	backoff time.Duration // the first retry's wait, doubling after
	log     func(format string, a ...any)
}

// newRetryPolicy defaults Getter's Retries and Backoff: Retries 0 means 3
// and negative means none; Backoff 0 or negative means one second.
func newRetryPolicy(retries int, backoff time.Duration, log func(string, ...any)) retryPolicy {
	switch {
	case retries == 0:
		retries = 3
	case retries < 0:
		retries = 0
	}
	if backoff <= 0 {
		backoff = time.Second
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	return retryPolicy{retries: retries, backoff: backoff, log: log}
}

// Retry runs op until it succeeds, fails in a way op says is not worth
// retrying, or has failed 1+retries times, and returns op's last error.
// Between attempts it waits the backoff, doubling each time, and logs
// why, naming what. A done ctx ends the wait with ctx's error. Retries
// and backoff default as Getter's do.
func Retry(ctx context.Context, retries int, backoff time.Duration, log func(string, ...any), what string, op func() (bool, error)) error {
	return newRetryPolicy(retries, backoff, log).do(ctx, what, op)
}

// do is Retry, with the policy already defaulted.
func (p retryPolicy) do(ctx context.Context, what string, op func() (retry bool, err error)) error {
	wait := p.backoff
	var err error
	for attempt := 0; attempt <= p.retries; attempt++ {
		if attempt > 0 {
			p.log("%s: retrying in %v (%v)", what, wait, err)
			t := time.NewTimer(wait)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			}
			wait *= 2
		}
		var retry bool
		retry, err = op()
		if err == nil || !retry {
			return err
		}
	}
	return err
}
