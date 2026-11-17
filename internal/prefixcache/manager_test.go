package prefixcache

import (
	"testing"
	"time"
)

func expiryPtr(value time.Time) *time.Time { return &value }

func TestEntryUsableUsesTenMinuteMarginAndExcludesShortPrefixes(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		entry *Entry
		want  bool
	}{
		{"missing", nil, false},
		{"no resource", &Entry{ExpiresAt: expiryPtr(now.Add(TTL))}, false},
		{"within margin", &Entry{Name: "cachedContents/1", ExpiresAt: expiryPtr(now.Add(9 * time.Minute))}, false},
		{"exact margin", &Entry{Name: "cachedContents/1", ExpiresAt: expiryPtr(now.Add(10 * time.Minute))}, false},
		{"outside margin", &Entry{Name: "cachedContents/1", ExpiresAt: expiryPtr(now.Add(10*time.Minute + time.Nanosecond))}, true},
		{"too short", &Entry{}, false},
		{"null expiry with stale resource", &Entry{Name: "cachedContents/1"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.Usable(now); got != tc.want {
				t.Fatalf("Usable=%v want=%v", got, tc.want)
			}
		})
	}
}
