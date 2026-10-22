-- Disposable explicit-cache registry. No prompts, credentials or request history.
CREATE TABLE IF NOT EXISTS modelhub.prefix_cache (
    cache_key text PRIMARY KEY,
    cached_content text NOT NULL,
    expires_at timestamptz NOT NULL
);
