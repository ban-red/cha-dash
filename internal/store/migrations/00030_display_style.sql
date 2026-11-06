-- A display's style (docs/plan/15-v0.5.md, wave 34): 'wall', a tablet on a
-- wall (calm: dims, tints at night, moves against burn-in); 'tv', the same
-- at a TV's size, moved with a remote's arrow keys; 'ink', an e-ink screen
-- or a printout: one colour, no motion, absolute times, never dimmed.
-- Every display made before this is a wall display.

-- +goose Up
ALTER TABLE displays ADD COLUMN style TEXT NOT NULL DEFAULT 'wall' CHECK (style IN ('wall', 'tv', 'ink'));

-- +goose Down
ALTER TABLE displays DROP COLUMN style;
