package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// sessionSecretKey is the settings row holding the session-cookie signing
// secret, stored hex-encoded.
const sessionSecretKey = "session_secret"

// sessionSecretBytes sizes the signing secret: 32 random bytes (256 bits),
// matching the HMAC-SHA256 block the web layer signs cookies with.
const sessionSecretBytes = 32

// SettingsRepo persists small pieces of app-generated runtime state in the
// settings key-value table — values that must survive restarts but should
// never burden deployment as env vars.
type SettingsRepo struct {
	sql *sql.DB
}

// SessionSecret returns the persistent session-cookie signing secret,
// generating and storing one on first use so deployments need no extra
// configuration and signed cookies survive restarts. Concurrent first calls
// are safe: the insert ignores an existing row and the stored value wins.
func (r *SettingsRepo) SessionSecret(ctx context.Context) ([]byte, error) {
	if secret, ok, err := r.readSecret(ctx); err != nil || ok {
		return secret, err
	}

	fresh := make([]byte, sessionSecretBytes)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("store: generate session secret: %w", err)
	}
	if _, err := r.sql.ExecContext(ctx,
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO NOTHING`,
		sessionSecretKey, hex.EncodeToString(fresh),
	); err != nil {
		return nil, fmt.Errorf("store: persist session secret: %w", err)
	}

	// Read back rather than returning fresh: if another process won the insert
	// race, everyone must sign with the secret that actually landed.
	secret, ok, err := r.readSecret(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("store: session secret missing after insert")
	}
	return secret, nil
}

// readSecret loads and decodes the stored secret, reporting whether one exists.
func (r *SettingsRepo) readSecret(ctx context.Context) ([]byte, bool, error) {
	var encoded string
	err := r.sql.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, sessionSecretKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: read session secret: %w", err)
	}
	secret, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, false, fmt.Errorf("store: decode session secret: %w", err)
	}
	return secret, true, nil
}
