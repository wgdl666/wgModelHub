package prefixcache

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"

	"github.com/wgdl666/kangaroo/logs"
)

const TTL = time.Hour

// Manager only registers/reuses resources. It does not renew them or persist
// prompts. Bounded workers retain the current request's static input in memory.
type Manager struct {
	store  Store
	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
}

func New(ctx context.Context, store Store) *Manager {
	ctx, cancel := context.WithCancel(ctx)
	return &Manager{store: store, ctx: ctx, cancel: cancel, jobs: make(chan struct{}, 8)}
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) Lookup(ctx context.Context, key string) *Entry {
	if m.ctx.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	entry, err := m.store.Get(ctx, key)
	if err != nil {
		logs.Default().WarnContext(ctx, "modelhub_prefix_cache_store_failed", "operation", "lookup", "error", err)
		return nil
	}
	if entry.Usable(time.Now()) {
		return entry
	}
	return nil
}

func (m *Manager) Invalidate(ctx context.Context, entry *Entry) {
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	_, err := m.store.WithLock(ctx, lockID(entry.Key), func(store Store) error {
		return store.Invalidate(ctx, entry.Key, entry.Name)
	})
	if err != nil {
		// The full-prompt fallback is still usable during a registry outage.
		logs.Default().WarnContext(ctx, "modelhub_prefix_cache_store_failed", "operation", "invalidate", "error", err)
	}
}

func (m *Manager) ScheduleCreate(key string, create func(context.Context) (*Entry, error)) {
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	select {
	case m.jobs <- struct{}{}:
	default:
		m.mu.Unlock()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer func() {
			<-m.jobs
			m.wg.Done()
		}()
		ctx, cancel := context.WithTimeout(m.ctx, 25*time.Second)
		defer cancel()
		_, err := m.store.WithLock(ctx, lockID(key), func(store Store) error {
			// Another process may have filled the registry since the request's lookup.
			entry, err := store.Get(ctx, key)
			if err != nil || entry.Usable(time.Now()) {
				return err
			}
			entry, err = create(ctx)
			if err != nil {
				return err
			}
			if !entry.Usable(time.Now()) {
				return nil
			}
			entry.Key = key
			return store.Put(ctx, entry)
		})
		if err != nil {
			if m.ctx.Err() == nil {
				logs.Default().WarnContext(ctx, "modelhub_prefix_cache_create_failed", "key", key, "error", err)
			}
		}
	}()
}

func lockID(key string) int64 {
	digest := sha256.Sum256([]byte("wg-modelhub/prefix-cache-lock/v1/" + key))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}
