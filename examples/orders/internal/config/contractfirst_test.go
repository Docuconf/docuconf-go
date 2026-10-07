package config_test

import (
	"testing"
	"time"

	"github.com/docuconf/docuconf-go"
)

var contractJSON = []byte(`{
	"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
	"metadata": {"name": "orders-api"},
	"vars": {"REQUEST_TIMEOUT": {"type": "duration", "description": "Time limit for one request", "encoding": "iso8601", "default": "30s"}}
}`)

// requestTimeout is the README's contract-first snippet.
func requestTimeout() (time.Duration, error) {
	vals, err := docuconf.LoadContract(contractJSON, docuconf.Options{})
	if err != nil {
		return 0, err // a *docuconf.ValidationError with every violation
	}
	timeout := vals["REQUEST_TIMEOUT"].(time.Duration) // int is int64, list is []string or []int64
	return timeout, nil
}

// TestContractFirst loads variables from a contract with no Go struct.
func TestContractFirst(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT", "PT1M30S")
	timeout, err := requestTimeout()
	if err != nil || timeout != 90*time.Second {
		t.Fatalf("got %v, %v", timeout, err)
	}
}
