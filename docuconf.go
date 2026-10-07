package docuconf

import (
	"fmt"

	"github.com/caarlos0/env/v11"

	"github.com/docuconf/docuconf-go/internal/dotenv"
)

// LoadDotEnv reads the .env file at filePath into envStruct, whose fields
// carry env tags, and returns the result. Fields the file does not set
// keep their values. Only the file is read, not the process environment.
//
// Deprecated: use ParseWithOptions with Options.DotEnv, which also reads
// the process environment (it wins over the file), checks docuconf
// constraints and loads file inputs. LoadDotEnv remains for code generated
// by the deprecated gen package.
func LoadDotEnv[T any](filePath string, envStruct T) (T, error) {
	vals, err := dotenv.Read(filePath)
	if err != nil {
		return envStruct, fmt.Errorf("docuconf: loading %s: %w", filePath, err)
	}
	if err := env.ParseWithOptions(&envStruct, env.Options{Environment: vals}); err != nil {
		return envStruct, fmt.Errorf("docuconf: loading %s: %w", filePath, err)
	}
	return envStruct, nil
}
