package docs

import "embed"

//go:embed README.md user/*.md
var Docs embed.FS
