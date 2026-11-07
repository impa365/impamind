package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"impamind/internal/auth"
)

var vaultKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,127}$`)

var (
	qVaultList = scoped("vault.list", `SELECT v.id, v.scope_id, v.key, v.label, v.hint, v.created_by_principal_id, v.created_at, v.updated_at, COALESCE(v.last_accessed_at,'')
		FROM vault_entries v WHERE {{scope:v}} ORDER BY v.key`)
	qVaultGetMeta = scoped("vault.get_meta", `SELECT v.id, v.scope_id, v.key, v.label, v.hint, v.created_by_principal_id, v.created_at, v.updated_at, COALESCE(v.last_accessed_at,'')
		FROM vault_entries v WHERE v.scope_id = ? AND v.key = ? AND {{scope:v}}`)
	qVaultGetCipher = scoped("vault.get_cipher", `SELECT v.ciphertext FROM vault_entries v WHERE v.scope_id = ? AND v.key = ? AND {{scope:v}}`)
	qVaultInsert    = scoped("vault.insert", `INSERT INTO vault_entries(id, scope_id, key, label, hint, ciphertext, created_by_principal_id, created_at, updated_at)
		SELECT ?, id, ?, ?, ?, ?, ?, ?, ? FROM scopes WHERE id = ? AND deleted_at IS NULL AND {{in:id}}`)
	qVaultUpdate = scoped("vault.update", `UPDATE vault_entries SET label = ?, hint = ?, ciphertext = ?, updated_at = ?
		WHERE scope_id = ? AND key = ? AND {{in:scope_id}}`)
	qVaultTouch  = scoped("vault.touch", `UPDATE vault_entries SET last_accessed_at = ? WHERE scope_id = ? AND key = ? AND {{in:scope_id}}`)
	qVaultDelete = scoped("vault.delete", `DELETE FROM vault_entries WHERE scope_id = ? AND key = ? AND {{in:scope_id}}`)
)

// VaultMeta is a vault entry without the secret value.
type VaultMeta struct {
	ID          string `json:"id"`
	ScopeID     string `json:"scope_id"`
	Key         string `json:"key"`
	Label       string `json:"label"`
	Hint        string `json:"hint,omitempty"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	LastAccess  string `json:"last_accessed_at,omitempty"`
}

func NormalizeVaultKey(key string) (string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, " ", "-")
	if !vaultKeyRe.MatchString(key) {
		return "", fmt.Errorf("%w: vault key must be lowercase slug (a-z0-9._/-)", ErrInvalid)
	}
	return key, nil
}

func (s *Store) ListVault(ctx context.Context, ss auth.ScopeSet, scopeIDs []string) ([]VaultMeta, error) {
	scopeJSON := ids(intersect(ss, scopeIDs))
	list, err := collectRows(s.reader(ctx).q(qVaultList, scopeJSON), scanVaultMeta)
	if list == nil {
		list = []VaultMeta{}
	}
	return list, err
}

func (s *Store) GetVaultMeta(ctx context.Context, ss auth.ScopeSet, scopeID, key string) (VaultMeta, error) {
	key, err := NormalizeVaultKey(key)
	if err != nil {
		return VaultMeta{}, err
	}
	if !ss.CanRead(scopeID) {
		return VaultMeta{}, ErrNotFound
	}
	var m VaultMeta
	err = s.reader(ctx).row(qVaultGetMeta, scopeID, key, readable(ss)).Scan(
		&m.ID, &m.ScopeID, &m.Key, &m.Label, &m.Hint, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt, &m.LastAccess)
	return m, notFound(err)
}

func (s *Store) GetVaultCipher(ctx context.Context, ss auth.ScopeSet, scopeID, key string) ([]byte, error) {
	key, err := NormalizeVaultKey(key)
	if err != nil {
		return nil, err
	}
	if !ss.CanRead(scopeID) {
		return nil, ErrNotFound
	}
	var blob []byte
	err = s.reader(ctx).row(qVaultGetCipher, scopeID, key, readable(ss)).Scan(&blob)
	if err != nil {
		return nil, notFound(err)
	}
	_ = s.write(ctx, func(w wx) error {
		_, err := w.exec(qVaultTouch, now(), scopeID, key, readable(ss))
		return err
	})
	return blob, nil
}

func (s *Store) PutVault(ctx context.Context, ss auth.ScopeSet, scopeID, key, label, hint string, ciphertext []byte) (VaultMeta, error) {
	key, err := NormalizeVaultKey(key)
	if err != nil {
		return VaultMeta{}, err
	}
	if !ss.CanWrite(scopeID) {
		return VaultMeta{}, ErrForbidden
	}
	if len(ciphertext) < 12 {
		return VaultMeta{}, fmt.Errorf("%w: ciphertext too short", ErrInvalid)
	}
	label = strings.TrimSpace(label)
	hint = strings.TrimSpace(hint)
	if len(label) > 200 {
		label = label[:200]
	}
	if len(hint) > 500 {
		hint = hint[:500]
	}
	ts := now()
	existing, err := s.GetVaultMeta(ctx, ss, scopeID, key)
	if err == nil {
		err = s.write(ctx, func(w wx) error {
			n, err := w.execN(qVaultUpdate, label, hint, ciphertext, ts, scopeID, key, writable(ss))
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
			return nil
		})
		if err != nil {
			return VaultMeta{}, err
		}
		existing.Label, existing.Hint, existing.UpdatedAt = label, hint, ts
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return VaultMeta{}, err
	}
	m := VaultMeta{
		ID: newID(), ScopeID: scopeID, Key: key, Label: label, Hint: hint,
		CreatedBy: ss.Principal().ID, CreatedAt: ts, UpdatedAt: ts,
	}
	err = s.write(ctx, func(w wx) error {
		n, err := w.execN(qVaultInsert, m.ID, key, label, hint, ciphertext, m.CreatedBy, ts, ts, scopeID, writable(ss))
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: vault key already exists", ErrConflict)
			}
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
		return nil
	})
	return m, err
}

func (s *Store) DeleteVault(ctx context.Context, ss auth.ScopeSet, scopeID, key string) error {
	key, err := NormalizeVaultKey(key)
	if err != nil {
		return err
	}
	if !ss.CanWrite(scopeID) {
		return ErrForbidden
	}
	var n int64
	err = s.write(ctx, func(w wx) (err error) {
		n, err = w.execN(qVaultDelete, scopeID, key, writable(ss))
		return err
	})
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func scanVaultMeta(r rowScanner) (VaultMeta, error) {
	var m VaultMeta
	err := r.Scan(&m.ID, &m.ScopeID, &m.Key, &m.Label, &m.Hint, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt, &m.LastAccess)
	return m, err
}
