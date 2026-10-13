#!/usr/bin/env bash
# Freezes the current docs as release line <0.N>: Docusaurus snapshots docs/
# into versioned_docs/version-<0.N>/, and the samples those pages import are
# copied to _versioned_examples/version-<0.N>/ so the frozen pages keep showing
# the code of their own line once examples/ moves on. The copies are text for
# the site only: the leading underscore keeps the go command away from them.
#
# Run from website/ when main moves to the next Kubernetes minor, with the
# outgoing line: scripts/version-docs.sh 0.37
# Environment: NPX overrides the npx command (tests stub it).
set -euo pipefail

version="${1:-}"
npx_cmd=${NPX:-npx}

if [[ ! "$version" =~ ^0\.[0-9]+$ ]]; then
	echo "usage: version-docs.sh <0.N>   e.g. 0.37 for Kubernetes 1.37" >&2
	exit 1
fi

frozen_docs="versioned_docs/version-$version"
frozen_examples="_versioned_examples/version-$version"

if [[ -e "$frozen_docs" || -e "$frozen_examples" ]]; then
	echo "error: docs for $version are already frozen" >&2
	exit 1
fi

"$npx_cmd" docusaurus docs:version "$version"

# Copy the samples without their module files: a frozen copy is not a module.
mkdir -p "$frozen_examples"
(cd examples && find . -type f ! -name go.mod ! -name go.sum -print0) |
	while IFS= read -r -d '' file; do
		mkdir -p "$frozen_examples/$(dirname "$file")"
		cp "examples/$file" "$frozen_examples/$file"
	done

# Point the frozen pages at the frozen samples.
grep -rlF '@site/examples/' "$frozen_docs" | while IFS= read -r page; do
	sed -i.bak "s|@site/examples/|@site/$frozen_examples/|g" "$page"
	rm -f "$page.bak"
done

# Any other route to the live samples would show the next line's code.
if leftovers=$(grep -rnE "['\"][^'\"]*(^|/)examples/" "$frozen_docs" | grep -v "_versioned_examples/"); then
	echo "error: frozen pages still import live samples:" >&2
	echo "$leftovers" >&2
	exit 1
fi

echo "froze docs $version: $frozen_docs, samples in $frozen_examples"
