-- Migration 40: the NOVA group of grocery products — how processed a food
-- is, from 1 (unprocessed or minimally processed) to 4 (ultra-processed),
-- the classification Open Food Facts publishes as nova_group. NULL when
-- unknown. Filled from Open Food Facts on a barcode scan and by the text
-- search that enriches a product typed by hand, or set by a member; it
-- replaces the Yuka score in the PWA, Yuka having no public API (yuka_score,
-- migration 39, stays: still accepted and reported by the API, no longer
-- shown, so no score someone typed is lost).
--
-- Kept on the item and on the House catalog product, like the Nutri-Score
-- (see internal/handlers/nutrition.go).
ALTER TABLE items ADD COLUMN nova_group INTEGER CHECK (nova_group BETWEEN 1 AND 4);
ALTER TABLE house_products ADD COLUMN nova_group INTEGER CHECK (nova_group BETWEEN 1 AND 4);
