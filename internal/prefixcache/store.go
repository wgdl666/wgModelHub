package prefixcache

import (
	"context"
	"time"

	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/prefixcache"
)

type Entry struct {
	Key       string
	Name      string
	ExpiresAt time.Time
}

func (e *Entry) Usable(now time.Time) bool {
	// Avoid handing out a resource likely to expire during request dispatch.
	return e != nil && e.Name != "" && e.ExpiresAt.After(now.Add(5*time.Second))
}

type Store interface {
	Get(context.Context, string) (*Entry, error)
	Put(context.Context, *Entry) error
	Invalidate(context.Context, string, string) error
	WithLock(context.Context, int64, func(Store) error) (bool, error)
}

type Postgres struct {
	client *ent.Client
}

func NewPostgres(client *ent.Client) *Postgres {
	return &Postgres{client: client}
}

func (s *Postgres) Get(ctx context.Context, key string) (*Entry, error) {
	row, err := s.client.PrefixCache.Get(ctx, key)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Entry{Key: row.ID, Name: row.CachedContent, ExpiresAt: row.ExpiresAt}, nil
}

func (s *Postgres) Put(ctx context.Context, entry *Entry) error {
	// Every writer holds the per-key advisory lock, including conditional removal.
	n, err := s.client.PrefixCache.Update().Where(prefixcache.IDEQ(entry.Key)).SetCachedContent(entry.Name).SetExpiresAt(entry.ExpiresAt).Save(ctx)
	if err != nil || n > 0 {
		return err
	}
	return s.client.PrefixCache.Create().SetID(entry.Key).SetCachedContent(entry.Name).SetExpiresAt(entry.ExpiresAt).Exec(ctx)
}

func (s *Postgres) Invalidate(ctx context.Context, key, name string) error {
	// An old in-flight response must not remove a newly rebuilt resource.
	_, err := s.client.PrefixCache.Delete().Where(prefixcache.IDEQ(key), prefixcache.CachedContentEQ(name)).Exec(ctx)
	return err
}

func (s *Postgres) WithLock(ctx context.Context, key int64, fn func(Store) error) (bool, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	acquired, err := tx.TryPrefixCacheLock(ctx, key)
	if err != nil || !acquired {
		return false, err
	}
	if err := fn(NewPostgres(tx.Client())); err != nil {
		return true, err
	}
	return true, tx.Commit()
}
