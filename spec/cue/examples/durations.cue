package examples

import "docuconf.dev/contract"

// Every duration encoding, checked by unification: if a rendered value
// differs from want, cue vet fails.
durationCases: [
	{in: "90s", encoding: "go", want: "90s"},
	{in: "90s", encoding: "seconds", want: "90"},
	{in: "1500ms", encoding: "seconds", want: "1.5"},
	{in: "90s", encoding: "iso8601", want: "PT90S"},
	{in: "250ms", encoding: "iso8601", want: "PT0.25S"},
	{in: "90s", encoding: "timespan", want: "00:01:30"},
	{in: "26h3m4s500ms", encoding: "timespan", want: "1.02:03:04.5"},
	{in: "7ms", encoding: "timespan", want: "00:00:00.007"},
]

for i, c in durationCases {
	durationChecks: "\(i)": (contract.#RenderDuration & {in: c.in, encoding: c.encoding}).out & c.want
}
