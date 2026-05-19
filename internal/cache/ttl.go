// Package cache — простой TTL-cache. Generic, без зависимостей.
package cache

import (
	"sync"
	"time"
)

// Cache — потокобезопасный кэш с per-entry TTL.
// Просроченные записи удаляются лениво при Get/Set.
type Cache[K comparable, V any] struct {
	ttl time.Duration

	mu      sync.RWMutex
	entries map[K]entry[V]
}

type entry[V any] struct {
	val       V
	expiresAt time.Time
}

// New создаёт кэш с заданным TTL для всех записей.
func New[K comparable, V any](ttl time.Duration) *Cache[K, V] {
	return &Cache[K, V]{
		ttl:     ttl,
		entries: make(map[K]entry[V]),
	}
}

// Get возвращает значение и флаг присутствия.
// Просроченные записи возвращают ok=false и удаляются.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	c.mu.RLock()
	e, ok := c.entries[k]
	c.mu.RUnlock()
	var zero V
	if !ok {
		return zero, false
	}
	if time.Now().After(e.expiresAt) {
		c.mu.Lock()
		// Удаляем только если запись не была подменена свежим Set'ом.
		if cur, ok := c.entries[k]; ok && cur.expiresAt.Equal(e.expiresAt) {
			delete(c.entries, k)
		}
		c.mu.Unlock()
		return zero, false
	}
	return e.val, true
}

// Set кладёт значение с TTL.
func (c *Cache[K, V]) Set(k K, v V) {
	c.mu.Lock()
	c.entries[k] = entry[V]{val: v, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}
