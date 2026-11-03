-- Migration 43: "Ne pas envoyer à Fluv" for one purchase (internal/fluv,
-- docs/FLUV.md). The buyer can take a purchase out of Fluv while the item
-- stays bought: the row is cancelled like an uncheck, and skipped = 1 keeps
-- later corrections (a price typed afterwards, a new shipping fee) from
-- sending it again. Un-checking the item clears it: its next purchase is
-- sent as usual.
ALTER TABLE fluv_purchases ADD COLUMN skipped INTEGER NOT NULL DEFAULT 0 CHECK (skipped IN (0, 1));
