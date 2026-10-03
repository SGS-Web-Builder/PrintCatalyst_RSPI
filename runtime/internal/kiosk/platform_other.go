//go:build !linux

package kiosk

import (
	"context"
	"database/sql"
	"errors"
)

func NewPlatform(*sql.DB, Claimer, func(context.Context) error) (*Server, error) {
	return nil, errors.New("physical kiosk startup requires Linux credentials")
}
