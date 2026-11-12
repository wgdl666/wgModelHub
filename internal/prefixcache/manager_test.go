package prefixcache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestSuppressionExpiresAndIsBounded(t *testing.T) {
	m := New(context.Background(), nil)
	defer m.Close()
	start := time.Now()
	m.Suppress("short", time.Hour)
	if until := m.blocked["short"]; until.Before(start.Add(time.Hour)) || until.After(time.Now().Add(time.Hour)) {
		t.Fatalf("unexpected suppression deadline: %v", until)
	}
	if !m.Suppressed("short") || m.Suppressed("other") {
		t.Fatal("suppression must be scoped to the rejected key")
	}
	m.blocked["short"] = time.Now().Add(-time.Second)
	if m.Suppressed("short") {
		t.Fatal("expired suppression must allow another attempt")
	}
	for i := 0; i < 4097; i++ {
		m.Suppress(fmt.Sprint(i), time.Hour)
	}
	if len(m.blocked) != 4096 || !m.Suppressed("4096") {
		t.Fatal("suppression map must stay bounded and retain the newest key")
	}
	if _, ok := m.blocked["short"]; ok {
		t.Fatal("expired entries must be reclaimed")
	}
}
