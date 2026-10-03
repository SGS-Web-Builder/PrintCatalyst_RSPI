//go:build linux

package kiosk

import (
	"context"
	"database/sql"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/devicekeys"
)

func NewPlatform(db *sql.DB, p Claimer, check func(context.Context) error) (*Server, error) {
	key, err := devicekeys.LoadKioskCredential()
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return New(db, p, key, check)
}
