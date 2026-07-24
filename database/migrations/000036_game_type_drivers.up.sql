ALTER TABLE game_types ADD COLUMN kind TEXT NOT NULL DEFAULT 'generic'
    CHECK (kind IN ('embedded', 'generic'));
ALTER TABLE game_types ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1
    CHECK (enabled IN (0, 1));

UPDATE game_types SET kind = 'embedded' WHERE builtin = 1;
