// Package upstream is an HTTP client that trusts the CAs in a watched CA
// bundle, rebuilt when the bundle changes.
package upstream

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"sync/atomic"

	"github.com/docuconf/docuconf-go"
)

// Client is an HTTP client for an upstream whose certificate is signed by
// a private CA. A client copies its CA pool when it is built, so Client
// builds a new one each time the bundle changes.
type Client struct {
	cur    atomic.Pointer[http.Client]
	cancel func()
}

// New returns a client that trusts the bundle's current certificates.
func New(ca docuconf.CABundle) *Client {
	c := &Client{}
	build := func(certs []*x509.Certificate) {
		pool := x509.NewCertPool()
		for _, cert := range certs {
			pool.AddCert(cert)
		}
		next := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
		if old := c.cur.Swap(next); old != nil {
			old.CloseIdleConnections()
		}
	}
	build(ca.Certificates())
	c.cancel = ca.OnChange(build)
	return c
}

// Close stops following the bundle.
func (c *Client) Close() { c.cancel() }

// Get issues a GET with the current client.
func (c *Client) Get(url string) (*http.Response, error) { return c.cur.Load().Get(url) }
