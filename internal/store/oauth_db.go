package store

import "context"

var (
	qOAuthClientInsert = system("oauth.client_insert", "oauth", `INSERT INTO oauth_clients(id, name, redirect_uris, created_at) VALUES (?, ?, ?, ?)`)
	qOAuthClientGet    = system("oauth.client_get", "oauth", `SELECT id, name, redirect_uris, created_at, COALESCE(revoked_at,'') FROM oauth_clients WHERE id = ? AND revoked_at IS NULL`)
	qOAuthCodeInsert   = system("oauth.code_insert", "oauth", `INSERT INTO oauth_codes(code_hash, client_id, redirect_uri, code_challenge, resource, principal_id, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	qOAuthCodeTake     = system("oauth.code_take", "oauth", `UPDATE oauth_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?`)
	qOAuthCodeGet      = system("oauth.code_get", "oauth", `SELECT client_id, redirect_uri, code_challenge, principal_id, expires_at, COALESCE(used_at,'') FROM oauth_codes WHERE code_hash = ?`)
	qOAuthTokenInsert  = system("oauth.token_insert", "oauth", `INSERT INTO oauth_tokens(token_hash, client_id, kind, family, principal_id, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	qOAuthTokenGet     = system("oauth.token_get", "oauth", `SELECT client_id, kind, family, principal_id, expires_at, COALESCE(revoked_at,'') FROM oauth_tokens WHERE token_hash = ?`)
	qOAuthTokenRevoke  = system("oauth.token_revoke", "oauth", `UPDATE oauth_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`)
	qOAuthFamilyRevoke = system("oauth.family_revoke", "oauth", `UPDATE oauth_tokens SET revoked_at = ? WHERE family = ? AND revoked_at IS NULL`)
	qPrincipalOAuth    = system("oauth.principal_insert", "oauth", `INSERT INTO principals(id, class, oauth_client_id, label, created_at) VALUES (?, 'oauth_client', ?, ?, ?)`)
	qPrincipalByClient = system("oauth.principal_by_client", "oauth", `SELECT id FROM principals WHERE oauth_client_id = ?`)
)

// OAuthClient is a dynamically registered public client.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs string
	CreatedAt    string
	RevokedAt    string
}

func (s *Store) CreateOAuthClient(ctx context.Context, name, redirectURIs string) (OAuthClient, error) {
	c := OAuthClient{ID: newID(), Name: name, RedirectURIs: redirectURIs, CreatedAt: now()}
	err := s.write(ctx, func(w wx) error {
		_, err := w.exec(qOAuthClientInsert, c.ID, name, redirectURIs, c.CreatedAt)
		return err
	})
	return c, err
}

func (s *Store) OAuthClient(ctx context.Context, id string) (OAuthClient, error) {
	var c OAuthClient
	err := s.reader(ctx).row(qOAuthClientGet, id).Scan(&c.ID, &c.Name, &c.RedirectURIs, &c.CreatedAt, &c.RevokedAt)
	return c, notFound(err)
}

// EnsureOAuthPrincipal returns the principal of a client, creating it once.
func (s *Store) EnsureOAuthPrincipal(ctx context.Context, clientID, label string) (string, error) {
	var id string
	err := s.reader(ctx).row(qPrincipalByClient, clientID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if notFound(err) != ErrNotFound {
		return "", err
	}
	id = newID()
	err = s.write(ctx, func(w wx) error {
		_, err := w.exec(qPrincipalOAuth, id, clientID, label, now())
		return err
	})
	if isUniqueViolation(err) {
		err = s.reader(ctx).row(qPrincipalByClient, clientID).Scan(&id)
	}
	if err == nil {
		s.bumpAuth()
	}
	return id, err
}

// OAuthCode is a one-time authorization code.
type OAuthCode struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	PrincipalID   string
	ExpiresAt     string
	UsedAt        string
}

func (s *Store) SaveOAuthCode(ctx context.Context, hash string, c OAuthCode) error {
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qOAuthCodeInsert, hash, c.ClientID, c.RedirectURI, c.CodeChallenge, "", c.PrincipalID, c.ExpiresAt)
		return err
	})
}

// TakeOAuthCode marks a code used and returns it. A second take fails.
func (s *Store) TakeOAuthCode(ctx context.Context, hash string) (OAuthCode, error) {
	var c OAuthCode
	err := s.write(ctx, func(w wx) error {
		n, err := w.execN(qOAuthCodeTake, now(), hash, now())
		if err != nil || n != 1 {
			if err == nil {
				err = ErrNotFound
			}
			return err
		}
		return w.row(qOAuthCodeGet, hash).Scan(&c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.PrincipalID, &c.ExpiresAt, &c.UsedAt)
	})
	return c, err
}

// OAuthToken is a stored access or refresh token.
type OAuthToken struct {
	ClientID    string
	Kind        string
	Family      string
	PrincipalID string
	ExpiresAt   string
	RevokedAt   string
}

func (s *Store) SaveOAuthToken(ctx context.Context, hash string, t OAuthToken) error {
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qOAuthTokenInsert, hash, t.ClientID, t.Kind, t.Family, t.PrincipalID, t.ExpiresAt, now())
		return err
	})
}

func (s *Store) OAuthToken(ctx context.Context, hash string) (OAuthToken, error) {
	var t OAuthToken
	err := s.reader(ctx).row(qOAuthTokenGet, hash).Scan(&t.ClientID, &t.Kind, &t.Family, &t.PrincipalID, &t.ExpiresAt, &t.RevokedAt)
	return t, notFound(err)
}

func (s *Store) RevokeOAuthToken(ctx context.Context, hash string) error {
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qOAuthTokenRevoke, now(), hash)
		return err
	})
}

// RevokeOAuthFamily revokes every token in a refresh family (reuse detection).
func (s *Store) RevokeOAuthFamily(ctx context.Context, family string) error {
	return s.write(ctx, func(w wx) error {
		_, err := w.exec(qOAuthFamilyRevoke, now(), family)
		if err == nil {
			s.bumpAuth()
		}
		return err
	})
}
