package crypto

import (
	"math"
	"testing"
	"time"
)

func TestNonceTrackerRejectsExtremeTimestampWithoutOverflow(t *testing.T) {
	tracker := NewNonceTracker()
	if tracker.CheckAndRecord(math.MinInt64, "nonce") {
		t.Fatal("accepted an extreme stale timestamp")
	}
}

func TestNonceTrackerRejectsReplay(t *testing.T) {
	tracker := NewNonceTracker()
	now := time.Now().Unix()
	if !tracker.CheckAndRecord(now, "nonce") {
		t.Fatal("rejected first nonce")
	}
	if tracker.CheckAndRecord(now, "nonce") {
		t.Fatal("accepted replayed nonce")
	}
}
