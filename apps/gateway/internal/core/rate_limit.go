package core

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const maxRateLimitWait = 2 * time.Second

type RateLimiter interface {
	Wait(ctx context.Context, phoneNumberID string) error
}

type BucketLimiter struct {
	perSecond int
	maxWait   time.Duration
	mu        sync.Mutex
	buckets   map[string]*rate.Limiter
}

func NewBucketLimiter(perSecond int) *BucketLimiter {
	return &BucketLimiter{
		perSecond: perSecond,
		maxWait:   maxRateLimitWait,
		buckets:   make(map[string]*rate.Limiter),
	}
}

func (l *BucketLimiter) Wait(ctx context.Context, phoneNumberID string) error {
	reservation := l.bucket(phoneNumberID).Reserve()
	delay := reservation.Delay()
	if delay > l.maxWait {
		reservation.Cancel()
		return rateLimited("the send rate for this phone number is exceeded, retry shortly")
	}
	if delay == 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		reservation.Cancel()
		return rateLimited("the request ended while waiting for the send rate limit")
	}
}

func (l *BucketLimiter) bucket(phoneNumberID string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, found := l.buckets[phoneNumberID]
	if !found {
		bucket = rate.NewLimiter(rate.Limit(l.perSecond), l.perSecond)
		l.buckets[phoneNumberID] = bucket
	}
	return bucket
}

func rateLimited(message string) *RequestError {
	return &RequestError{Code: CodeRateLimited, Message: message, IsRetryable: true}
}
