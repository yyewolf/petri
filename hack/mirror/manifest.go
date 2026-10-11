package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// manifest is internal/kubernetes/MIRROR.json: the tool's input (roots) and its
// record of what was mirrored (kubernetes).
type manifest struct {
	Kubernetes string   `json:"kubernetes"`
	Roots      []string `json:"roots"`
}

func readManifest(path string) (manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, fmt.Errorf("read manifest: %w", err)
	}

	var m manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return manifest{}, fmt.Errorf("parse manifest %s: %w", path, err)
	}

	if len(m.Roots) == 0 {
		return manifest{}, errors.New("manifest " + path + " lists no roots")
	}

	return m, nil
}

func (m manifest) write(path string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}

	if err = os.WriteFile(
		path,
		append(raw, '\n'),
		0o644,
	); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	return nil
}
