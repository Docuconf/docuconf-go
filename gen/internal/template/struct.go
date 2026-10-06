package template

const Struct = Header + `
import (
	docuconf "github.com/docuconf/docuconf-go"
	"os"
	"errors"
)

type {{.StructName}} struct {
  {{- range .Fields}} 

  // {{.Description}}
  // {{if .SharedWith}}also used in: {{ range .SharedWith}}{{.}}{{end}}{{end}}
  {{.Name}} {{.Type}} ` + "`env:\"" + `{{.EnvName}}` + "\"`" + `
  {{- end}}
}

func Load{{.StructName}}(path string) ({{.StructName}}, error) {
    defaultEnv := New{{.StructName}}()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return defaultEnv, nil
	}
	env, err := docuconf.LoadDotEnv(path, defaultEnv)
	return env, err
}

func New{{.StructName}}() {{.StructName}} {
	return {{.StructName}}{
	{{- range .Fields}} 
		{{if .Default}}{{.Name}}: {{.Default}},{{end}}
  	{{- end}}
	}
}

`

const MarkDownVariables = NoEditMark + `

# {{.ServiceName}} Configuration
{{range .Fields}}
---
### {{.Name}} - ` + "`{{.Type}}`" + ` - ({{if .Required}}Required{{else}}Optional{{end}})
{{if .SharedWith}}Also used in: {{ range .SharedWith}}{{.}}{{end}}
{{end}}
**Description**

{{.Description}}
{{end}}
---
`
