package protocol

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"impamind/internal/store"
)

// VaultEntryOut is returned by list/get-meta (never includes the secret).
type VaultEntryOut struct {
	ID         string `json:"id"`
	Scope      string `json:"scope"`
	Key        string `json:"key"`
	Label      string `json:"label"`
	Hint       string `json:"hint,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	LastAccess string `json:"last_accessed_at,omitempty"`
	Ref        string `json:"ref"` // vault:<scope>/<key> — safe to put in memories
}

// VaultRevealOut is returned only by an explicit get; value is the plaintext.
type VaultRevealOut struct {
	VaultEntryOut
	Value string `json:"value"`
}

type VaultPutIn struct {
	Project string `json:"project,omitempty"`
	Scope   string `json:"scope,omitempty"` // global or project slug
	Key     string `json:"key"`
	Label   string `json:"label,omitempty"`
	Hint    string `json:"hint,omitempty"`
	Value   string `json:"value"`
}

type VaultKeyIn struct {
	Project string `json:"project,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Key     string `json:"key"`
}

type VaultListIn struct {
	Project string `json:"project,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

func (s *Service) vaultAEAD() (cipher.AEAD, error) {
	secret := strings.TrimSpace(s.Cfg.Secret)
	if secret == "" {
		return nil, newErr(http.StatusServiceUnavailable, "vault_unavailable", "IMPAMIND_SECRET is required to use the credential vault")
	}
	sum := sha256.Sum256([]byte("impamind-vault-v1|" + secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Service) sealVault(value string) ([]byte, error) {
	aead, err := s.vaultAEAD()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(value), nil), nil
}

func (s *Service) openVault(blob []byte) (string, error) {
	aead, err := s.vaultAEAD()
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(blob) < ns {
		return "", fmt.Errorf("ciphertext truncated")
	}
	plain, err := aead.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return "", newErr(http.StatusInternalServerError, "vault_decrypt", "could not decrypt vault entry (wrong IMPAMIND_SECRET?)")
	}
	return string(plain), nil
}

func (s *Service) vaultScopeID(ctx context.Context, call Call, project, scope string) (scopeID, slug string, err error) {
	want := strings.TrimSpace(scope)
	if want == "" {
		want = strings.TrimSpace(project)
	}
	if want == "" || want == "project" {
		if call.SS.Project().ID == "" {
			return "", "", newErr(http.StatusBadRequest, "invalid", "project or scope is required")
		}
		return call.SS.Project().ID, call.SS.Project().Slug, nil
	}
	if want == "global" {
		return call.SS.GlobalID(), "global", nil
	}
	sc, err := s.ScopeBySlug(ctx, call, want)
	if err != nil {
		return "", "", err
	}
	return sc.ID, sc.Slug, nil
}

func vaultRef(slug, key string) string {
	return "vault:" + slug + "/" + key
}

func (s *Service) vaultOut(m store.VaultMeta, slug string) VaultEntryOut {
	return VaultEntryOut{
		ID: m.ID, Scope: slug, Key: m.Key, Label: m.Label, Hint: m.Hint,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, LastAccess: m.LastAccess,
		Ref: vaultRef(slug, m.Key),
	}
}

// VaultList returns metadata only (no secret values).
func (s *Service) VaultList(ctx context.Context, call Call, in VaultListIn) ([]VaultEntryOut, error) {
	scopeID, slug, err := s.vaultScopeID(ctx, call, in.Project, in.Scope)
	if err != nil {
		return nil, err
	}
	list, err := s.St.ListVault(ctx, call.SS, []string{scopeID})
	if err != nil {
		return nil, err
	}
	out := make([]VaultEntryOut, 0, len(list))
	for _, m := range list {
		out = append(out, s.vaultOut(m, slug))
	}
	return out, nil
}

// VaultPut stores or replaces an encrypted credential (needs write on the scope).
func (s *Service) VaultPut(ctx context.Context, call Call, in VaultPutIn) (VaultEntryOut, error) {
	if strings.TrimSpace(in.Value) == "" {
		return VaultEntryOut{}, newErr(http.StatusBadRequest, "invalid", "value is required")
	}
	if len(in.Value) > 32*1024 {
		return VaultEntryOut{}, newErr(http.StatusBadRequest, "invalid", "value too large (max 32KiB)")
	}
	scopeID, slug, err := s.vaultScopeID(ctx, call, in.Project, in.Scope)
	if err != nil {
		return VaultEntryOut{}, err
	}
	blob, err := s.sealVault(in.Value)
	if err != nil {
		return VaultEntryOut{}, err
	}
	m, err := s.St.PutVault(ctx, call.SS, scopeID, in.Key, in.Label, in.Hint, blob)
	if err != nil {
		return VaultEntryOut{}, err
	}
	s.audit(call, "vault_put", scopeID, 200, m.Key)
	return s.vaultOut(m, slug), nil
}

// VaultGet decrypts and returns the value. Audited. Agents may get when they have read.
func (s *Service) VaultGet(ctx context.Context, call Call, in VaultKeyIn) (VaultRevealOut, error) {
	scopeID, slug, err := s.vaultScopeID(ctx, call, in.Project, in.Scope)
	if err != nil {
		return VaultRevealOut{}, err
	}
	meta, err := s.St.GetVaultMeta(ctx, call.SS, scopeID, in.Key)
	if err != nil {
		return VaultRevealOut{}, err
	}
	blob, err := s.St.GetVaultCipher(ctx, call.SS, scopeID, in.Key)
	if err != nil {
		return VaultRevealOut{}, err
	}
	value, err := s.openVault(blob)
	if err != nil {
		return VaultRevealOut{}, err
	}
	s.audit(call, "vault_get", scopeID, 200, meta.Key)
	return VaultRevealOut{VaultEntryOut: s.vaultOut(meta, slug), Value: value}, nil
}

// VaultDelete removes a vault entry. Human-only.
func (s *Service) VaultDelete(ctx context.Context, call Call, in VaultKeyIn) error {
	if err := s.humanOnly(call); err != nil {
		return err
	}
	scopeID, _, err := s.vaultScopeID(ctx, call, in.Project, in.Scope)
	if err != nil {
		return err
	}
	if err := s.St.DeleteVault(ctx, call.SS, scopeID, in.Key); err != nil {
		return err
	}
	s.audit(call, "vault_delete", scopeID, 200, in.Key)
	return nil
}

// EncodeVaultValueB64 is a test helper.
func EncodeVaultValueB64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
