// Package docuconf is the Go SDK for docuconf configuration contracts.
//
// It extends caarlos0/env (https://github.com/caarlos0/env) rather than
// replacing it. A configuration struct is a normal caarlos0/env struct;
// docuconf adds what the host library lacks: descriptions, secrets,
// constraints, file inputs, boot-time checks with stable error codes, and
// export of a CUE contract the platform validates before it deploys.
//
//	type Config struct {
//		// Primary Postgres connection string.
//		DatabaseURL docuconf.Secret `env:"DATABASE_URL,required" schemes:"postgres,postgresql"`
//
//		// HTTP listen port.
//		Port int `env:"PORT" envDefault:"8080" min:"1" max:"65535"`
//
//		// Certificate the service serves HTTPS with.
//		TLS docuconf.TLSKeyPair `file:"serving-tls,required" path:"/etc/app/tls" dnsNames:"api.example.com" minRemaining:"720h" reload:"watch"`
//	}
//
//	cfg := docuconf.ParseOrExit[Config]() // prints every problem and exits 1
//
// # Variables
//
// caarlos0/env's own tags work unchanged: env (with the options required,
// file, notEmpty, expand, unset and init), envDefault, envSeparator and
// envPrefix. Every variable needs a description of at least five
// characters: the first paragraph of the field's doc comment, or a desc
// tag when there is none. Later paragraphs of the doc comment become the
// variable's details, converted to Markdown (headings, lists and code
// blocks carry over), for generated docs only: at most 4000 characters.
// File inputs follow the same rule. docuconf adds:
//
//	secret:"true"           the value comes from a Secret; never printed, no default
//	min:"1" max:"65535"     int, float and duration bounds (durations as "1s")
//	minLength maxLength     string length in characters; maxLength also
//	                        bounds a url, and a JSON[T] value as received
//	pattern:"^[a-z]+$"      string pattern, RE2, partial match like CUE =~
//	values:"debug,info"     makes a string an enum
//	schemes:"https"         makes a string a url; also on url.URL fields
//	type:"url"              a url with any scheme
//	minItems maxItems       list length
//	itemMin:"0" itemMax:"9" bounds on each item of an integer list
//	itemMinLength itemMaxLength  length of each item of a string list
//	minKeys maxKeys         number of keys in a KeySet (default 1 and 2)
//	keyMinLength keyMaxLength  length of each key of a KeySet
//	deprecated:"Use PORT"   what to use instead, or why it is going away
//	replacedBy:"PORT"       the input that replaces a deprecated one
//	group, examples ("a|b"), configKey
//
// A tag key that is not one of these but is close to one (secrte, mni) is
// a declaration error, so a typo never drops a rule silently. Keys of
// common libraries (json, yaml, validate, default, ...) are left alone.
//
// # Secrets
//
// A field of type Secret is a secret variable that prints as *** under
// fmt, log/slog and encoding/json; Reveal returns its value. For other
// types, secret:"true" marks the variable secret in the contract and in
// messages, and Redacted and LogValue give the whole configuration with
// every secret replaced by ***.
//
// A field of type KeySet is a set of secret keys that are all valid at
// once, so a key can be rotated without an outage (contract type
// "keySet"): its Contains and Verify methods check a candidate against
// every key.
//
// The contract type follows from the Go type: string, bool, every int and
// uint kind, float32/64, time.Duration (encoding "go"), url.URL, slices of
// strings or integers (encoding "csv" with envSeparator), KeySet (also
// "csv"), JSON[T] for a structured value, and any encoding.TextUnmarshaler
// as a string. Nested structs are walked with their envPrefix. Integer
// bounds include the range caarlos0/env parses the kind with (int is
// parsed as 32 bits), for scalars as min and max and for list items as
// itemMin and itemMax: a []uint16 exports itemMin 0 and itemMax 65535
// without any tag.
//
// # Deprecated inputs
//
// A deprecated tag marks a variable or file input for removal: the
// platform should stop setting it. Parse logs a warning naming the input
// and the message, never the value, when one is set, and docuconf vet
// warns about it too. A required input cannot be deprecated.
//
// # File inputs
//
// A field of a docuconf file type is a file input. Its file tag names the
// input (a DNS label), optionally followed by ",required":
//
//	TLSKeyPair     type tls: a directory with tls.crt, tls.key, ca.crt
//	CABundle       type caBundle: PEM CA certificates
//	Keystore       type keystore: PKCS#12, password from a secret variable
//	TextFile       type text
//	BinaryFile     type binary
//	ConfigFile[T]  type config: JSON or YAML bound to T
//
// Tags for every file type: path (required; absolute), pathEnv (a variable
// the platform sets to the path; it overrides path at runtime), reload
// ("restart" or "watch"), maxSize (bytes, or with a Ki, Mi or Gi suffix),
// secret, desc, group, deprecated, replacedBy. Per type:
//
//	TLSKeyPair  dnsNames:"a,b" keyAlgorithms:"ECDSA,RSA" minRemaining:"720h" requireCA:"true"
//	CABundle    minCertificates:"2"
//	Keystore    passwordVar:"KEYSTORE_PASSWORD" format:"pkcs12"
//	TextFile    pattern, minLength, maxLength
//	ConfigFile  format:"json", "yaml" or "toml" (default from the path's extension)
//
// A config file's contract carries a JSON Schema generated from T: json
// tags name the properties, fields without omitempty (or omitzero) and
// not pointers are required, and other properties are rejected. T's fields
// take the constraint tags above, which become schema keywords, and T may
// implement Validate() error.
//
// # Boot checks
//
// Parse runs caarlos0/env, then checks every variable and file and
// returns all violations together in a *ValidationError. Each has a
// stable Code (missing_required, out_of_range, certificate_expiring, ...).
// Violations never include a secret's value. They are also written to
// /dev/termination-log when it exists, or to DOCUCONF_TERMINATION_LOG.
// An empty value counts as unset for every type except string.
// DOCUCONF_FILE_ROOT remaps file paths for local development, and .env
// files are read only when listed in Options.DotEnv.
//
// # Contract-first
//
// LoadContract validates an environment against a contract given as JSON,
// with no Go declaration, and returns typed values and file inputs. It
// parses every wire encoding of SPEC §5, and runs the same checks as Parse. The shared
// conformance suite runs through it.
//
// # Options
//
// A configuration struct parsed with a Prefix or a FuncMap should return
// them from a DocuconfOptions method (see OptionsProvider). Parse, Export
// and the export commands all use it, so the app and its contract agree.
//
// # Testing
//
// ParseWithOptions with Options.Environment loads from a map, without
// reading or changing the process environment; set TerminationLog to "-"
// in tests. FileRoot and Now control file inputs and the clock.
//
// # Export
//
// Export renders the declaration as a contract.cue for the platform. The
// docuconf-export command (cmd/docuconf-export, in this module) and
// "docuconf export" (cmd/docuconf) wrap it: they compile and run a small
// program inside the app's module with go run, so export needs the Go
// toolchain and the module's dependencies, but no environment values.
// The docuconf command adds "docuconf vet" and "docuconf render" for the
// platform side.
package docuconf
