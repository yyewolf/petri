package petri

import (
	"errors"
	"fmt"
	"io/fs"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

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
		return crdsFromManifests("fs.FS", func() ([]manifest, error) { return manifestDataFS(s) })
	case string:
		return crdsFromManifests(s, func() ([]manifest, error) { return manifestData(s) })
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
		found, err := crdsFromManifests(path, func() ([]manifest, error) { return manifestData(path) })
		if err != nil {
			return nil, err
		}

		crds = append(crds, found...)
	}

	return crds, nil
}

// crdsFromManifests reads a source's files and decodes their CRD documents.
func crdsFromManifests(
	source string, read func() ([]manifest, error),
) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	files, err := read()
	if err != nil {
		return nil, err
	}

	var crds []*apiextensionsv1.CustomResourceDefinition

	for _, file := range files {
		documents, docErr := decodeDocuments(file.data)
		if docErr != nil {
			return nil, fmt.Errorf("petri: %s: %w", file.name, docErr)
		}

		for _, document := range documents {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			ok, decodeErr := decodeKind(document, "CustomResourceDefinition", crd)
			if decodeErr != nil {
				return nil, fmt.Errorf("petri: %s: %w", file.name, decodeErr)
			}
			if !ok {
				continue
			}

			// The API server requires metadata.name to be plural.group but
			// does not default it; most manifests rely on it being written
			// out.
			if crd.Name == "" {
				crd.Name = crd.Spec.Names.Plural + "." + crd.Spec.Group
			}

			crds = append(crds, crd)
		}
	}

	if len(crds) == 0 {
		return nil, fmt.Errorf("petri: source %q contains no CustomResourceDefinition manifests", source)
	}

	return crds, nil
}
