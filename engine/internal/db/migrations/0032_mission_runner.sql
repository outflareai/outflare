-- Who carries out a mission. external: an outside agent sends the
-- Outflare-Mission header on its own calls. builtin: Outflare's own agent
-- runs it in turns with the workspace's Claude key, as the mission's token.
ALTER TABLE missions
    ADD COLUMN runner       text NOT NULL DEFAULT 'external' CHECK (runner IN ('external', 'builtin')),
    -- When the built-in agent next takes a turn. Null: not scheduled.
    ADD COLUMN next_turn_at timestamptz,
    ADD COLUMN last_turn_at timestamptz,
    -- Set while a turn runs, so two engines never run the same mission.
    ADD COLUMN turn_started_at timestamptz,
    ADD COLUMN turns        integer NOT NULL DEFAULT 0,
    -- What the agent said it did in its last turn.
    ADD COLUMN turn_summary text NOT NULL DEFAULT '',
    -- Turns that failed in a row; the mission pauses after three.
    ADD COLUMN turn_failures integer NOT NULL DEFAULT 0,
    -- Why the last turn failed, shown on the page; empty when it went fine.
    ADD COLUMN turn_error   text NOT NULL DEFAULT '';

CREATE INDEX missions_turn_idx ON missions (next_turn_at) WHERE runner = 'builtin' AND status = 'running';
