-- 0002_rules.sql
-- Rule version history and the rule application audit log backing
-- internal/store.RuleStore.

CREATE TABLE IF NOT EXISTS rule_versions (
    rule_id        TEXT NOT NULL,
    version        INT NOT NULL,
    rule_type      TEXT NOT NULL,
    content        TEXT NOT NULL,
    content_hash   TEXT NOT NULL,
    source_commit  TEXT,
    author         TEXT,
    change_note    TEXT,
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to   TIMESTAMPTZ NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, version)
);

COMMENT ON TABLE rule_versions IS
    'Rows are immutable once inserted, except for effective_to, which is set '
    'exactly once (from NULL to a timestamp) when a newer version for the '
    'same rule_id is inserted, closing the previously-open version.';

CREATE TABLE IF NOT EXISTS rule_applications (
    id          TEXT PRIMARY KEY,
    rule_id     TEXT NOT NULL,
    version     INT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    inputs      JSONB NOT NULL,
    outcome     JSONB NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS rule_applications_rule_entity_idx
    ON rule_applications (rule_id, entity_id);
CREATE INDEX IF NOT EXISTS rule_applications_entity_id_idx
    ON rule_applications (entity_id);
