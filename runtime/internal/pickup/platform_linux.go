//go:build linux

package pickup

import (
	"database/sql"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/devicekeys"
)

func NewPlatform(db *sql.DB) (*Service, error) {
	vault, err := devicekeys.Load()
	if err != nil {
		return nil, err
	}
	encryption, err := vault.Derive("pickup-encryption")
	if err != nil {
		return nil, err
	}
	defer clear(encryption)
	lookup, err := vault.Derive("pickup-lookup")
	if err != nil {
		return nil, err
	}
	defer clear(lookup)
	return New(db, encryption, lookup)
}
