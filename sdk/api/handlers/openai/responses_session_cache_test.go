package openai

import (
	"testing"
	"time"
)

func TestResponseSessionCache_SetGet(t *testing.T) {
	c := NewResponseSessionCache(10*time.Minute, 100)

	c.Set("resp_001", "auth_A")

	authID, ok := c.Get("resp_001")
	if !ok {
		t.Fatal("expected cache hit for resp_001")
	}
	if authID != "auth_A" {
		t.Fatalf("expected auth_A, got %s", authID)
	}
}

func TestResponseSessionCache_Miss(t *testing.T) {
	c := NewResponseSessionCache(10*time.Minute, 100)

	_, ok := c.Get("nonexistent")
	if ok {
		t.Fatal("expected cache miss for nonexistent key")
	}
}

func TestResponseSessionCache_Expiry(t *testing.T) {
	c := NewResponseSessionCache(1*time.Millisecond, 100)

	c.Set("resp_exp", "auth_B")
	time.Sleep(5 * time.Millisecond)

	_, ok := c.Get("resp_exp")
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestResponseSessionCache_Overwrite(t *testing.T) {
	c := NewResponseSessionCache(10*time.Minute, 100)

	c.Set("resp_ow", "auth_C")
	c.Set("resp_ow", "auth_D")

	authID, ok := c.Get("resp_ow")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if authID != "auth_D" {
		t.Fatalf("expected auth_D after overwrite, got %s", authID)
	}
}

func TestResponseSessionCache_CapacityEviction(t *testing.T) {
	c := NewResponseSessionCache(10*time.Minute, 3)

	c.Set("r1", "a1")
	c.Set("r2", "a2")
	c.Set("r3", "a3")
	c.Set("r4", "a4") // should evict r1 (oldest expiry)

	if _, ok := c.Get("r4"); !ok {
		t.Error("r4 should be present")
	}
	// r1 should have been evicted
	if _, ok := c.Get("r1"); ok {
		t.Error("r1 should have been evicted")
	}
}

func TestResponseSessionCache_EmptyKeys(t *testing.T) {
	c := NewResponseSessionCache(10*time.Minute, 100)

	c.Set("", "auth_X")
	c.Set("resp_empty_auth", "")

	if _, ok := c.Get(""); ok {
		t.Error("empty response ID should not be stored")
	}
	if _, ok := c.Get("resp_empty_auth"); ok {
		t.Error("empty auth ID should not be stored")
	}
}
