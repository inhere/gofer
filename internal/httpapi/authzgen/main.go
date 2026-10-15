// Command authzgen writes the generated permission reference
// (docs/reference/permissions.md) from the running code: the permission catalog and the
// route declaration table.
//
// It is the program behind
//
//	go generate ./internal/httpapi/...
//
// and it is deliberately a thin wrapper: the document itself is rendered by
// httpapi.AuthzReference, so the checked-in file and the staleness test
// (TestAuthzReferenceIsUpToDate) are produced by exactly the same code.
//
// The working directory is the package directory (that is what `go generate` sets), so
// the default -out is relative to internal/httpapi.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inhere/gofer/internal/httpapi"
)

func main() {
	out := flag.String("out", httpapi.AuthzReferencePath,
		"where to write the document, relative to internal/httpapi")
	flag.Parse()

	path := filepath.Clean(*out)
	if err := os.WriteFile(path, []byte(httpapi.AuthzReference()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "authzgen:", err)
		os.Exit(1)
	}
	fmt.Println("authzgen: wrote", path)
}
