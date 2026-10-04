// Package metaschema embeds the docuconf contract meta-schema (the CUE
// module docuconf.dev, package docuconf.dev/contract).
//
// The files are copies of spec/cue/cue.mod and spec/cue/contract, which
// stay the single source of truth: run go generate after changing the
// spec, and TestInSyncWithSpec fails until the copies match.
package metaschema

//go:generate sh -c "cp ../../../../spec/cue/contract/*.cue contract/ && cp ../../../../spec/cue/cue.mod/module.cue cue.mod/"

import (
	"embed"
	"io/fs"
)

//go:embed cue.mod/module.cue contract/*.cue
var files embed.FS

// FS returns the meta-schema module: cue.mod/module.cue and contract/*.cue.
func FS() fs.FS { return files }
