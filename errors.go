package docuconf

import (
	"fmt"
	"strings"
)

// Code is a stable, machine-readable violation code (SPEC §11.2 item 5).
// Codes are shared by every docuconf SDK, so tooling and alerts can match
// on them across languages.
type Code string

// Violation codes.
const (
	CodeMissingRequired         Code = "missing_required"
	CodeInvalidType             Code = "invalid_type"
	CodeOutOfRange              Code = "out_of_range"
	CodePatternMismatch         Code = "pattern_mismatch"
	CodeNotInEnum               Code = "not_in_enum"
	CodeInvalidScheme           Code = "invalid_scheme"
	CodeTooFewItems             Code = "too_few_items"
	CodeTooManyItems            Code = "too_many_items"
	CodeFileMissing             Code = "file_missing"
	CodeFileUnreadable          Code = "file_unreadable"
	CodeFileTooLarge            Code = "file_too_large"
	CodeFileMalformed           Code = "file_malformed"
	CodeSchemaMismatch          Code = "schema_mismatch"
	CodeCertificateInvalid      Code = "certificate_invalid"
	CodeCertificateExpiring     Code = "certificate_expiring"
	CodeCertificateNameMismatch Code = "certificate_name_mismatch"
	CodeKeyMismatch             Code = "key_mismatch"
	CodeKeystoreUnreadable      Code = "keystore_unreadable"
)

// Violation is one problem found while loading configuration at boot.
//
// Message never contains the value of a secret variable or the contents
// of a secret file.
type Violation struct {
	// Input is the environment variable name, or the file input name.
	Input string
	// Code is the stable violation code.
	Code Code
	// Message is a human-readable explanation, without the input name.
	Message string
}

// String formats the violation as "INPUT: message (code)".
func (v Violation) String() string {
	return fmt.Sprintf("%s: %s (%s)", v.Input, v.Message, v.Code)
}

// ValidationError reports every violation found at boot, together.
// Parse returns it when the environment or files do not satisfy the
// declaration.
type ValidationError struct {
	Violations []Violation
}

func (e *ValidationError) Error() string {
	var b strings.Builder
	n := len(e.Violations)
	if n == 1 {
		b.WriteString("docuconf: 1 configuration problem:")
	} else {
		fmt.Fprintf(&b, "docuconf: %d configuration problems:", n)
	}
	for _, v := range e.Violations {
		b.WriteString("\n  ")
		b.WriteString(v.String())
	}
	return b.String()
}

// Has reports whether any violation has the given code.
func (e *ValidationError) Has(code Code) bool {
	for _, v := range e.Violations {
		if v.Code == code {
			return true
		}
	}
	return false
}

// DeclarationError reports mistakes in the configuration struct itself:
// an invalid variable name, an unsupported type, a default that breaks
// its own constraints, a non-RE2 pattern. These are programming errors,
// found before any value is read.
type DeclarationError struct {
	Problems []string
}

func (e *DeclarationError) Error() string {
	if len(e.Problems) == 1 {
		return "docuconf: invalid declaration: " + e.Problems[0]
	}
	return "docuconf: invalid declaration:\n  " + strings.Join(e.Problems, "\n  ")
}
