// Package metaschema embeds the docuconf contract meta-schema (the CUE
// module docuconf.dev, package docuconf.dev/contract) and the docs model
// schema (package docuconf.dev/docs).
//
// The files are copies of spec/cue/cue.mod, spec/cue/contract and
// spec/cue/docs, which
// stay the single source of truth: run go generate after changing the
// spec, and TestInSyncWithSpec fails until the copies match.
package metaschema

//go:generate sh -c "cp ../../../../spec/cue/contract/*.cue contract/ && mkdir -p docs && cp ../../../../spec/cue/docs/*.cue docs/ && cp ../../../../spec/cue/cue.mod/module.cue cue.mod/"

import (
	"embed"
	"io/fs"
)

//go:embed cue.mod/module.cue contract/*.cue docs/*.cue
var files embed.FS

// FS returns the meta-schema module: cue.mod/module.cue, contract/*.cue
// and docs/*.cue.
func FS() fs.FS { return files }
