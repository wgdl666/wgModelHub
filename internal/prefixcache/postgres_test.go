package prefixcache

import (
	"context"
	"database/sql"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/prefixcache"
	"github.com/wgdl666/wgModelHub/internal/dbmigration"
)

// Requires a disposable database: the explicit migration command is exercised.
func TestPostgresCacheRegistryAndCrossConnectionLocks(t *testing.T) {
	dsn := os.Getenv("MODELHUB_PREFIX_CACHE_TEST_DSN")
	if dsn == "" {
		t.Skip("set MODELHUB_PREFIX_CACHE_TEST_DSN to a disposable PostgreSQL database")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := dbmigration.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	store := NewPostgres(client)
	key := "prefix-cache-postgres-integration"
	defer client.PrefixCache.Delete().Where(prefixcache.IDEQ(key)).Exec(context.Background())
	first := &Entry{Key: key, Name: "cachedContents/first", ExpiresAt: time.Now().Add(TTL).UTC().Truncate(time.Microsecond)}
	acquired, err := store.WithLock(ctx, lockID(key), func(txStore Store) error {
		// A second connection must return immediately without entering its callback,
		// including when no registry row exists yet.
		secondEntered := false
		secondAcquired, err := store.WithLock(ctx, lockID(key), func(Store) error {
			secondEntered = true
			return nil
		})
		if err != nil || secondAcquired || secondEntered {
			t.Fatalf("second lock acquired=%v entered=%v err=%v", secondAcquired, secondEntered, err)
		}
		return txStore.Put(ctx, first)
	})
	if err != nil || !acquired {
		t.Fatalf("first lock acquired=%v err=%v", acquired, err)
	}
	got, err := store.Get(ctx, key)
	if err != nil || got == nil || got.Name != first.Name || !got.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("registry entry=%+v err=%v", got, err)
	}
	second := &Entry{Key: key, Name: "cachedContents/second", ExpiresAt: first.ExpiresAt.Add(time.Minute)}
	acquired, err = store.WithLock(ctx, lockID(key), func(txStore Store) error { return txStore.Put(ctx, second) })
	if err != nil || !acquired {
		t.Fatalf("lock not released by commit: acquired=%v err=%v", acquired, err)
	}
	if err := store.Invalidate(ctx, key, first.Name); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, key)
	if err != nil || got == nil || got.Name != second.Name {
		t.Fatalf("stale invalidation removed rebuilt cache: %+v err=%v", got, err)
	}
	if err := store.Invalidate(ctx, key, second.Name); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, key); err != nil || got != nil {
		t.Fatalf("old cache still registered: %+v err=%v", got, err)
	}
	// Rollback must also release the transaction-level advisory lock.
	_, err = store.WithLock(ctx, lockID(key), func(Store) error { return context.Canceled })
	if err == nil {
		t.Fatal("expected rollback callback error")
	}
	acquired, err = store.WithLock(ctx, lockID(key), func(Store) error { return nil })
	if err != nil || !acquired {
		t.Fatalf("lock not released by rollback: acquired=%v err=%v", acquired, err)
	}

	// Independent process managers share PostgreSQL rather than in-memory locks.
	managerKey := key + "-managers"
	defer client.PrefixCache.Delete().Where(prefixcache.IDEQ(managerKey)).Exec(context.Background())
	a, b := New(ctx, store), New(ctx, store)
	defer a.Close()
	defer b.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	var creates atomic.Int32
	create := func(context.Context) (*Entry, error) {
		creates.Add(1)
		return &Entry{Name: "cachedContents/shared", ExpiresAt: time.Now().Add(TTL)}, nil
	}
	a.ScheduleCreate(managerKey, func(ctx context.Context) (*Entry, error) {
		close(started)
		select {
		case <-release:
			return create(ctx)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first manager did not acquire lock")
	}
	b.ScheduleCreate(managerKey, create)
	b.wg.Wait()
	close(release)
	a.wg.Wait()
	// A worker scheduled from a stale miss must reread the committed registry.
	b.ScheduleCreate(managerKey, create)
	b.wg.Wait()
	if creates.Load() != 1 || b.Lookup(ctx, managerKey) == nil {
		t.Fatalf("cross-manager duplicate creation: count=%d", creates.Load())
	}
	_, err = store.WithLock(ctx, lockID(managerKey), func(txStore Store) error {
		return txStore.Put(ctx, &Entry{Key: managerKey, Name: "cachedContents/shared", ExpiresAt: time.Now().Add(-time.Minute)})
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Lookup(ctx, managerKey) != nil {
		t.Fatal("expired registry entry reused")
	}
	b.ScheduleCreate(managerKey, create)
	b.wg.Wait()
	if creates.Load() != 2 || b.Lookup(ctx, managerKey) == nil {
		t.Fatal("expired prefix not rebuilt")
	}
}
