//go:build !linux

package pickup

import (
	"database/sql"
	"errors"
)

func NewPlatform(*sql.DB) (*Service, error) {
	return nil, errors.New("Pi pickup startup requires Linux device credentials")
}
