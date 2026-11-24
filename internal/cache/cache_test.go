package cache

import (
	"fmt"
	"testing"
	"time"
)

func TestLRUEvictsLeastRecent(t *testing.T) {
	c := New()
	for i := range maxEntries {
		c.Set(fmt.Sprintf("k%d", i), i, time.Hour)
	}
	if _, ok := c.Get("k0"); !ok {
		t.Fatal("k0 missing before overflow")
	}
	c.Set("fresh", "x", time.Hour)
	if _, ok := c.Get("k0"); !ok {
		t.Fatal("refreshed k0 was evicted")
	}
	if _, ok := c.Get("k1"); ok {
		t.Fatal("k1 should have been the least recent")
	}
	if v, ok := c.Get("fresh"); !ok || v != "x" {
		t.Fatalf("fresh %+v %v", v, ok)
	}
}

func TestExpiredKeyIsMissAndDropped(t *testing.T) {
	c := New()
	c.Set("keep", "yes", time.Hour)
	c.Set("stale", "old", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("stale"); ok {
		t.Fatal("stale hit")
	}
	for i := range maxEntries - 1 {
		c.Set(fmt.Sprintf("n%d", i), i, time.Hour)
	}
	if v, ok := c.Get("keep"); !ok || v != "yes" {
		t.Fatalf("keep %+v %v", v, ok)
	}
}

func TestSetIgnoresNonPositiveTTL(t *testing.T) {
	c := New()
	c.Set("k", "v", 0)
	if _, ok := c.Get("k"); ok {
		t.Fatal("stored zero ttl")
	}
}
