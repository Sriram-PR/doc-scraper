package fetch

import (
	"testing"
	"time"

	"github.com/temoto/robotstxt"
)

func newTestRobotsHandler() *RobotsHandler {
	return &RobotsHandler{
		robotsCache: make(map[string]robotsCacheEntry),
		log:         testLogger(),
	}
}

func TestRobotsCache_MissWhenEmpty(t *testing.T) {
	rh := newTestRobotsHandler()
	if data, found := rh.lookupCache("example.com"); found || data != nil {
		t.Fatalf("expected miss on empty cache, got found=%v data=%v", found, data)
	}
}

func TestRobotsCache_SuccessCachedIndefinitely(t *testing.T) {
	rh := newTestRobotsHandler()
	data, err := robotstxt.FromBytes([]byte("User-agent: *\nDisallow: /private\n"))
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	rh.cacheSuccess("example.com", data)

	got, found := rh.lookupCache("example.com")
	if !found || got != data {
		t.Fatalf("expected cached success entry, got found=%v", found)
	}
}

func TestRobotsCache_FailureLivesWithinTTL(t *testing.T) {
	rh := newTestRobotsHandler()
	rh.cacheFailure("example.com")

	data, found := rh.lookupCache("example.com")
	if !found {
		t.Fatal("expected fresh negative entry to be reported as present (fail-open)")
	}
	if data != nil {
		t.Fatalf("expected nil data for negative entry, got %v", data)
	}
}

func TestRobotsCache_FailureReattemptedAfterTTL(t *testing.T) {
	rh := newTestRobotsHandler()
	rh.robotsCache["example.com"] = robotsCacheEntry{expires: time.Now().Add(-time.Minute)}

	if _, found := rh.lookupCache("example.com"); found {
		t.Fatal("expected expired negative entry to be reported absent so it re-fetches")
	}
}
