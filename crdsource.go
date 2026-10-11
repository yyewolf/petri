package petri

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// crdFileExtensions are the manifest extensions envtest also accepts.
var crdFileExtensions = map[string]bool{ //nolint:gochecknoglobals // A fixed set, not configuration.
	".yaml": true, ".yml": true, ".json": true,
}

// parseCRDs collects CRD objects from every source: an [fs.FS] (embed.FS
// works), file and directory paths, and CRD objects, including slices of paths
// and of CRD objects. Documents that are not CRDs are skipped, as envtest does.
func parseCRDs(sources []any) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	var crds []*apiextensionsv1.CustomResourceDefinition

	for _, source := range sources {
		found, err := parseCRDSource(source)
		if err != nil {
			return nil, err
		}

		crds = append(crds, found...)
	}

	return crds, nil
}

// parseCRDSource collects the CRDs of one source.
func parseCRDSource(source any) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	switch s := source.(type) {
	case fs.FS:
		return crdsFromFS(s)
	case string:
		return crdsFromPath(s)
	case []string:
		return crdsFromPaths(s)
	case *apiextensionsv1.CustomResourceDefinition:
		if s == nil {
			return nil, errors.New("petri: nil CRD source")
		}

		return []*apiextensionsv1.CustomResourceDefinition{s}, nil
	case []*apiextensionsv1.CustomResourceDefinition:
		for _, crd := range s {
			if crd == nil {
				return nil, errors.New("petri: nil CRD source")
			}
		}

		return s, nil
	default:
		return nil, fmt.Errorf("petri: unsupported CRD source %T: want a path, an fs.FS, or CRD objects", source)
	}
}

// crdsFromPaths reads several file or directory paths into CRDs.
func crdsFromPaths(paths []string) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	var crds []*apiextensionsv1.CustomResourceDefinition

	for _, path := range paths {
		found, err := crdsFromPath(path)
		if err != nil {
			return nil, err
		}

		crds = append(crds, found...)
	}

	return crds, nil
}

// crdsFromPath reads one file or walks one directory. A path that does not
// exist fails, and a directory with no manifests fails too: a test that
// intended to install CRDs and installed none is a bug worth reporting.
func crdsFromPath(path string) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("petri: crd source %q: %w", path, err)
	}

	if !info.IsDir() {
		return crdsFromFile(path, os.ReadFile)
	}

	var files []string

	walkErr := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && crdFileExtensions[filepath.Ext(p)] {
			files = append(files, p)
		}

		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("petri: crd source %q: %w", path, walkErr)
	}

	crds, err := crdsFromFiles(path, files, os.ReadFile)
	if err != nil {
		return nil, err
	}

	return crds, nil
}

// crdsFromFS does the same for an [fs.FS], so manifests can be embedded.
func crdsFromFS(fsys fs.FS) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	var files []string

	walkErr := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && crdFileExtensions[filepath.Ext(p)] {
			files = append(files, p)
		}

		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("petri: crd source fs.FS: %w", walkErr)
	}

	read := func(name string) ([]byte, error) {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("petri: read %s: %w", name, err)
		}

		return data, nil
	}

	crds, err := crdsFromFiles("fs.FS", files, read)
	if err != nil {
		return nil, err
	}

	return crds, nil
}

func crdsFromFiles(
	source string, files []string, read func(string) ([]byte, error),
) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("petri: crd source %q contains no .yaml, .yml or .json files", source)
	}

	var crds []*apiextensionsv1.CustomResourceDefinition

	for _, file := range files {
		found, err := crdsFromFile(file, read)
		if err != nil {
			return nil, err
		}
		crds = append(crds, found...)
	}

	if len(crds) == 0 {
		return nil, fmt.Errorf("petri: crd source %q contains no CustomResourceDefinition manifests", source)
	}

	return crds, nil
}

func crdsFromFile(name string, read func(string) ([]byte, error)) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	data, err := read(name)
	if err != nil {
		return nil, fmt.Errorf("petri: read %s: %w", name, err)
	}

	crds, err := decodeCRDs(data)
	if err != nil {
		return nil, fmt.Errorf("petri: %s: %w", name, err)
	}

	return crds, nil
}

// manifestReadChunk is the buffer size of the YAML/JSON document decoder,
// matching what kubectl uses for test-sized manifests.
const manifestReadChunk = 4096

// decodeCRDs decodes every CRD document of a YAML or JSON manifest, skipping
// documents of other kinds. Multi-document files are the norm for test
// manifests, so a single file may carry any number of CRDs.
func decodeCRDs(data []byte) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	var crds []*apiextensionsv1.CustomResourceDefinition

	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), manifestReadChunk)
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("decode manifest: %w", err)
		}

		if len(document) == 0 {
			continue
		}

		apiVersion, _ := document["apiVersion"].(string)
		kind, _ := document["kind"].(string)
		if apiVersion != apiextensionsv1.SchemeGroupVersion.String() || kind != "CustomResourceDefinition" {
			continue
		}

		encoded, err := json.Marshal(document)
		if err != nil {
			return nil, fmt.Errorf("re-encode manifest: %w", err)
		}

		crd := &apiextensionsv1.CustomResourceDefinition{}
		if unmarshalErr := json.Unmarshal(encoded, crd); unmarshalErr != nil {
			return nil, fmt.Errorf("decode CustomResourceDefinition: %w", unmarshalErr)
		}

		// The API server requires metadata.name to be plural.group but does
		// not default it; most manifests rely on it being written out.
		if crd.Name == "" {
			crd.Name = crd.Spec.Names.Plural + "." + crd.Spec.Group
		}

		crds = append(crds, crd)
	}

	return crds, nil
}
