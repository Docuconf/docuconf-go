package gen

import (
	"fmt"
	"strconv"
)

type confOption struct {
	Name        string
	Type        string
	Description string
	Required    bool
	Default     string
	Builder     *ConfBuild
	Services    []*Service
}

type Service struct {
	name        string
	packageName string
	path        string
	readmePath  string
	builder     *ConfBuild
}

type ConfBuilder interface {
	AddString(name string, description string, required bool, defaultValue string) ConfBuilder
	AddInt(name string, description string, required bool, defaultValue int) ConfBuilder
	AddBool(name string, description string, required bool, defaultValue bool) ConfBuilder
	AddFloat(name string, description string, required bool, defaultValue string) ConfBuilder
	Options() []*confOption
}

type ConfBuild struct {
	// a map of all the configuration options
	options []*confOption
}

type AdditionalOptions struct {
	ReadmePath string
}

// NewService starts a generated configuration for one service.
//
// Deprecated: see the package documentation.
func NewService(name string, packageStr string, outputPath string, options AdditionalOptions) *Service {
	readmePath := outputPath
	if len(options.ReadmePath) > 0 {
		readmePath = options.ReadmePath
	}
	return &Service{name: name, packageName: packageStr, path: outputPath, builder: NewConfBuilder(), readmePath: readmePath}
}

func (s *Service) AddShared(builder ConfBuilder) *Service {
	for _, option := range builder.Options() {
		option.Services = append(option.Services, s)
	}
	s.builder.options = append(s.builder.options, builder.Options()...)
	return s
}

func assertConfBuilderImplementsConfBuilder(c *ConfBuild) { assertConfBuilder(c) }

//goland:noinspection GoUnusedParameter
func assertConfBuilder(c ConfBuilder) {}

// NewConfBuilder starts a set of options that services can share.
//
// Deprecated: see the package documentation.
func NewConfBuilder() *ConfBuild {
	c := &ConfBuild{options: []*confOption{}}
	assertConfBuilderImplementsConfBuilder(c)
	return c
}

func (c *ConfBuild) Options() []*confOption {
	return c.options
}

func (s *Service) Options() []*confOption {
	return s.builder.Options()
}

func (c *ConfBuild) AddString(name string, description string, required bool, defaultValue string) ConfBuilder {
	if len(description) < 5 {
		panic(fmt.Errorf("AddString(%s, %s) Failed: description must be at least 5 characters long. This is to ensure your configuration is well documented", name, description))
	}
	if len(name) == 0 {
		panic(fmt.Errorf("AddString(%s, %s) Failed: name must be a non empty string", name, description))
	}
	c.options = append(c.options, &confOption{Name: name, Description: description, Type: "string", Required: required, Default: defaultValue, Builder: c})
	return c
}

func (s *Service) AddString(name string, description string, required bool, defaultValue string) *Service {
	s.builder.AddString(name, description, required, defaultValue)
	return s
}

func (c *ConfBuild) AddInt(name string, description string, required bool, defaultValue int) ConfBuilder {
	if len(description) < 5 {
		panic(fmt.Errorf("AddInt(%s, %s) Failed: description must be at least 5 characters long. This is to ensure your configuration is well documented", name, description))
	}
	if len(name) == 0 {
		panic(fmt.Errorf("AddInt(%s, %s) Failed: name must be a non empty string", name, description))
	}
	c.options = append(c.options, &confOption{Name: name, Description: description, Type: "int", Required: required, Default: strconv.Itoa(defaultValue), Builder: c})
	return c
}

func (s *Service) AddInt(name string, description string, required bool, defaultValue int) *Service {
	s.builder.AddInt(name, description, required, defaultValue)
	return s
}

func (c *ConfBuild) AddBool(name string, description string, required bool, defaultValue bool) ConfBuilder {
	if len(description) < 5 {
		panic(fmt.Errorf("AddBool(%s, %s) Failed: description must be at least 5 characters long. This is to ensure your configuration is well documented", name, description))
	}
	if len(name) == 0 {
		panic(fmt.Errorf("AddBool(%s, %s) Failed: name must be a non empty string", name, description))
	}
	c.options = append(c.options, &confOption{Name: name, Description: description, Type: "bool", Required: required, Default: strconv.FormatBool(defaultValue), Builder: c})
	return c
}

func (s *Service) AddBool(name string, description string, required bool, defaultValue bool) *Service {
	s.builder.AddBool(name, description, required, defaultValue)
	return s
}

func (c *ConfBuild) AddFloat(name string, description string, required bool, defaultValue string) ConfBuilder {
	if len(description) < 5 {
		panic(fmt.Errorf("AddFloat(%s, %s) Failed: description must be at least 5 characters long. This is to ensure your configuration is well documented", name, description))
	}
	if len(name) == 0 {
		panic(fmt.Errorf("AddFloat(%s, %s) Failed: name must be a non empty string", name, description))
	}

	c.options = append(c.options, &confOption{Name: name, Description: description, Type: "float64", Required: required, Default: defaultValue, Builder: c})
	return c
}

func (s *Service) AddFloat(name string, description string, required bool, defaultValue string) *Service {
	s.builder.AddFloat(name, description, required, defaultValue)
	return s
}
