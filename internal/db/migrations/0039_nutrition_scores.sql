-- Migration 39: nutrition scores of grocery products, and where each user
-- wants them shown (see internal/handlers/products.go and
-- static/js/nutrition.js).
--
-- nutri_score is a Nutri-Score grade, one of 'A' to 'E' (stored upper-case,
-- see internal/validate.NutriScore), NULL when unknown: Open Food Facts'
-- nutriscore_grade for a scanned product, or what a member picked.
-- yuka_score is a 0-100 score, NULL when unknown. Nothing fills it in by
-- itself: Yuka has no public API and Trakka doesn't compute an imitation of
-- its proprietary score, so it only holds what a client sends (typed in by a
-- member, or set by an integration).
--
-- Kept on the item (what the list view shows, read with every item) and on
-- the House catalog product (what an item added again by name inherits,
-- and what a scan of a code the House knows answers), like the aisle.
ALTER TABLE items ADD COLUMN nutri_score TEXT CHECK (nutri_score IN ('A', 'B', 'C', 'D', 'E'));
ALTER TABLE items ADD COLUMN yuka_score INTEGER CHECK (yuka_score BETWEEN 0 AND 100);
ALTER TABLE house_products ADD COLUMN nutri_score TEXT CHECK (nutri_score IN ('A', 'B', 'C', 'D', 'E'));
ALTER TABLE house_products ADD COLUMN yuka_score INTEGER CHECK (yuka_score BETWEEN 0 AND 100);

-- Where the scores are shown, per account: on a phone (the Android app or a
-- narrow screen) and on a wider web screen, each on its own. Both off by
-- default — the feature is opt-in, and with neither set nothing is shown.
ALTER TABLE users ADD COLUMN nutrition_scores_mobile INTEGER NOT NULL DEFAULT 0 CHECK (nutrition_scores_mobile IN (0, 1));
ALTER TABLE users ADD COLUMN nutrition_scores_web INTEGER NOT NULL DEFAULT 0 CHECK (nutrition_scores_web IN (0, 1));
