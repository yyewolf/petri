package petri

import (
	"errors"
	"fmt"
	"io/fs"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
)

// webhookConfigurations is the parsed content of webhook manifest sources.
type webhookConfigurations struct {
	mutating   []*admissionregistrationv1.MutatingWebhookConfiguration
	validating []*admissionregistrationv1.ValidatingWebhookConfiguration
}

func (p *webhookConfigurations) empty() bool {
	return len(p.mutating) == 0 && len(p.validating) == 0
}

// parseWebhookConfigurations collects Mutating and ValidatingWebhookConfiguration
// objects from the same kinds of sources InstallCRDs takes. Documents of other
// kinds are skipped.
func parseWebhookConfigurations(sources []any) (*webhookConfigurations, error) {
	parsed := &webhookConfigurations{}

	for _, source := range sources {
		if err := parseWebhookConfigurationSource(parsed, source); err != nil {
			return nil, err
		}
	}

	return parsed, nil
}

func parseWebhookConfigurationSource(parsed *webhookConfigurations, source any) error {
	switch s := source.(type) {
	case fs.FS:
		return webhookConfigsFromManifests(parsed, "fs.FS", func() ([]manifest, error) { return manifestDataFS(s) })
	case string:
		return webhookConfigsFromManifests(parsed, s, func() ([]manifest, error) { return manifestData(s) })
	case []string:
		for _, path := range s {
			if err := webhookConfigsFromManifests(
				parsed, path, func() ([]manifest, error) { return manifestData(path) },
			); err != nil {
				return err
			}
		}

		return nil
	case *admissionregistrationv1.MutatingWebhookConfiguration:
		if s == nil {
			return errors.New("petri: nil webhook configuration source")
		}

		parsed.mutating = append(parsed.mutating, s)

		return nil
	case []*admissionregistrationv1.MutatingWebhookConfiguration:
		for _, config := range s {
			if config == nil {
				return errors.New("petri: nil webhook configuration source")
			}

			parsed.mutating = append(parsed.mutating, config)
		}

		return nil
	case *admissionregistrationv1.ValidatingWebhookConfiguration:
		if s == nil {
			return errors.New("petri: nil webhook configuration source")
		}

		parsed.validating = append(parsed.validating, s)

		return nil
	case []*admissionregistrationv1.ValidatingWebhookConfiguration:
		for _, config := range s {
			if config == nil {
				return errors.New("petri: nil webhook configuration source")
			}

			parsed.validating = append(parsed.validating, config)
		}

		return nil
	default:
		return fmt.Errorf(
			"petri: unsupported webhook source %T: want a path, an fs.FS, or webhook configuration objects", source,
		)
	}
}

// webhookConfigsFromManifests reads a source's files and decodes their webhook
// configuration documents.
func webhookConfigsFromManifests(
	parsed *webhookConfigurations, source string, read func() ([]manifest, error),
) error {
	files, err := read()
	if err != nil {
		return err
	}

	before := len(parsed.mutating) + len(parsed.validating)

	for _, file := range files {
		documents, docErr := decodeDocuments(file.data)
		if docErr != nil {
			return fmt.Errorf("petri: %s: %w", file.name, docErr)
		}

		for _, document := range documents {
			mutating := &admissionregistrationv1.MutatingWebhookConfiguration{}
			ok, decodeErr := decodeKind(document, "MutatingWebhookConfiguration", mutating)
			if decodeErr != nil {
				return fmt.Errorf("petri: %s: %w", file.name, decodeErr)
			}
			if ok {
				parsed.mutating = append(parsed.mutating, mutating)

				continue
			}

			validating := &admissionregistrationv1.ValidatingWebhookConfiguration{}
			ok, decodeErr = decodeKind(document, "ValidatingWebhookConfiguration", validating)
			if decodeErr != nil {
				return fmt.Errorf("petri: %s: %w", file.name, decodeErr)
			}
			if ok {
				parsed.validating = append(parsed.validating, validating)
			}
		}
	}

	if before == len(parsed.mutating)+len(parsed.validating) {
		return fmt.Errorf("petri: source %q contains no webhook configuration manifests", source)
	}

	return nil
}
