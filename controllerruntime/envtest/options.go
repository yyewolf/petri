package envtest

import (
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// CRDInstallOptions mirrors the envtest struct for the fields that make sense
// here. Paths and CRDs are merged with the Environment's CRDDirectoryPaths and
// CRDs; the wait behavior petri uses is fixed, so MaxTime and PollInterval
// have no place here.
type CRDInstallOptions struct {
	// Paths lists files and directories of CRD manifests to install.
	Paths []string
	// CRDs are CRD objects to install.
	CRDs []*apiextensionsv1.CustomResourceDefinition
	// CleanUpAfterUse cannot be honored: petri never deletes CRDs, because
	// other tests on a shared control plane may still use them. Setting it
	// fails Start instead of silently leaking CRDs the suite expects to be
	// gone.
	CleanUpAfterUse bool
}

// WebhookInstallOptions mirrors the envtest struct. Start installs the
// configurations, points them at the local serving address, and fills in
// LocalServingHost, LocalServingPort and LocalServingCertDir for the suite's
// webhook server, exactly as envtest does.
type WebhookInstallOptions struct {
	// Paths lists files and directories of webhook configuration manifests.
	Paths []string
	// MutatingWebhooks and ValidatingWebhooks are configurations to install in
	// addition to the manifests.
	MutatingWebhooks   []*admissionregistrationv1.MutatingWebhookConfiguration
	ValidatingWebhooks []*admissionregistrationv1.ValidatingWebhookConfiguration
	// LocalServingHost is the host of the suite's webhook server; Start
	// defaults it to 127.0.0.1.
	LocalServingHost string
	// LocalServingPort is the port of the suite's webhook server; Start
	// allocates a free one when it is zero.
	LocalServingPort int
	// LocalServingCertDir is a directory with tls.crt and tls.key for the
	// server, signed by the control plane's CA. Start writes a fresh pair
	// into a temporary directory when it is empty; the directory is removed
	// by Stop.
	LocalServingCertDir string
}
