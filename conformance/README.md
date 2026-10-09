# docuconf conformance suite

Language-neutral test cases that every docuconf SDK runs (SPEC §12). Two
SDKs that disagree on a case cannot both be right, so the suite is how
"the platform and the app accept exactly the same values" is kept true
across languages.

| Path | What it is |
| --- | --- |
| `load/*.yaml` | The cases, written by hand. Edit these. |
| `cases.json` | Generated from `load/`. Every SDK's runner reads this file. Do not edit. |

Regenerate `cases.json` after changing `load/` (the CLI's tests fail if it
is out of date):

```sh
cd cmd/docuconf && go run . conformance -dir ../../conformance
```

## Writing cases

A `load/*.yaml` file declares variables, as in a contract's `vars`, and a
list of cases:

```yaml
description: Integer variables (SPEC §4.3, §5).
vars:
  PORT: {type: int, description: Port to listen on, min: 1, max: 65535, default: 8080}
cases:
  - name: typed values          # what the platform sends
    values: {PORT: 9090}
  - name: below min             # what the app might see anyway
    env: {PORT: "0"}
    errors: [{var: PORT, code: out_of_range}]
  - name: empty is unset
    env: {PORT: ""}
    expect: {PORT: 8080}
```

- **`values`**: typed platform values. They must pass `#Validate`. The
  generator renders them with `#Render` into the environment the pod gets,
  and derives the expected result from the values and the defaults. A
  values case runs once per list encoding (`csv`, `json`, `indexed`) and
  duration encoding (`go`, `iso8601`, `seconds`, `timespan`) for each list
  or duration variable it sets whose encoding the file leaves open. Limit
  that with `only: {list: [json, indexed]}`.
- **`env`**: the raw environment, as strings, for input the platform would
  never send. Give `expect` (the variables you care about; the rest are
  derived from defaults) or `errors`.
- **`requires`**: capability tags (below). Use them only where a host
  genuinely cannot do what the case tests, or for a transitional tag. A
  file may list `requires` at its top level too, for every case in it.

Keep each case to one problem per variable: SDKs may stop checking a
variable after its first violation.

## Running the suite in an SDK

Each SDK has a runner in its own test suite. It needs the SDK's
**contract-first mode** (SPEC §11.2, item 11): validate an environment
against a contract given as JSON, with no in-language declaration,
returning typed values.

SDK CI already checks out docuconf-go `main`; the runner reads
`conformance/cases.json` from that checkout. Locate it with
`DOCUCONF_CONFORMANCE` (the path to `cases.json`), falling back to
`../docuconf-go/conformance/cases.json` for local runs, and fail (not skip)
when `DOCUCONF_REQUIRE_CONFORMANCE=1` and the file is missing.

`cases.json` looks like this:

```json
{
  "version": 1,
  "cases": [
    {
      "id": "int/below min",
      "source": "load/int.yaml",
      "requires": [],
      "contract": {"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract", "metadata": {...}, "vars": {...}},
      "env": {"PORT": "0"},
      "errors": [{"var": "PORT", "code": "out_of_range"}]
    }
  ]
}
```

For every case:

1. **Skip** it only if `requires` holds a tag the SDK does not support,
   and report the number skipped. The SDK's README lists its unsupported
   tags. Treat a tag the runner does not know as unsupported: skip the
   case, never run it, so that a new tag does not break an SDK written
   before it. Keep a list of the tags the SDK supports, not of the ones
   it lacks.
2. **Load** `contract` with the contract-first mode, using `env` as the
   whole environment: no process environment, no `.env` file, no config
   files. `contract` has every default filled in (`required: false`,
   `encoding: "csv"`, `separator: ","`, ...).
3. If the case has **`expect`**, loading succeeds, and every variable's
   typed value, converted to JSON, equals `expect[name]`:
   - absent optional variables are `null`;
   - `duration` is the canonical Go form: units `h`, `m`, `s`, `ms`, `us`,
     `ns`, each at most once, zero units omitted, `0s` for zero (`1m30s`,
     `1s500ms`);
   - `int` compares exactly (cases hold 64-bit values; parse `expect` with
     a big-number-safe JSON reader where the language needs one);
   - `float` compares numerically (`3` equals `3.0`);
   - `list` and `keySet` are arrays (a key set's keys in order), `json`
     any JSON value, everything else a string or bool.
4. If the case has **`errors`**, loading fails, and the set of
   (variable, code) pairs reported equals `errors`, in any order. No error
   message, and nothing written to the termination log, may contain the
   raw `env` value of a variable whose contract entry has `secret: true`.

Report each failing case by `id`, so a failure points at its YAML source.

## Capability tags

| Tag | Meaning | SDKs that may skip it |
| --- | --- | --- |
| `int64` | The host holds every 64-bit integer as an integer. | JavaScript-runtime SDKs (T3 Env, Gleam on JavaScript) |
| `json-schema` | Contract-first mode validates `json` values against the variable's JSON Schema. | Any SDK without a JSON Schema validator, documented in its README |
| `key-set` | The `keySet` type (SPEC §4.3), in `load/key_set_type.yaml`. | Transitional: an SDK that does not have the type yet. Every SDK MUST support it by `v1beta1`. |
| `deprecated` | Deprecated inputs (SPEC §4.2): a deprecated variable that is set still loads, in `load/deprecated.yaml`. | Transitional: an SDK that does not read `deprecated` yet. Every SDK MUST support it by `v1beta1`. |
| `strict-parsing` | The exact parsing rules of SPEC §5, in `load/strict.yaml`: `bool` is only `true` or `false` in any case (never `1`, `t`, `yes`, `on`), `int` only decimal digits with an optional sign (never `0x10`, `1_000`, `1e3`), `float` only decimal (never hex, `inf`, `.5`), durations only their encoding's grammar, and nothing is trimmed, including `csv` items. | Transitional: an SDK whose host library is still more lenient than the spec. Every SDK MUST support it by `v1beta1`. |

The transitional tags let the suite gain cases for a new feature while
SDKs written before it stay green. When every SDK supports a feature, or
at `v1beta1` at the latest, its tag is removed from the cases.

## Scope

v1 covers variables: every type, encoding and constraint, required and
optional values, empty values, secrets (including key sets: the `keySet`
type in `key_set_type.yaml`, and the older secret list convention in
`key_set.yaml`), deprecated variables, unresolved injector references,
and aggregate error reporting. File inputs, profiles and config-file
overlays are tested inside each SDK for now.
