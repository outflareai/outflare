-- Attribution models a workspace defines on top of the five built in:
-- position weights, the time-decay half-life, channels left out and a
-- look-back window per channel. A model is a key with versions; a version
-- never changes, so a report or a module can say exactly which one it used.
-- Editing a model adds a version and credits every conversion again.
CREATE TABLE attribution_models (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    key          text NOT NULL CHECK (key ~ '^[a-z][a-z0-9-]{0,39}$'),
    version      integer NOT NULL CHECK (version >= 1),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    -- engine/internal/attribution.Spec: base model and its options.
    spec         jsonb NOT NULL,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- An archived model is no longer credited, unless a version is pinned.
    archived_at  timestamptz,
    UNIQUE (workspace_id, key, version)
);

-- A module that must not move when a model is edited (an affiliate
-- program's commission rule) pins the exact version it uses. A pinned
-- version stays credited until every pin on it is gone.
CREATE TABLE attribution_model_pins (
    model_id     uuid NOT NULL REFERENCES attribution_models (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    module       text NOT NULL CHECK (module ~ '^[a-z][a-z0-9_]{0,39}$'),
    ref          text NOT NULL CHECK (length(ref) BETWEEN 1 AND 200),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (model_id, module, ref)
);
CREATE INDEX attribution_model_pins_ws_idx ON attribution_model_pins (workspace_id);

-- A custom model's credits sit beside the built-in ones, under the model
-- 'm:' and its version's id, so every report reads them the same way.
ALTER TABLE attribution_credits DROP CONSTRAINT attribution_credits_model_check,
    ADD CONSTRAINT attribution_credits_model_check CHECK (model IN ('last_non_direct', 'first_touch', 'linear', 'position', 'time_decay')
        OR model ~ '^m:[0-9a-f-]{36}$');

-- The workspace default can be a custom model's key.
ALTER TABLE attribution_settings DROP CONSTRAINT attribution_settings_model_check,
    ADD CONSTRAINT attribution_settings_model_check CHECK (model IN ('last_non_direct', 'first_touch', 'linear', 'position', 'time_decay')
        OR model ~ '^[a-z][a-z0-9-]{0,39}$');

-- The default look-back is 30 days (it was 90). Workspaces that never
-- chose theirs move to it, and are credited again by the runner (a null
-- backfilled_at), which first clears what was credited before updated_at.
-- The column's own default stays 90 while the previous version may still
-- run: it makes rows without a lookback. This version writes 30 itself
-- (attribution.DefaultLookbackDays); a later migration moves the default.
UPDATE attribution_settings SET lookback_days = 30, backfilled_at = NULL, updated_at = now()
WHERE updated_by IS NULL AND lookback_days = 90;
