package apicontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type Descriptor struct {
	ID          string `json:"operationId"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	ContentType string `json:"contentType,omitempty"`
	Transport   string `json:"transport"`
}

// Manifest is a small generated HTTP mapping for browser/SDK clients; schemas
// remain in OpenAPI. AI and MCP use the same operations without a copied map.
func Manifest() ([]byte, error) {
	c, err := Default()
	if err != nil {
		return nil, err
	}
	items := make([]Descriptor, 0, len(c.ordered))
	for _, op := range c.ordered {
		items = append(items, Descriptor{op.ID, op.Method, op.Path, op.ContentType(), op.Transport})
	}
	mapping, _ := json.Marshal(items)
	sum := sha256.Sum256(mapping)
	data, err := json.MarshalIndent(struct {
		Digest     string       `json:"operationsSHA256"`
		Operations []Descriptor `json:"operations"`
	}{hex.EncodeToString(sum[:]), items}, "", "  ")
	return append(data, '\n'), err
}
