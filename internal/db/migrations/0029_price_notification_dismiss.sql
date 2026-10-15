-- Lets a user remove entries from their 🔔 price alert inbox, one at a time
-- or all at once (DELETE /api/v1/price-notifications[/{id}]).
--
-- A soft delete rather than a DELETE: HasPriceNotification looks for an
-- earlier 'expired' row to announce an expired deal only once per url, and
-- a physically deleted row would have the next scan announce it again to
-- everyone. A dismissed row is also marked read, so PrunePriceNotifications'
-- read-row retention removes it for real after the usual delay.
ALTER TABLE price_notifications ADD COLUMN dismissed_at TEXT;
