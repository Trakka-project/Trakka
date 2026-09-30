-- Migration 22: recurrence rules move to a canonical iCalendar RRULE subset
-- (see internal/recurrence), e.g. FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR,
-- which is what makes "weekly on given days" expressible. The write path
-- (internal/validate.Recurrence) already normalizes every rule it accepts;
-- this rewrites rows stored in the legacy forms to the exact same canonical
-- spelling, so a rule reads back the same way whichever path wrote it:
--   DAILY/WEEKLY/MONTHLY/YEARLY  → FREQ=<same>
--   EVERY_X_DAYS:<n>             → FREQ=WEEKLY[;INTERVAL=n/7] when n is a
--                                  multiple of 7, else FREQ=DAILY[;INTERVAL=n]
--   EVERY_X_MONTHS:<n>           → FREQ=MONTHLY[;INTERVAL=n]
-- (INTERVAL omitted when it is 1). Stepping semantics are unchanged for all
-- of these. The runtime parsers still accept the legacy forms, so a value
-- this misses — or one still cached in a browser's offline mirror — keeps
-- working.
UPDATE items SET recurrence_rule = 'FREQ=' || recurrence_rule
WHERE recurrence_rule IN ('DAILY', 'WEEKLY', 'MONTHLY', 'YEARLY');

UPDATE items SET recurrence_rule = CASE
    WHEN CAST(substr(recurrence_rule, 14) AS INTEGER) = 1 THEN 'FREQ=DAILY'
    WHEN CAST(substr(recurrence_rule, 14) AS INTEGER) = 7 THEN 'FREQ=WEEKLY'
    WHEN CAST(substr(recurrence_rule, 14) AS INTEGER) % 7 = 0
        THEN 'FREQ=WEEKLY;INTERVAL=' || (CAST(substr(recurrence_rule, 14) AS INTEGER) / 7)
    ELSE 'FREQ=DAILY;INTERVAL=' || CAST(substr(recurrence_rule, 14) AS INTEGER)
END
WHERE recurrence_rule GLOB 'EVERY_X_DAYS:[1-9]*';

UPDATE items SET recurrence_rule = CASE
    WHEN CAST(substr(recurrence_rule, 16) AS INTEGER) = 1 THEN 'FREQ=MONTHLY'
    ELSE 'FREQ=MONTHLY;INTERVAL=' || CAST(substr(recurrence_rule, 16) AS INTEGER)
END
WHERE recurrence_rule GLOB 'EVERY_X_MONTHS:[1-9]*';
