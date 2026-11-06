// Command fakeazure serves the little of Azure Resource Manager that the
// azurerm provider calls before it can plan: the cloud's metadata, a token,
// and the subscription's resource providers. CI plans the containerapps
// root against it, with ARM_METADATA_HOSTNAME set to the host it prints and
// SSL_CERT_FILE to the certificate it writes. Nothing it returns is real,
// and it accepts any credentials.
package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
)

func main() {
	ca := flag.String("ca", "fakeazure-ca.pem", "where to write the server's certificate")
	flag.Parse()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/metadata/endpoints", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":                     "AzureCloud",
			"resourceManager":          srv.URL + "/",
			"microsoftGraphResourceId": srv.URL + "/",
			"authentication": map[string]any{
				"loginEndpoint":    srv.URL + "/",
				"audiences":        []string{srv.URL + "/"},
				"tenant":           "common",
				"identityProvider": "AAD",
			},
			"suffixes": map[string]any{"keyVaultDns": "vault.azure.net", "storage": "core.windows.net"},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/providers") {
			w.Write([]byte(`{"value": []}`))
			return
		}
		// A token: the provider reads its claims, and nothing checks it.
		part := func(m map[string]any) string { b, _ := json.Marshal(m); return base64.RawURLEncoding.EncodeToString(b) }
		tok := part(map[string]any{"alg": "none", "typ": "JWT"}) + "." + part(map[string]any{"oid": "00000000-0000-0000-0000-000000000001", "tid": "00000000-0000-0000-0000-000000000000", "exp": 4102444800}) + ".x"
		json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600})
	})
	srv = httptest.NewTLSServer(mux)
	if err := os.WriteFile(*ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println(strings.TrimPrefix(srv.URL, "https://"))
	select {}
}
