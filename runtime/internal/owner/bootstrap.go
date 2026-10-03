package owner

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/localfiles"
)

const BootstrapTokenFile = "owner-setup-token.txt"

// The installer administrator reads this protected file locally. It is never
// served by HTTP, logged, passed in URLs or included in a support response.
func (s *Service) EnsureBootstrapToken(ctx context.Context, files *localfiles.Files) (string, error) {
	exists, err := s.Exists(ctx)
	if err != nil {
		return "", err
	}
	if exists {
		return "", nil
	}
	file, err := files.Open(BootstrapTokenFile)
	if err == nil {
		defer file.Close()
		contents, err := io.ReadAll(io.LimitReader(file, 66))
		if err != nil {
			return "", err
		}
		token := strings.TrimSpace(string(contents))
		raw, err := hex.DecodeString(token)
		if err != nil || len(raw) != 32 {
			return "", fmt.Errorf("invalid local setup token file")
		}
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	token, err := RandomToken()
	if err != nil {
		return "", err
	}
	if err := files.WriteAtomic(BootstrapTokenFile, strings.NewReader(token)); err != nil {
		return "", err
	}
	return token, nil
}
