package treasury

import (
	"database/sql"
	"errors"
)

const DefaultMainnetRPC = "https://bsc-dataseed.bnbchain.org"

type PublicConfig struct {
	RPCURL            string
	XPUB              string
	ReferenceAddress  string
	ChainID           uint64
	Confirmations     uint64
	MonitoringSeconds uint64
	PublicInvoicing   bool
}

func EnsurePublicConfig(db *sql.DB) error {
	if _, e := db.Exec(`CREATE TABLE IF NOT EXISTS treasury_public_config(id INTEGER PRIMARY KEY CHECK(id=1),rpc_url TEXT NOT NULL DEFAULT '',xpub_fingerprint TEXT NOT NULL DEFAULT '',reference_address TEXT NOT NULL DEFAULT '',chain_id INTEGER NOT NULL DEFAULT 56,updated_at TEXT NOT NULL DEFAULT '',rpc_cipher BLOB,confirmations INTEGER NOT NULL DEFAULT 12,monitoring_seconds INTEGER NOT NULL DEFAULT 15)`); e != nil {
		return e
	}
	for _, q := range []string{
		`ALTER TABLE treasury_public_config ADD COLUMN rpc_cipher BLOB`,
		`ALTER TABLE treasury_public_config ADD COLUMN confirmations INTEGER NOT NULL DEFAULT 12`,
		`ALTER TABLE treasury_public_config ADD COLUMN monitoring_seconds INTEGER NOT NULL DEFAULT 15`,
		`ALTER TABLE treasury_public_config ADD COLUMN xpub_cipher BLOB`,
		`ALTER TABLE treasury_public_config ADD COLUMN public_invoicing INTEGER NOT NULL DEFAULT 0`,
	} {
		_, _ = db.Exec(q)
	}
	_, e := db.Exec(`INSERT OR IGNORE INTO treasury_public_config(id,rpc_url,chain_id,updated_at,confirmations,monitoring_seconds) VALUES(1,?,56,datetime('now'),12,15)`, DefaultMainnetRPC)
	return e
}

func LoadPublicConfig(db *sql.DB, open func([]byte) (string, error)) (PublicConfig, error) {
	if e := EnsurePublicConfig(db); e != nil {
		return PublicConfig{}, e
	}
	var c PublicConfig
	var cipher, xpubCipher []byte
	var enabled int
	if e := db.QueryRow(`SELECT rpc_url,chain_id,confirmations,monitoring_seconds,COALESCE(rpc_cipher,X''),COALESCE(xpub_cipher,X''),reference_address,public_invoicing FROM treasury_public_config WHERE id=1`).Scan(&c.RPCURL, &c.ChainID, &c.Confirmations, &c.MonitoringSeconds, &cipher, &xpubCipher, &c.ReferenceAddress, &enabled); e != nil {
		return c, e
	}
	c.PublicInvoicing = enabled == 1
	if len(cipher) > 0 {
		if open == nil {
			return c, errors.New("encrypted RPC configuration unavailable")
		}
		clear, e := open(cipher)
		if e != nil {
			return c, e
		}
		c.RPCURL = clear
	}
	if len(xpubCipher) > 0 {
		if open == nil {
			return c, errors.New("encrypted xpub configuration unavailable")
		}
		clear, e := open(xpubCipher)
		if e != nil {
			return c, e
		}
		c.XPUB = clear
	}
	if c.RPCURL == "" || c.ChainID != 56 {
		return c, errors.New("invalid BSC mainnet configuration")
	}
	return c, nil
}
