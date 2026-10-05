//go:build linux

package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/SGS-Web-Builder/PrintCatalyst_RSPI/runtime/internal/prepared"
	"io"
	"os/exec"
	"time"
)

func RenderPreview(ctx context.Context, body []byte, page int) ([]byte, error) {
	select {
	case rendererSlots <- struct{}{}:
		defer func() { <-rendererSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if len(body) == 0 || len(body) > prepared.MaxPDFBytes || page < 1 {
		return nil, errors.New("invalid preview input")
	}
	header, _ := json.Marshal(map[string]any{"preview": true, "page": page})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, NewPDFRenderer().python, "-I", "-c", rendererProgram)
	command.Stdin = io.MultiReader(bytes.NewReader(append(header, '\n')), bytes.NewReader(body))
	out := &cappedBuffer{limit: 8 << 20}
	command.Stdout = out
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, errors.New("local preview renderer unavailable or document invalid")
	}
	index := bytes.IndexByte(out.Bytes(), '\n')
	if index < 0 || index > 4096 {
		return nil, errors.New("invalid preview response")
	}
	png := out.Bytes()[index+1:]
	if !bytes.HasPrefix(png, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return nil, errors.New("invalid preview image")
	}
	return png, nil
}
