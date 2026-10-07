module github.com/docuconf/docuconf-go/examples/orders

go 1.24.0

require github.com/docuconf/docuconf-go v0.0.0

require (
	github.com/caarlos0/env/v11 v11.4.1 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	software.sslmate.com/src/go-pkcs12 v0.7.3 // indirect
)

// Build against the SDK in this repository, not a published version.
replace github.com/docuconf/docuconf-go => ../..
