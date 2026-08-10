package crypto

import (
	"sync"
	"time"
)

const windowSeconds = 300 // ±5 minutes
const maxBucketSize = 10000

// NonceTracker tracks seen nonces within time buckets to prevent replay.
type NonceTracker struct {
	mu      sync.Mutex
	buckets map[int64]*nonceSet
}

type nonceSet struct {
	seen map[string]struct{}
}

func NewNonceTracker() *NonceTracker {
	return &NonceTracker{
		buckets: make(map[int64]*nonceSet),
	}
}

// CheckAndRecord returns true if the nonce is new (not replayed).
func (nt *NonceTracker) CheckAndRecord(timestamp int64, nonce string) bool {
	nt.mu.Lock()
	defer nt.mu.Unlock()

	now := time.Now().Unix()
	if timestamp < now-windowSeconds || timestamp > now+windowSeconds {
		return false
	}

	bucketKey := timestamp / windowSeconds

	// Garbage collect old buckets
	for k := range nt.buckets {
		if now-(k*windowSeconds) > windowSeconds*2 {
			delete(nt.buckets, k)
		}
	}

	bucket, ok := nt.buckets[bucketKey]
	if !ok {
		bucket = &nonceSet{seen: make(map[string]struct{})}
		nt.buckets[bucketKey] = bucket
	}

	if _, exists := bucket.seen[nonce]; exists {
		return false
	}
	if len(bucket.seen) >= maxBucketSize {
		return false // rate limit
	}
	bucket.seen[nonce] = struct{}{}
	return true
}
