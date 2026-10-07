// Package prefixed shows a configuration struct that owns its parse
// options. Its variables are ORDERS_PORT and ORDERS_UPSTREAM, and a
// FuncMap entry parses Upstream. Parse and docuconf export both call
// DocuconfOptions, so they always agree.
package prefixed

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/docuconf/docuconf-go"
)

// Config is the configuration of a service whose variables carry a prefix.
type Config struct {
	// HTTP listen port.
	Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`

	// Upstream service address, as host:port.
	Upstream HostPort `env:"UPSTREAM,required"`
}

func (Config) DocuconfOptions() docuconf.Options {
	return docuconf.Options{Prefix: "ORDERS_", FuncMap: parsers}
}

// HostPort is a network address.
type HostPort struct{ Host, Port string }

var parsers = map[reflect.Type]env.ParserFunc{
	reflect.TypeOf(HostPort{}): func(v string) (any, error) {
		host, port, ok := strings.Cut(v, ":")
		if !ok {
			return nil, fmt.Errorf("%q is not host:port", v)
		}
		return HostPort{host, port}, nil
	},
}
