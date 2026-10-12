// Command mirror regenerates internal/kubernetes from upstream k8s.io/kubernetes
// and internal/kine from github.com/k3s-io/kine, and plans automated mirror
// updates.
package main

import (
	"bytes"
	"fmt"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// rewriteImports returns src with every import path equal to oldPrefix, or
// below it, moved under newPrefix. Only the import path literals change; every
// other byte of src is preserved.
func rewriteImports(filename string, src []byte, oldPrefix, newPrefix string) ([]byte, error) {
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, filename, src, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	var out bytes.Buffer

	last := 0

	for _, spec := range file.Imports {
		path, unquoteErr := strconv.Unquote(spec.Path.Value)
		if unquoteErr != nil {
			return nil, fmt.Errorf("unquote import %s in %s: %w", spec.Path.Value, filename, unquoteErr)
		}

		if path != oldPrefix && !strings.HasPrefix(path, oldPrefix+"/") {
			continue
		}

		start := fset.Position(spec.Path.Pos()).Offset
		end := fset.Position(spec.Path.End()).Offset

		out.Write(src[last:start])
		out.WriteString(strconv.Quote(newPrefix + strings.TrimPrefix(path, oldPrefix)))

		last = end
	}

	out.Write(src[last:])

	return out.Bytes(), nil
}
