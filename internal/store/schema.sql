-- impamind schema v1. Applied once on an empty database inside a single transaction.

CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE scopes (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN ('global','project')),
  slug          TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  description   TEXT NOT NULL DEFAULT '',
  hidden        INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0,1)),
  review_policy TEXT NOT NULL DEFAULT 'balanced' CHECK (review_policy IN ('strict','balanced','open')),
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL,
  deleted_at    TEXT
);
CREATE UNIQUE INDEX scopes_one_global ON scopes(kind) WHERE kind = 'global';

CREATE TRIGGER scopes_kind_immutable BEFORE UPDATE OF kind ON scopes
WHEN NEW.kind IS NOT OLD.kind
BEGIN SELECT RAISE(ABORT, 'scope kind is immutable'); END;

CREATE TABLE scope_tags (
  scope_id TEXT NOT NULL REFERENCES scopes(id),
  tag      TEXT NOT NULL,
  PRIMARY KEY (scope_id, tag)
);

CREATE TABLE project_aliases (
  alias      TEXT PRIMARY KEY,
  scope_id   TEXT NOT NULL REFERENCES scopes(id),
  kind       TEXT NOT NULL CHECK (kind IN ('git_remote','path','name')),
  created_at TEXT NOT NULL
);
CREATE INDEX project_aliases_scope ON project_aliases(scope_id);

CREATE TRIGGER project_aliases_only_projects BEFORE INSERT ON project_aliases
WHEN (SELECT kind FROM scopes WHERE id = NEW.scope_id) IS NOT 'project'
BEGIN SELECT RAISE(ABORT, 'aliases only point to project scopes'); END;

-- Identity -------------------------------------------------------------------

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  hash         TEXT NOT NULL UNIQUE,
  class        TEXT NOT NULL CHECK (class IN ('agent','human')),
  client_type  TEXT NOT NULL DEFAULT '',
  created_at   TEXT NOT NULL,
  last_used_at TEXT,
  revoked_at   TEXT
);

CREATE TABLE oauth_clients (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  redirect_uris TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  authorized_at TEXT,
  revoked_at    TEXT
);

CREATE TABLE principals (
  id              TEXT PRIMARY KEY,
  class           TEXT NOT NULL CHECK (class IN ('agent_token','human_token','admin_session','oauth_client')),
  token_id        TEXT UNIQUE REFERENCES api_tokens(id),
  oauth_client_id TEXT UNIQUE REFERENCES oauth_clients(id),
  label           TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  CHECK ((class IN ('agent_token','human_token')) = (token_id IS NOT NULL)),
  CHECK ((class = 'oauth_client') = (oauth_client_id IS NOT NULL))
);
CREATE UNIQUE INDEX principals_one_admin ON principals(class) WHERE class = 'admin_session';

-- scope_id is a scopes.id, or '*' meaning every project scope (never the global scope).
CREATE TABLE grants (
  principal_id  TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  scope_id      TEXT NOT NULL,
  can_read      INTEGER NOT NULL DEFAULT 0 CHECK (can_read IN (0,1)),
  can_write     INTEGER NOT NULL DEFAULT 0 CHECK (can_write IN (0,1)),
  can_propose   INTEGER NOT NULL DEFAULT 0 CHECK (can_propose IN (0,1)),
  can_approve   INTEGER NOT NULL DEFAULT 0 CHECK (can_approve IN (0,1)),
  cross_project INTEGER NOT NULL DEFAULT 0 CHECK (cross_project IN (0,1)),
  PRIMARY KEY (principal_id, scope_id)
);

CREATE TRIGGER grants_agents_never_approve_ins BEFORE INSERT ON grants
WHEN NEW.can_approve = 1
 AND (SELECT class FROM principals WHERE id = NEW.principal_id) IN ('agent_token','oauth_client')
BEGIN SELECT RAISE(ABORT, 'agent principals cannot hold can_approve'); END;

CREATE TRIGGER grants_agents_never_approve_upd BEFORE UPDATE OF can_approve ON grants
WHEN NEW.can_approve = 1
 AND (SELECT class FROM principals WHERE id = NEW.principal_id) IN ('agent_token','oauth_client')
BEGIN SELECT RAISE(ABORT, 'agent principals cannot hold can_approve'); END;

CREATE TRIGGER grants_scope_exists BEFORE INSERT ON grants
WHEN NEW.scope_id <> '*' AND NOT EXISTS (SELECT 1 FROM scopes WHERE id = NEW.scope_id)
BEGIN SELECT RAISE(ABORT, 'grant scope does not exist'); END;

CREATE TABLE web_sessions (
  id           TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id),
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  revoked_at   TEXT
);

CREATE TABLE oauth_codes (
  code_hash      TEXT PRIMARY KEY,
  client_id      TEXT NOT NULL REFERENCES oauth_clients(id),
  redirect_uri   TEXT NOT NULL,
  code_challenge TEXT NOT NULL,
  resource       TEXT NOT NULL DEFAULT '',
  principal_id   TEXT NOT NULL DEFAULT '',
  expires_at     TEXT NOT NULL,
  used_at        TEXT
);

CREATE TABLE oauth_tokens (
  token_hash TEXT PRIMARY KEY,
  client_id  TEXT NOT NULL REFERENCES oauth_clients(id),
  kind         TEXT NOT NULL CHECK (kind IN ('access','refresh')),
  family       TEXT NOT NULL,
  principal_id TEXT NOT NULL DEFAULT '',
  expires_at   TEXT NOT NULL,
  revoked_at TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX oauth_tokens_client ON oauth_tokens(client_id);

-- Work sessions --------------------------------------------------------------

CREATE TABLE work_sessions (
  id                      TEXT PRIMARY KEY,
  scope_id                TEXT NOT NULL REFERENCES scopes(id),
  created_by_principal_id TEXT NOT NULL REFERENCES principals(id),
  client                  TEXT NOT NULL DEFAULT '',
  agent                   TEXT NOT NULL DEFAULT '',
  model                   TEXT NOT NULL DEFAULT '',
  title                   TEXT NOT NULL DEFAULT '',
  task                    TEXT NOT NULL DEFAULT '',
  status                  TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','closed')),
  files                   TEXT NOT NULL DEFAULT '[]',
  recalled                INTEGER NOT NULL DEFAULT 0,
  observations            INTEGER NOT NULL DEFAULT 0,
  writes                  INTEGER NOT NULL DEFAULT 0,
  checkpoints             INTEGER NOT NULL DEFAULT 0,
  violations              INTEGER NOT NULL DEFAULT 0,
  duplicates_avoided      INTEGER NOT NULL DEFAULT 0,
  secrets_blocked         INTEGER NOT NULL DEFAULT 0,
  handed_off              INTEGER NOT NULL DEFAULT 0,
  started_at              TEXT NOT NULL,
  last_activity_at        TEXT NOT NULL,
  closed_at               TEXT
);
CREATE INDEX work_sessions_scope ON work_sessions(scope_id, status);
CREATE INDEX work_sessions_principal ON work_sessions(created_by_principal_id);

CREATE TRIGGER work_sessions_scope_immutable BEFORE UPDATE OF scope_id, created_by_principal_id ON work_sessions
WHEN NEW.scope_id IS NOT OLD.scope_id OR NEW.created_by_principal_id IS NOT OLD.created_by_principal_id
BEGIN SELECT RAISE(ABORT, 'work session owner and scope are immutable'); END;

CREATE TRIGGER work_sessions_closed_final BEFORE UPDATE OF status ON work_sessions
WHEN OLD.status = 'closed' AND NEW.status <> 'closed'
BEGIN SELECT RAISE(ABORT, 'closed work sessions cannot reopen'); END;

-- Memories -------------------------------------------------------------------

CREATE TABLE memories (
  seq                     INTEGER PRIMARY KEY,
  id                      TEXT NOT NULL UNIQUE,
  scope_id                TEXT NOT NULL REFERENCES scopes(id),
  session_id              TEXT REFERENCES work_sessions(id),
  kind                    TEXT NOT NULL CHECK (kind IN ('preference','pattern','knowledge','decision','requirement','business_rule','architecture','todo','observation','checkpoint','session_summary','note')),
  title                   TEXT NOT NULL,
  content                 TEXT NOT NULL,
  tier                    INTEGER NOT NULL DEFAULT 2 CHECK (tier BETWEEN 1 AND 3),
  pinned                  INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0,1)),
  status                  TEXT NOT NULL CHECK (status IN ('proposed','active','rejected','superseded','deprecated','expired','archived')),
  review_state            TEXT NOT NULL DEFAULT 'unreviewed' CHECK (review_state IN ('unreviewed','approved','needs_review','disputed')),
  confidence              REAL NOT NULL DEFAULT 0.5 CHECK (confidence >= 0 AND confidence <= 1),
  source_type             TEXT NOT NULL CHECK (source_type IN ('user_stated','agent_inferred','tool_observed','imported','human_ui')),
  created_by_principal_id TEXT NOT NULL REFERENCES principals(id),
  created_by_client       TEXT NOT NULL DEFAULT '',
  created_by_agent        TEXT NOT NULL DEFAULT '',
  created_by_model        TEXT NOT NULL DEFAULT '',
  created_session_id      TEXT,
  user_confirmed_claim    INTEGER NOT NULL DEFAULT 0 CHECK (user_confirmed_claim IN (0,1)),
  approved_by             TEXT,
  approved_via            TEXT CHECK (approved_via IS NULL OR approved_via IN ('ui','token')),
  lineage_id              TEXT NOT NULL CHECK (length(lineage_id) > 0),
  supersedes_id           TEXT,
  target_lineage_id       TEXT,
  superseded_by_id        TEXT,
  supersede_reason        TEXT NOT NULL DEFAULT '',
  derived_from_id         TEXT,
  rationale               TEXT NOT NULL DEFAULT '',
  details                 TEXT NOT NULL DEFAULT '{}',
  valid_from              TEXT,
  valid_until             TEXT,
  review_after            TEXT,
  content_hash            TEXT NOT NULL,
  version                 INTEGER NOT NULL DEFAULT 1,
  confirmations           INTEGER NOT NULL DEFAULT 1,
  last_confirmed_at       TEXT,
  access_count            INTEGER NOT NULL DEFAULT 0,
  last_accessed_at        TEXT,
  created_at              TEXT NOT NULL,
  updated_at              TEXT NOT NULL
);
CREATE UNIQUE INDEX memories_one_active_per_lineage ON memories(lineage_id) WHERE status = 'active';
CREATE INDEX memories_scope_status_kind ON memories(scope_id, status, kind);
CREATE INDEX memories_scope_tier ON memories(scope_id, tier);
CREATE INDEX memories_session ON memories(session_id);
CREATE INDEX memories_scope_hash ON memories(scope_id, content_hash);
CREATE INDEX memories_review_after ON memories(review_after) WHERE status = 'active';
CREATE INDEX memories_valid_until ON memories(valid_until) WHERE valid_until IS NOT NULL;
CREATE INDEX memories_supersedes ON memories(supersedes_id) WHERE supersedes_id IS NOT NULL;
CREATE INDEX memories_lineage ON memories(lineage_id);

CREATE TRIGGER memories_scope_immutable BEFORE UPDATE OF scope_id ON memories
WHEN NEW.scope_id IS NOT OLD.scope_id
BEGIN SELECT RAISE(ABORT, 'memory scope is immutable'); END;

CREATE TRIGGER memories_session_same_scope BEFORE INSERT ON memories
WHEN NEW.session_id IS NOT NULL
 AND (SELECT scope_id FROM work_sessions WHERE id = NEW.session_id) IS NOT NEW.scope_id
BEGIN SELECT RAISE(ABORT, 'session belongs to another scope'); END;

-- A memory is protected when it lives in the global scope, or when the scope
-- policy makes its kind require human approval.
CREATE TRIGGER memories_protected_insert BEFORE INSERT ON memories
WHEN NEW.status = 'active' AND (
  (SELECT kind FROM scopes WHERE id = NEW.scope_id) = 'global'
  OR ((SELECT review_policy FROM scopes WHERE id = NEW.scope_id) = 'balanced' AND NEW.kind IN ('architecture','business_rule'))
  OR ((SELECT review_policy FROM scopes WHERE id = NEW.scope_id) = 'strict' AND NEW.kind IN ('decision','business_rule','architecture','requirement','preference'))
)
BEGIN SELECT RAISE(ABORT, 'protected memory must be approved before it becomes active'); END;

CREATE TRIGGER memories_protected_activation BEFORE UPDATE OF status ON memories
WHEN NEW.status = 'active' AND OLD.status = 'proposed' AND (
  (SELECT kind FROM scopes WHERE id = NEW.scope_id) = 'global'
  OR ((SELECT review_policy FROM scopes WHERE id = NEW.scope_id) = 'balanced' AND NEW.kind IN ('architecture','business_rule'))
  OR ((SELECT review_policy FROM scopes WHERE id = NEW.scope_id) = 'strict' AND NEW.kind IN ('decision','business_rule','architecture','requirement','preference'))
) AND NOT EXISTS (SELECT 1 FROM approvals WHERE memory_id = NEW.id AND to_status = 'active')
BEGIN SELECT RAISE(ABORT, 'protected memory must be approved before it becomes active'); END;

CREATE TRIGGER memories_user_stated_insert BEFORE INSERT ON memories
WHEN NEW.source_type = 'user_stated'
BEGIN SELECT RAISE(ABORT, 'user_stated requires a human approval'); END;

CREATE TRIGGER memories_user_stated_update BEFORE UPDATE OF source_type ON memories
WHEN NEW.source_type = 'user_stated' AND OLD.source_type IS NOT 'user_stated'
 AND NOT EXISTS (SELECT 1 FROM approvals WHERE memory_id = NEW.id AND marks_user_stated = 1)
BEGIN SELECT RAISE(ABORT, 'user_stated requires a human approval'); END;

CREATE TRIGGER memories_no_hard_delete BEFORE DELETE ON memories
WHEN NOT EXISTS (SELECT 1 FROM tombstones WHERE memory_id = OLD.id)
BEGIN SELECT RAISE(ABORT, 'hard delete requires a tombstone'); END;

CREATE VIRTUAL TABLE memories_fts USING fts5(
  title, content,
  content = 'memories', content_rowid = 'seq',
  tokenize = 'porter unicode61 remove_diacritics 2'
);

CREATE TRIGGER memories_fts_ins AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, title, content) VALUES (NEW.seq, NEW.title, NEW.content);
END;
CREATE TRIGGER memories_fts_del AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, title, content) VALUES ('delete', OLD.seq, OLD.title, OLD.content);
END;
CREATE TRIGGER memories_fts_upd AFTER UPDATE OF title, content ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, title, content) VALUES ('delete', OLD.seq, OLD.title, OLD.content);
  INSERT INTO memories_fts(rowid, title, content) VALUES (NEW.seq, NEW.title, NEW.content);
END;

CREATE TABLE memory_tags (
  memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  tag       TEXT NOT NULL,
  PRIMARY KEY (memory_id, tag)
);
CREATE INDEX memory_tags_tag ON memory_tags(tag);

CREATE TABLE memory_versions (
  id                      INTEGER PRIMARY KEY,
  memory_id               TEXT NOT NULL,
  version                 INTEGER NOT NULL,
  snapshot                TEXT NOT NULL,
  changed_by_principal_id TEXT NOT NULL,
  changed_by_agent        TEXT NOT NULL DEFAULT '',
  change_type             TEXT NOT NULL,
  change_reason           TEXT NOT NULL DEFAULT '',
  created_at              TEXT NOT NULL,
  UNIQUE (memory_id, version)
);
CREATE TRIGGER memory_versions_no_update BEFORE UPDATE ON memory_versions
BEGIN SELECT RAISE(ABORT, 'memory_versions is append-only'); END;
CREATE TRIGGER memory_versions_no_delete BEFORE DELETE ON memory_versions
BEGIN SELECT RAISE(ABORT, 'memory_versions is append-only'); END;

CREATE TABLE memory_evidence (
  id                    TEXT PRIMARY KEY,
  memory_id             TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  type                  TEXT NOT NULL CHECK (type IN ('file','commit','url','test','conversation','issue','log','manifest')),
  ref                   TEXT NOT NULL DEFAULT '',
  note                  TEXT NOT NULL DEFAULT '',
  added_by_principal_id TEXT NOT NULL,
  created_at            TEXT NOT NULL
);
CREATE INDEX memory_evidence_memory ON memory_evidence(memory_id);

CREATE TABLE approvals (
  id                TEXT PRIMARY KEY,
  memory_id         TEXT NOT NULL,
  from_status       TEXT NOT NULL,
  to_status         TEXT NOT NULL,
  approver_class    TEXT NOT NULL CHECK (approver_class IN ('admin_session','human_token')),
  approver_id       TEXT NOT NULL,
  marks_user_stated INTEGER NOT NULL DEFAULT 0 CHECK (marks_user_stated IN (0,1)),
  note              TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL
);
CREATE INDEX approvals_memory ON approvals(memory_id);
CREATE TRIGGER approvals_no_update BEFORE UPDATE ON approvals
BEGIN SELECT RAISE(ABORT, 'approvals is append-only'); END;
CREATE TRIGGER approvals_no_delete BEFORE DELETE ON approvals
BEGIN SELECT RAISE(ABORT, 'approvals is append-only'); END;
CREATE TRIGGER approvals_human_only BEFORE INSERT ON approvals
WHEN (SELECT class FROM principals WHERE id = NEW.approver_id) IS NOT NEW.approver_class
BEGIN SELECT RAISE(ABORT, 'approver must be a human principal'); END;

CREATE TABLE tombstones (
  id                      TEXT PRIMARY KEY,
  memory_id               TEXT NOT NULL UNIQUE,
  scope_id                TEXT NOT NULL,
  title                   TEXT NOT NULL,
  deleted_by_principal_id TEXT NOT NULL,
  reason                  TEXT NOT NULL,
  created_at              TEXT NOT NULL
);

CREATE TABLE links (
  source_id               TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  target_id               TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  label                   TEXT NOT NULL DEFAULT 'related',
  created_by_principal_id TEXT NOT NULL,
  created_at              TEXT NOT NULL,
  PRIMARY KEY (source_id, target_id),
  CHECK (source_id <> target_id)
);
CREATE INDEX links_target ON links(target_id);

-- Same scope, or project -> global. Never global -> project, never across projects.
CREATE TRIGGER links_scope_rule BEFORE INSERT ON links
WHEN (SELECT scope_id FROM memories WHERE id = NEW.source_id) IS NOT (SELECT scope_id FROM memories WHERE id = NEW.target_id)
 AND (SELECT s.kind FROM memories m JOIN scopes s ON s.id = m.scope_id WHERE m.id = NEW.target_id) IS NOT 'global'
BEGIN SELECT RAISE(ABORT, 'links cannot cross project scopes'); END;

CREATE TRIGGER links_no_global_to_project BEFORE INSERT ON links
WHEN (SELECT s.kind FROM memories m JOIN scopes s ON s.id = m.scope_id WHERE m.id = NEW.source_id) = 'global'
 AND (SELECT s.kind FROM memories m JOIN scopes s ON s.id = m.scope_id WHERE m.id = NEW.target_id) = 'project'
BEGIN SELECT RAISE(ABORT, 'global memories cannot link to project memories'); END;

CREATE TABLE review_items (
  id           TEXT PRIMARY KEY,
  scope_id     TEXT NOT NULL REFERENCES scopes(id),
  type         TEXT NOT NULL CHECK (type IN ('proposed','conflict','duplicate','stale','expired','tier_suggestion')),
  dedupe_key   TEXT NOT NULL UNIQUE,
  memory_ids   TEXT NOT NULL,
  payload      TEXT NOT NULL DEFAULT '{}',
  status       TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')),
  created_at   TEXT NOT NULL,
  resolved_by  TEXT,
  resolution   TEXT,
  resolved_at  TEXT
);
CREATE INDEX review_items_scope ON review_items(scope_id, status, type);

CREATE TABLE embeddings (
  memory_seq INTEGER PRIMARY KEY REFERENCES memories(seq) ON DELETE CASCADE,
  scope_id   TEXT NOT NULL,
  model      TEXT NOT NULL,
  dims       INTEGER NOT NULL,
  vector     BLOB NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX embeddings_scope ON embeddings(scope_id);

CREATE TABLE logs (
  id           INTEGER PRIMARY KEY,
  ts           TEXT NOT NULL,
  source       TEXT NOT NULL,
  principal_id TEXT NOT NULL DEFAULT '',
  scope_id     TEXT NOT NULL DEFAULT '',
  operation    TEXT NOT NULL,
  level        TEXT NOT NULL DEFAULT 'info' CHECK (level IN ('info','warn','error')),
  status       INTEGER NOT NULL DEFAULT 0,
  detail       TEXT NOT NULL DEFAULT '',
  duration_us  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX logs_ts ON logs(ts);
CREATE INDEX logs_operation ON logs(operation);

CREATE TABLE probe (
  id    INTEGER PRIMARY KEY,
  note  TEXT NOT NULL,
  at    TEXT NOT NULL
);

-- Encrypted credential vault. Plaintext never enters memories or recall.
CREATE TABLE vault_entries (
  id                      TEXT PRIMARY KEY,
  scope_id                TEXT NOT NULL REFERENCES scopes(id),
  key                     TEXT NOT NULL,
  label                   TEXT NOT NULL DEFAULT '',
  hint                    TEXT NOT NULL DEFAULT '',
  ciphertext              BLOB NOT NULL,
  created_by_principal_id TEXT NOT NULL,
  created_at              TEXT NOT NULL,
  updated_at              TEXT NOT NULL,
  last_accessed_at        TEXT,
  UNIQUE (scope_id, key),
  CHECK (length(key) >= 1 AND length(key) <= 128)
);
CREATE INDEX vault_entries_scope ON vault_entries(scope_id);
