package cache

import (
	"container/list"
	"sync"
	"time"
)

const maxEntries = 256

// ponytail: 256-entry LRU, raise if one process needs more hot IDs.
type Cache struct {
	mu    sync.Mutex
	items map[string]*entry
	order *list.List
}

type entry struct {
	key string
	v   any
	exp time.Time
	el  *list.Element
}

func New() *Cache {
	return &Cache{items: map[string]*entry{}, order: list.New()}
}

func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if !time.Now().Before(it.exp) {
		c.remove(it)
		return nil, false
	}
	c.order.MoveToFront(it.el)
	return it.v, true
}

func (c *Cache) Set(key string, v any, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if it, ok := c.items[key]; ok {
		it.v = v
		it.exp = time.Now().Add(ttl)
		c.order.MoveToFront(it.el)
		return
	}
	for c.order.Len() >= maxEntries {
		c.remove(c.order.Back().Value.(*entry))
	}
	it := &entry{key: key, v: v, exp: time.Now().Add(ttl)}
	it.el = c.order.PushFront(it)
	c.items[key] = it
}

func (c *Cache) remove(it *entry) {
	delete(c.items, it.key)
	c.order.Remove(it.el)
}
