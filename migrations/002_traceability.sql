-- Per-routine traceability: when on, every message the routine handles also gets a
-- "MailRoutine/<Action>" label so it is easy to see what MailRoutine touched.
ALTER TABLE routines ADD COLUMN traceability INTEGER NOT NULL DEFAULT 1 CHECK (traceability IN (0, 1));
