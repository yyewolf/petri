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

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// schemeGroupVersions maps the manifest kinds the helpers install to the API
// versions they are served under.
var schemeGroupVersions = map[string]string{ //nolint:gochecknoglobals // A fixed set, not configuration.
	"CustomResourceDefinition":       apiextensionsv1.SchemeGroupVersion.String(),
	"MutatingWebhookConfiguration":   admissionregistrationv1.SchemeGroupVersion.String(),
	"ValidatingWebhookConfiguration": admissionregistrationv1.SchemeGroupVersion.String(),
}

// manifestExtensions are the manifest extensions envtest also accepts.
var manifestExtensions = map[string]bool{ //nolint:gochecknoglobals // A fixed set, not configuration.
	".yaml": true, ".yml": true, ".json": true,
}

// manifestReadChunk is the buffer size of the YAML/JSON document decoder,
// matching what kubectl uses for test-sized manifests.
const manifestReadChunk = 4096

// manifest is one manifest file read from a source.
type manifest struct {
	name string
	data []byte
}

// manifestData returns every manifest file's contents at a file or directory
// path. A path that does not exist fails, and so does a directory with no
// manifest files: a test that intended to install manifests and installed none
// is a bug worth reporting.
func manifestData(path string) ([]manifest, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("petri: source %q: %w", path, err)
	}

	if !info.IsDir() {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("petri: read %s: %w", path, readErr)
		}

		return []manifest{{name: path, data: data}}, nil
	}

	// Walking through os.DirFS keeps the read rooted at the source directory.
	files, err := manifestDataFS(os.DirFS(path))
	if err != nil {
		return nil, fmt.Errorf("petri: source %q: %w", path, err)
	}

	return files, nil
}

// manifestDataFS does the same for an [fs.FS], so manifests can be embedded.
func manifestDataFS(fsys fs.FS) ([]manifest, error) {
	var files []manifest

	walkErr := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && manifestExtensions[filepath.Ext(p)] {
			data, readErr := fs.ReadFile(fsys, p)
			if readErr != nil {
				return fmt.Errorf("read %s: %w", p, readErr)
			}

			files = append(files, manifest{name: p, data: data})
		}

		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("petri: source fs.FS: %w", walkErr)
	}

	if len(files) == 0 {
		return nil, errors.New("petri: source fs.FS contains no .yaml, .yml or .json files")
	}

	return files, nil
}

// decodeDocuments splits a YAML or JSON manifest into its documents, skipping
// empty ones. Multi-document files are the norm for test manifests.
func decodeDocuments(data []byte) ([]map[string]any, error) {
	var documents []map[string]any

	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), manifestReadChunk)
	for {
		var document map[string]any
		if err := decoder.Decode(&document); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, fmt.Errorf("decode manifest: %w", err)
		}

		if len(document) > 0 {
			documents = append(documents, document)
		}
	}

	return documents, nil
}

// decodeKind turns one manifest document into out when it is of the wanted
// kind, and reports whether it was. Documents of other kinds are skipped, as
// envtest does with non-CRD documents.
func decodeKind(document map[string]any, kind string, out any) (bool, error) {
	apiVersion, _ := document["apiVersion"].(string)
	got, _ := document["kind"].(string)
	if apiVersion != schemeGroupVersions[kind] || got != kind {
		return false, nil
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		return false, fmt.Errorf("re-encode manifest: %w", err)
	}

	if unmarshalErr := json.Unmarshal(encoded, out); unmarshalErr != nil {
		return false, fmt.Errorf("decode %s: %w", kind, unmarshalErr)
	}

	return true, nil
}
