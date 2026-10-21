package cache

import (
	"sync"
	"time"
)

// ponytail: one process map, same ceiling as the old per-process array cache.
type Cache struct {
	mu    sync.Mutex
	items map[string]item
}

type item struct {
	v   any
	exp time.Time
}

func New() *Cache {
	return &Cache{items: map[string]item{}}
}

func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok || !time.Now().Before(it.exp) {
		return nil, false
	}
	return it.v, true
}

func (c *Cache) Set(key string, v any, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = item{v: v, exp: time.Now().Add(ttl)}
}
