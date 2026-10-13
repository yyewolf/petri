// This module holds no Go code. It fences the documentation site off from
// the petri module: without it, petri's module zip would carry the site, and
// `go ... ./...` at the repository root would walk website/node_modules.
// The compiled samples live in their own module, website/examples.
module github.com/yyewolf/petri/website

go 1.26.0
