-- The whole schema, applied at startup and idempotent.
--
-- Deliberately absent: CREATE EXTENSION pgcrypto (gen_random_uuid() is core in
-- PG 13+, and CREATE EXTENSION can fail outright on a locked-down managed
-- server), any uuidv7() default (that is a PG 18 builtin; ids come from Go's
-- uuid.NewV7()), and ix_stations_active (a boolean index over ~19 mostly-true
-- rows has no query behind it — both station queries seq-scan at that size).

-- ---------- weather records: the durable queue and the record table ----------
CREATE TABLE IF NOT EXISTS weather_records (
  id               text        PRIMARY KEY,            -- uuidv7, generated in Go
  station_id       bigint      NOT NULL,
  timestamp        timestamptz NOT NULL,               -- reading timestamp
  observation_time timestamptz NOT NULL,               -- dedupe key
  data             jsonb       NOT NULL,
  status           text        NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','processing','completed','failed')),
  attempts         int         NOT NULL DEFAULT 0,
  claim_ref        uuid,                               -- batch label, survives reaping
  adopt_required   boolean     NOT NULL DEFAULT false,
  claimed_at       timestamptz,
  txid             text,
  output_index     int,
  block_height     bigint,
  chain_status     text
                   CHECK (chain_status IS NULL
                          OR chain_status IN ('arc-accepted','unmined','mined','aborted')),
  mined_at         timestamptz,
  error            text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  processed_at     timestamptz,

  -- Makes the naive claim shape structurally unwritable. A SELECT-then-UPDATE
  -- claim that forgets claimed_at is rejected with SQLSTATE 23514, and the
  -- reaper's `claimed_at < now() - lease` predicate becomes total, so no
  -- defensive `OR claimed_at IS NULL` is needed anywhere.
  CONSTRAINT ck_records_processing_leased
    CHECK (status <> 'processing' OR claimed_at IS NOT NULL),

  -- Encodes the invariant the DTO layer would otherwise have to trust: a
  -- completed record always has a txid, an output index and a processed_at.
  CONSTRAINT ck_records_completed_published
    CHECK (status <> 'completed'
           OR (txid IS NOT NULL AND output_index IS NOT NULL AND processed_at IS NOT NULL))
);

-- Structural duplicate prevention and the ON CONFLICT arbiter. NOT nullable,
-- NOT partial. Mongo had no unique index anywhere, so this is new capability
-- rather than a port. Also serves count(*) WHERE station_id = $1.
CREATE UNIQUE INDEX IF NOT EXISTS ux_records_station_obs
  ON weather_records (station_id, observation_time);

-- The claim's inner SELECT: Index Scan with Index Cond status='pending'.
CREATE INDEX IF NOT EXISTS ix_records_status_created
  ON weather_records (status, created_at);

-- The station history page. Delivers an Index Only Scan with Heap Fetches 0.
CREATE INDEX IF NOT EXISTS ix_records_station_created
  ON weather_records (station_id, created_at DESC, id DESC);

-- The global record list. The id tiebreaker is what makes the order TOTAL:
-- created_at defaults to now(), which is the TRANSACTION timestamp, so a
-- whole poll batch shares one value.
CREATE INDEX IF NOT EXISTS ix_records_list
  ON weather_records (created_at DESC, id DESC);

-- The proof/verify existence gate and the reconciler's match-by-txid.
CREATE INDEX IF NOT EXISTS ix_records_txid
  ON weather_records (txid) WHERE txid IS NOT NULL;

-- ReconcileCandidates.
CREATE INDEX IF NOT EXISTS ix_records_reconcile
  ON weather_records (processed_at)
  WHERE status = 'completed' AND (chain_status IS NULL OR chain_status <> 'mined');

-- ---------- stations ----------
CREATE TABLE IF NOT EXISTS stations (
  station_id        bigint      PRIMARY KEY,
  name              text        NOT NULL DEFAULT '',
  location          text        NOT NULL DEFAULT '',
  latitude          double precision,
  longitude         double precision,
  is_active         boolean     NOT NULL DEFAULT true,
  tx_records        bigint      NOT NULL DEFAULT 0,
  last_reading      timestamptz,
  -- NULLABLE, and the API never OMITS it: the frontend tests
  -- `lastTemp !== null` strictly, so an absent key renders "undefined°C".
  last_temp         double precision,
  last_conditions   text        NOT NULL DEFAULT '',
  last_block_height bigint,
  -- The TWO-argument to_tsvector is IMMUTABLE and therefore legal in a
  -- generated column. The one-argument to_tsvector(text) is only STABLE and
  -- would be rejected at CREATE TABLE. Do not "simplify" this.
  search_tsv        tsvector    GENERATED ALWAYS AS (
                      to_tsvector('english',
                        coalesce(name,'') || ' ' || coalesce(location,''))) STORED,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_stations_search ON stations USING gin (search_tsv);

-- ---------- stats: a transactionally-maintained singleton ----------
CREATE TABLE IF NOT EXISTS app_stats (
  id                smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  total_tx          bigint      NOT NULL DEFAULT 0,    -- DISTINCT on-chain transactions
  total_records     bigint      NOT NULL DEFAULT 0,    -- completed records
  last_record_write timestamptz,
  updated_at        timestamptz NOT NULL DEFAULT now()
);
INSERT INTO app_stats (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

-- ---------- the txid ledger behind app_stats.total_tx ----------
-- This table exists ONLY so total_tx can mean what it says. Complete used to do
-- `total_tx = total_tx + 1` per CALL, so a txid applied to a second processing
-- set -- a retry, a duplicated action, a chunked publish reusing one
-- transaction -- counted twice, and the dashboard's headline number drifted
-- upward with no way to notice or correct it.
--
-- The primary key IS the mechanism: Complete inserts the txid ON CONFLICT DO
-- NOTHING inside its own transaction and increments total_tx only when the
-- insert actually took a row. That makes "distinct" a property enforced by the
-- schema rather than by every caller remembering not to reuse a txid.
--
-- Deliberately NOT a foreign key onto weather_records.txid: that column is not
-- unique (one transaction publishes many records) and a completed row can later
-- be requeued, which would either block the requeue or cascade away the ledger
-- entry -- and a transaction that reached the chain stays counted regardless of
-- what happens to the rows afterwards.
CREATE TABLE IF NOT EXISTS completed_txids (
  txid       text        PRIMARY KEY,
  first_seen timestamptz NOT NULL DEFAULT now()
);

-- ---------- operator deposits ----------
CREATE TABLE IF NOT EXISTS deposits (
  suffix          text        PRIMARY KEY,
  prefix          text        NOT NULL,
  address         text        NOT NULL,
  locking_script  text        NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  txid            text,
  vout            int,
  satoshis        bigint,
  internalized_at timestamptz
);

-- ---------- preflight fingerprint cache ----------
CREATE TABLE IF NOT EXISTS app_preflight (
  fingerprint text        PRIMARY KEY,
  ok_at       timestamptz NOT NULL
);
