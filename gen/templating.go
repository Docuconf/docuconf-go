package gen

import (
	"bytes"
	"fmt"
	"github.com/docuconf/docuconf-go/gen/internal"
	tmpl "github.com/docuconf/docuconf-go/gen/internal/template"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

func convertConfOptionsToMeta(s *Service) []internal.ConfOptionMeta {
	var result []internal.ConfOptionMeta
	for _, o := range s.builder.options {
		var defaultValueFormatted = ""
		switch o.Type {
		case "string":
			defaultValueFormatted = fmt.Sprintf("\"%s\"", o.Default)
		default:
			defaultValueFormatted = o.Default
		}
		result = append(result, internal.ConfOptionMeta{
			Name:        o.Name,
			EnvName:     toCapSnakeCase(o.Name),
			Type:        o.Type,
			Description: o.Description,
			Required:    o.Required,
			Default:     defaultValueFormatted,
			SharedWith:  convertServicesToStrings(o.Services, s),
		})
	}
	return result
}

func convertServicesToStrings(services []*Service, currentService *Service) []string {
	var result []string
	for _, service := range services {
		if service != currentService {
			result = append(result, service.name+", ")
		}
	}
	if len(result) > 0 {
		result[len(result)-1] = strings.TrimRight(result[len(result)-1], ", ")
	}
	return result
}

var matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
var matchAllCap = regexp.MustCompile("([a-z0-9])([A-Z])")

func toCapSnakeCase(str string) string {
	snake := matchFirstCap.ReplaceAllString(str, "${1}_${2}")
	snake = matchAllCap.ReplaceAllString(snake, "${1}_${2}")
	return strings.ToUpper(snake)
}

func (s *Service) execute() (*internal.ExecutionResult, error) {
	if err := os.MkdirAll(s.path, os.ModePerm); err != nil {
		return nil, fmt.Errorf("create model pkg path(%s) fail: %s", s.path, err)
	}
	var goCodeBuf bytes.Buffer
	meta := internal.ConfStructMeta{
		Package:     s.packageName,
		StructName:  "Configuration",
		ServiceName: s.name,
		Fields:      convertConfOptionsToMeta(s),
	}
	if err := render(tmpl.Struct, &goCodeBuf, meta); err != nil {
		//goland:noinspection GoPrintFunctions
		return nil, fmt.Errorf("failed to render template: %s", err)
	}

	var markdownBuf bytes.Buffer

	if err := render(tmpl.MarkDownVariables, &markdownBuf, meta); err != nil {
		return nil, fmt.Errorf("failed to render template: %s", err)
	}
	result := &internal.ExecutionResult{
		GoFiles: []internal.OutputFile{
			{
				Path:    filepath.Join(s.path, "configuration.go"),
				Content: goCodeBuf.String(),
			},
		},
		MarkDownFiles: []internal.OutputFile{
			{
				Path:    filepath.Join(s.readmePath, "CONFIG_README.md"),
				Content: markdownBuf.String(),
			},
		},
	}
	return result, nil
}

func render(tmpl string, wr io.Writer, data interface{}) error {
	t, err := template.New(tmpl).Parse(tmpl)
	if err != nil {
		return err
	}
	return t.Execute(wr, data)
}
