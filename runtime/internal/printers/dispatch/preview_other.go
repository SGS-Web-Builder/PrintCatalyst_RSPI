//go:build !windows && !linux

package dispatch

import (
	"context"
	"errors"
)

func RenderPreview(context.Context, []byte, int) ([]byte, error) {
	return nil, errors.New("PDF preview requires the Windows renderer")
}
