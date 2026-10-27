package domainconnect

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/secrets"
)

// KeyID is the host of the current key's TXT records at PubKeyDomain. A
// new key takes the next id, so the old one can stay published while
// links signed with it are still open.
const KeyID = "_dck1"

// DB is what LoadKey needs from a pool.
type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// LoadKey gives the server's signing key, making and storing it (sealed)
// the first time. Servers that start together end up with the same key.
func LoadKey(ctx context.Context, db DB, box *secrets.Box) (*Key, error) {
	if box == nil {
		return nil, errors.New("domain connect: no secret key to seal the signing key")
	}
	for range 2 {
		var sealed []byte
		err := db.QueryRow(ctx, `SELECT private_key FROM domain_connect_keys WHERE id = $1`, KeyID).Scan(&sealed)
		if err == nil {
			der, err := box.Open(sealed, []byte(KeyID))
			if err != nil {
				return nil, err
			}
			priv, err := x509.ParsePKCS1PrivateKey(der)
			if err != nil {
				return nil, err
			}
			return NewKey(KeyID, priv), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		sealed = box.Seal(x509.MarshalPKCS1PrivateKey(priv), []byte(KeyID))
		if _, err := db.Exec(ctx, `INSERT INTO domain_connect_keys (id, private_key) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`, KeyID, sealed); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("domain connect: the signing key could not be stored")
}

func marshalPublic(pub *rsa.PublicKey) ([]byte, error) { return x509.MarshalPKIXPublicKey(pub) }
