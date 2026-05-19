package cache

import (
	"sync"
	"testing"
	"time"
)

func TestTTL_GetSet(t *testing.T) {
	c := New[string, int](time.Minute)
	if _, ok := c.Get("x"); ok {
		t.Fatal("expected miss")
	}
	c.Set("x", 42)
	v, ok := c.Get("x")
	if !ok {
		t.Fatal("expected hit")
	}
	if v != 42 {
		t.Errorf("got %d, want 42", v)
	}
}

func TestTTL_Expires(t *testing.T) {
	c := New[string, int](50 * time.Millisecond)
	c.Set("x", 1)
	if _, ok := c.Get("x"); !ok {
		t.Fatal("expected hit before expiry")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get("x"); ok {
		t.Fatal("expected miss after expiry")
	}
}

func TestTTL_Overwrites(t *testing.T) {
	c := New[string, int](time.Minute)
	c.Set("x", 1)
	c.Set("x", 2)
	v, _ := c.Get("x")
	if v != 2 {
		t.Errorf("got %d, want 2", v)
	}
}

func TestTTL_Concurrent(t *testing.T) {
	c := New[int, int](time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Set(i, i*2)
			_, _ = c.Get(i)
		}(i)
	}
	wg.Wait()
	// Все 100 ключей должны быть на месте.
	for i := 0; i < 100; i++ {
		v, ok := c.Get(i)
		if !ok || v != i*2 {
			t.Errorf("key %d: %d ok=%v", i, v, ok)
		}
	}
}

func TestTTL_DifferentKeysIndependent(t *testing.T) {
	c := New[string, string](time.Minute)
	c.Set("a", "1")
	c.Set("b", "2")
	if v, _ := c.Get("a"); v != "1" {
		t.Errorf("a: %q", v)
	}
	if v, _ := c.Get("b"); v != "2" {
		t.Errorf("b: %q", v)
	}
}
