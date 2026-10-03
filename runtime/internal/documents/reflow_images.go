package documents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

type imageRange struct {
	Offset int
	Length int
}
type imageRecipe struct {
	Paper       string
	Orientation string
	PerPage     int
	Split       int
	Images      []imageRange
}

// ReflowImages rebuilds only our tagged compositions from their embedded images.
// Ordinary PDFs retain their existing page layout. Original photo proportions stay fixed.
func ReflowImages(body []byte, paper, orientation string) ([]byte, error) {
	marker := []byte("%PrintCatalystImages ")
	start := bytes.LastIndex(body, marker)
	if start < 0 {
		return body, nil
	}
	metadata := body[start+len(marker):]
	end := bytes.IndexByte(metadata, '\n')
	if end < 0 || end > 4096 {
		return nil, fmt.Errorf("invalid image layout metadata")
	}
	var recipe imageRecipe
	if err := json.Unmarshal(metadata[:end], &recipe); err != nil {
		return nil, fmt.Errorf("invalid image layout metadata")
	}
	if len(recipe.Images) < 2 || len(recipe.Images) > 10 {
		return nil, fmt.Errorf("invalid image layout sources")
	}
	if paper == "" {
		paper = recipe.Paper
	}
	if orientation == "auto" || orientation == "" {
		orientation = recipe.Orientation
	}
	if strings.EqualFold(paper, recipe.Paper) && orientation == recipe.Orientation {
		return body, nil
	}
	sources := make([][]byte, 0, len(recipe.Images))
	for _, source := range recipe.Images {
		if source.Offset < 0 || source.Length < 1 || source.Offset > start || source.Length > start-source.Offset {
			return nil, fmt.Errorf("invalid image layout source")
		}
		sources = append(sources, body[source.Offset:source.Offset+source.Length])
	}
	return ComposeImages(sources, recipe.PerPage, paper, orientation, recipe.Split)
}
