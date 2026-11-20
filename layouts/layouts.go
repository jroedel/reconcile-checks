// Package layouts holds the stock layouts, as data, embedded in the program
// (docs/04-stock-and-layout.md, "What is printed, and where"). A new stock is
// a new file here, not new code. business/domain/layout/layoutbus reads them.
package layouts

import "embed"

// FS is every layout, by file name: <name>.toml.
//
//go:embed *.toml
var FS embed.FS
