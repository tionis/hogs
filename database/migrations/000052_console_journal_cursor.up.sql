ALTER TABLE console_history ADD COLUMN journal_cursor TEXT;

CREATE UNIQUE INDEX idx_console_history_journal_cursor
    ON console_history(server_id, journal_cursor)
    WHERE journal_cursor IS NOT NULL;
