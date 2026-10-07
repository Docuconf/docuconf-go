package docuconf

import "testing"

// TestSplitDoc checks the rule that turns a field's doc comment into a
// description and details (SPEC §4.2).
func TestSplitDoc(t *testing.T) {
	for _, c := range []struct {
		name, doc, desc, details string
	}{
		{"empty", "", "", ""},
		{"one line", "HTTP listen port.\n", "HTTP listen port", ""},
		{"one paragraph over several lines", "Certificate to serve HTTPS with.\nWithout it, the service serves HTTP.\n",
			"Certificate to serve HTTPS with. Without it, the service serves HTTP", ""},
		{"paragraphs", "Number of workers.\n\nEach holds a database connection,\nso keep it below the pool size.\n\nRaise it when the queue grows.\n",
			"Number of workers", "Each holds a database connection, so keep it below the pool size.\n\nRaise it when the queue grows."},
		{"heading, list and code", "Request timeout.\n\n# Choosing a value\n\nMeasure first:\n  - p99 latency\n  - retries\n\nThen run:\n\n\tcurl -w '%{time_total}' $URL\n",
			"Request timeout", "# Choosing a value\n\nMeasure first:\n\n  - p99 latency\n  - retries\n\nThen run:\n\n\tcurl -w '%{time_total}' $URL"},
		{"doc link", "Upstream timeout.\n\nSee [time.ParseDuration].\n", "Upstream timeout", "See [time.ParseDuration](https://pkg.go.dev/time#ParseDuration)."},
		{"Markdown characters are escaped", "Queue depth.\n\nAlert on queue_depth > 10.\n", "Queue depth", `Alert on queue\_depth > 10.`},
		{"non-ASCII", "Grußtext für die Startseite.\n\nZeigt «ça va» und 東京.\n", "Grußtext für die Startseite", "Zeigt «ça va» und 東京."},
		{"starts with a code block: all description", "\tcode first\nthen text\n", "code first then text", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			desc, details := splitDoc(c.doc)
			if desc != c.desc || details != c.details {
				t.Errorf("splitDoc(%q)\n got desc %q, details %q\nwant desc %q, details %q", c.doc, desc, details, c.desc, c.details)
			}
		})
	}
}
