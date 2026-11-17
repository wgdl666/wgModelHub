-- NULL expiry marks a prefix that must use full input without explicit caching.
ALTER TABLE modelhub.prefix_cache ALTER COLUMN expires_at DROP NOT NULL;
