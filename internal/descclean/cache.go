package descclean

import "container/list"

// lruCache is a small bounded map that evicts the least recently used entry. It
// is not safe for concurrent use; callers hold their own lock.
type lruCache[K comparable, V any] struct {
	max   int
	order *list.List // front = most recently used
	items map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	key K
	val V
}

func newLRU[K comparable, V any](max int) *lruCache[K, V] {
	return &lruCache[K, V]{max: max, order: list.New(), items: make(map[K]*list.Element)}
}

func (c *lruCache[K, V]) get(key K) (V, bool) {
	el, ok := c.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry[K, V]).val, true
}

func (c *lruCache[K, V]) put(key K, val V) {
	if el, ok := c.items[key]; ok {
		el.Value.(*lruEntry[K, V]).val = val
		c.order.MoveToFront(el)
		return
	}
	c.items[key] = c.order.PushFront(&lruEntry[K, V]{key: key, val: val})
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*lruEntry[K, V]).key)
	}
}

func (c *lruCache[K, V]) remove(key K) {
	if el, ok := c.items[key]; ok {
		c.order.Remove(el)
		delete(c.items, key)
	}
}

func (c *lruCache[K, V]) len() int { return c.order.Len() }
