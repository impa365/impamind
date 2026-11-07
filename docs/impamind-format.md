# Formato `.impamind` (v1)

Arquivo portátil de clone/backup do ImpaMind. Extensão `.impamind` = ZIP.

## Layout

```
manifest.json
payload.json
vault.salt
vault.sealed
```

### `manifest.json`

| Campo | Tipo | Descrição |
|---|---|---|
| `format` | string | sempre `"impamind-bundle"` |
| `format_version` | string | `"1"` |
| `schema_version` | string | versão do schema SQLite de origem |
| `exported_at` | string | RFC3339 UTC |
| `include_global` | bool | se memórias/vault global entraram |
| `include_sessions` | bool | v1: sempre `false` |
| `scopes` | array | `{slug, kind, name}` exportados |
| `counts` | object | `projects`, `memories`, `links`, `vault_entries`, `aliases` |
| `content_hash` | string | sha256 hex de `payload.json` + `vault.sealed` |

### `payload.json`

JSON UTF-8 com:

- `projects[]` — slug, name, description, hidden, review_policy, tags
- `aliases[]` — project_slug, kind, value (valor original normalizável)
- `memories[]` — id (origem), scope_slug, kind, title, content, tier, pinned, status, review_state, confidence, source_type, tags, evidence[], rationale, details, valid_*, review_after
- `links[]` — source_id, target_id, label (IDs da origem)

Só memórias `active` (e `proposed` opcionalmente não — v1: só `active`). Global só se `include_global`.

### Cofre (`vault.salt` + `vault.sealed`)

Lista JSON `[{scope_slug,key,label,hint,value},…]` cifrada com:

1. **Argon2id** (passphrase + salt 16 bytes) → chave 32 bytes  
   - time=3, memory=64 MiB, threads=4
2. **AES-256-GCM** — nonce prepended ao ciphertext

No import, cada valor é re-selado com o `IMPAMIND_SECRET` do destino.

## Segurança

O arquivo **contém segredos em claro sob a passphrase**. Tratar como backup de cofre. Não versionar no git.

## Fora do v1

api_tokens, web_sessions, oauth, embeddings, logs, principals/grants, work_sessions.
