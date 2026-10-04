package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/toml"
	"encoding/yaml"
	"list"
	"path"
	"strings"
	"time"
)

// File inputs: configuration the app reads from the filesystem rather
// than from an environment variable. Structured config files, TLS key
// pairs, CA bundles, keystores, licence files.
//
// The contract states what the app needs: the path it reads, the kind of
// content, and the constraints that content must meet. The platform
// chooses where the content comes from (#FileSource), and #RenderFile
// mounts it at the declared path.

// Input names become volume names, so they follow DNS-label rules.
#InputName: =~"^[a-z]([-a-z0-9]{0,40}[a-z0-9])?$"

// An absolute, normalised path: no "..", ".", "//" or trailing slash.
#AbsPath: =~"^/[A-Za-z0-9._/-]+$" & !~"(^|/)\\.\\.?(/|$)" & !~"//" & !~"/$"

// Mounting a volume hides everything already at the mount point, so a
// file input must never be mounted over a directory the image or the OS
// needs. A file is mounted at its parent directory; a TLS key pair at
// its own directory.
#ReservedDirs: [
	"/", "/app", "/bin", "/boot", "/dev", "/etc", "/etc/pki", "/etc/ssl",
	"/etc/ssl/certs", "/home", "/lib", "/lib64", "/opt", "/proc", "/root",
	"/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/usr/lib", "/usr/local",
	"/usr/share", "/var", "/var/lib", "/var/run",
]

#FileCommon: {
	name:        #InputName
	description: strings.MinRunes(5)
	required:    *false | bool
	secret:      *false | bool
	// Where the app reads the input. A directory for "tls", a file otherwise.
	path: #AbsPath
	// When the app reads the path from an environment variable, the
	// renderer sets this variable to `path`. It must not also be in vars.
	pathEnv?: #EnvName
	// restart: the app reads the file once, so a changed source needs a
	//          rollout. watch: the app reloads the file itself.
	reload:   *"restart" | "watch"
	maxSize?: int & >0 // bytes
	group?:   string
	deprecated?: {
		message:     string
		replacedBy?: #InputName
	}
}

#File: #ConfigFile | #TLSFile | #CABundleFile | #KeystoreFile | #TextFile | #BinaryFile

// A structured configuration file. `schema` is a JSON Schema generated
// from the app's own type (a .NET options class, a pydantic model, a Zod
// schema), so the file is checked against the same type the app binds.
#ConfigFile: close({
	#FileCommon
	type:   "config"
	format: "json" | "yaml" | "toml"
	schema?: {...}
})

// A TLS key pair in the kubernetes.io/tls layout: a directory holding
// tls.crt, tls.key and, when requireCA is set, ca.crt.
#TLSFile: close({
	#FileCommon
	type:   "tls"
	secret: true
	// Names the certificate must cover. A wildcard on the issued
	// certificate (*.example.com) covers a single-label name under it.
	dnsNames?: [string, ...string]
	keyAlgorithms?: [...("RSA" | "ECDSA" | "Ed25519")]
	// The certificate must always have at least this long left. With
	// cert-manager this is guaranteed by renewBefore.
	minRemaining?: #Duration
	requireCA:     *false | bool
})

// A PEM file of one or more CA certificates, for trusting private CAs.
#CABundleFile: close({
	#FileCommon
	type:            "caBundle"
	minCertificates: *1 | int & >=1
})

// A PKCS#12 or JKS keystore. Its password is a separate secret variable.
#KeystoreFile: close({
	#FileCommon
	type:         "keystore"
	secret:       true
	format:       "pkcs12" | "jks"
	passwordVar?: #EnvName
})

// A text file, such as a licence key.
#TextFile: close({
	#FileCommon
	type:       "text"
	pattern?:   string // RE2
	minLength?: int & >=0
	maxLength?: int & >=0
})

// Opaque bytes, such as a GeoIP database. Only size can be checked.
#BinaryFile: close({
	#FileCommon
	type: "binary"
})

// Where the platform gets a file input's content. Fields marked
// "resolved" are filled in by the platform tooling from the cluster
// (never from secret contents) so more can be checked before deploy.
#FileSource: #InlineSource | #ConfigMapSource | #SecretSource | #CertificateSource | #CSISource | #ImageSource

// Content written into the platform repository. Only for non-secret
// inputs; the renderer turns it into an immutable, content-hashed
// ConfigMap.
#InlineSource: close({inline: string})

#ConfigMapSource: close({configMap: {
	name: string
	key?: string
}})

#SecretSource: close({secret: {
	name:  string
	key?:  string
	type?: string // resolved: the Secret's type
	keys?: [...string] // resolved: the Secret's data keys, never values
}})

// A cert-manager Certificate. secretName and the other fields are
// resolved from the Certificate's spec, which is not secret.
#CertificateSource: close({certificate: {
	name:       string
	secretName: string
	dnsNames?: [...string]
	privateKey?: algorithm?: "RSA" | "ECDSA" | "Ed25519"
	duration?:    #Duration
	renewBefore?: #Duration
}})

// The Secrets Store CSI driver (Vault, AWS, Azure, GCP). Nothing about
// the content is visible before the pod starts.
#CSISource: close({csi: {
	driver:              *"secrets-store.csi.k8s.io" | string
	secretProviderClass: string
}})

// An OCI image mounted read-only with a Kubernetes image volume, for
// content too large for a ConfigMap (1 MiB), such as a GeoIP database.
// Needs a cluster with image volumes enabled.
#ImageSource: close({image: {
	reference:   string
	pullPolicy?: "Always" | "IfNotPresent" | "Never"
}})

#SourceKind: {
	source: #FileSource
	kind:   "inline" | "configMap" | "secret" | "certificate" | "csi" | "image"
	if source.inline != _|_ {kind: "inline"}
	if source.configMap != _|_ {kind: "configMap"}
	if source.secret != _|_ {kind: "secret"}
	if source.certificate != _|_ {kind: "certificate"}
	if source.csi != _|_ {kind: "csi"}
	if source.image != _|_ {kind: "image"}
}

// #CheckFile binds a file input to its source and checks everything that
// can be known before deploy. What remains (the contents of secrets,
// certificate expiry, key/certificate match) is checked by the SDK at
// boot.
#CheckFile: {
	file:     #File
	source:   #FileSource
	#schema?: _ // the file's JSON Schema, compiled to CUE by the toolchain

	let kind = (#SourceKind & {"source": source}).kind
	let F = file

	if F.secret {
		secretFromSecretStore: true & (kind == "secret" || kind == "certificate" || kind == "csi")
	}
	if kind == "certificate" {
		certificateOnlyForTLS: true & F.type == "tls"
	}
	if kind == "inline" {
		binaryCannotBeInline: true & F.type != "binary"
	}

	// A single file needs the key that holds it; a TLS directory takes the
	// whole Secret.
	if F.type != "tls" && kind == "configMap" {
		keyRequired: true & source.configMap.key != _|_
	}
	if F.type != "tls" && kind == "secret" {
		keyRequired: true & source.secret.key != _|_
	}
	if F.type == "tls" && kind == "secret" {
		wholeSecretForTLS: true & source.secret.key == _|_
		if source.secret.type != _|_ {
			tlsSecretType: true & source.secret.type == "kubernetes.io/tls"
		}
		if source.secret.keys != _|_ {
			hasCertAndKey: true & list.Contains(source.secret.keys, "tls.crt") & list.Contains(source.secret.keys, "tls.key")
			if F.requireCA {
				hasCA: true & list.Contains(source.secret.keys, "ca.crt")
			}
		}
	}

	if kind == "certificate" {
		let C = source.certificate
		if F.dnsNames != _|_ && C.dnsNames != _|_ {
			coversDNSName: {
				for d in F.dnsNames {
					let parts = strings.SplitN(d, ".", 2)
					let wildcard = [if len(parts) == 2 {"*.\(parts[1])"}, ""][0]
					(d): true & (list.Contains(C.dnsNames, d) || list.Contains(C.dnsNames, wildcard))
				}
			}
		}
		if F.keyAlgorithms != _|_ && C.privateKey.algorithm != _|_ {
			allowedKeyAlgorithm: true & list.Contains(F.keyAlgorithms, C.privateKey.algorithm)
		}
		if F.minRemaining != _|_ && C.renewBefore != _|_ {
			// cert-manager renews when renewBefore is left, so that is the
			// least validity the app can ever see.
			renewsBeforeMinRemaining: true & time.ParseDuration(C.renewBefore) >= time.ParseDuration(F.minRemaining)
		}
	}

	if kind == "inline" {
		let content = source.inline
		if F.maxSize != _|_ {
			withinMaxSize: true & len(content) <= F.maxSize
		}
		if F.type == "config" {
			if F.format == "json" {
				wellFormed: true & json.Valid(content)
				if #schema != _|_ {matchesSchema: json.Validate(content, #schema)}
			}
			if F.format == "yaml" {
				wellFormed: yaml.Validate(content, _)
				if #schema != _|_ {matchesSchema: yaml.Validate(content, #schema)}
			}
			if F.format == "toml" {
				wellFormed: toml.Unmarshal(content)
				if #schema != _|_ {matchesSchema: toml.Unmarshal(content) & #schema}
			}
		}
		if F.type == "text" {
			text: content
			if F.pattern != _|_ {text: =~F.pattern}
			if F.minLength != _|_ {text: strings.MinRunes(F.minLength)}
			if F.maxLength != _|_ {text: strings.MaxRunes(F.maxLength)}
		}
		if F.type == "caBundle" {
			enoughCertificates: true & strings.Count(content, "-----BEGIN CERTIFICATE-----") >= F.minCertificates
		}
	}
}

// #RenderFile produces the Kubernetes pieces that deliver one file input.
#RenderFile: {
	service: string
	name:    string
	file:    #File
	source:  #FileSource

	let F = file
	let S = source
	let N = name
	let vol = "dc-\(N)"
	let isDir = F.type == "tls"
	let mdir = [if isDir {F.path}, path.Dir(F.path, path.Unix)][0]
	let fileName = path.Base(F.path, path.Unix)

	let mode = [if F.secret {256}, 292][0] // 0400 for secrets, 0444 otherwise

	volume: {name: vol}
	volumeMount: {name: vol, mountPath: mdir, readOnly: true}
	env: [if F.pathEnv != _|_ {{name: F.pathEnv, value: strings.Replace(F.path, "$", "$$", -1)}}]
	configMaps: [...]
	restartTriggers: [...]

	// Files are projected with `items` instead of `subPath`: a subPath
	// mount never sees updates, which would silently break rotation.
	if S.inline != _|_ {
		// Content-hashed and immutable: a change creates a new ConfigMap and
		// therefore a rollout, whatever the reload setting.
		let hash = strings.SliceRunes(hex.Encode(sha256.Sum256(S.inline)), 0, 10)
		let cm = "\(service)-\(N)-\(hash)"
		volume: configMap: {name: cm, defaultMode: mode, items: [{key: fileName, path: fileName}]}
		configMaps: [{
			apiVersion: "v1"
			kind:       "ConfigMap"
			metadata: name: cm
			immutable: true
			data: (fileName): S.inline
		}]
	}
	if S.configMap != _|_ {
		volume: configMap: {name: S.configMap.name, defaultMode: mode, items: [{key: S.configMap.key, path: fileName}]}
		if F.reload == "restart" {restartTriggers: [{kind: "ConfigMap", name: S.configMap.name}]}
	}
	if S.secret != _|_ {
		if isDir {
			volume: secret: {secretName: S.secret.name, defaultMode: mode}
		}
		if !isDir {
			volume: secret: {secretName: S.secret.name, defaultMode: mode, items: [{key: S.secret.key, path: fileName}]}
		}
		if F.reload == "restart" {restartTriggers: [{kind: "Secret", name: S.secret.name}]}
	}
	if S.certificate != _|_ {
		volume: secret: {secretName: S.certificate.secretName, defaultMode: mode}
		if F.reload == "restart" {restartTriggers: [{kind: "Secret", name: S.certificate.secretName}]}
	}
	if S.image != _|_ {
		// The image's files appear under the mount directory; the file the
		// app reads must sit at the image root under the declared name.
		volume: image: {reference: S.image.reference, if S.image.pullPolicy != _|_ {pullPolicy: S.image.pullPolicy}}
	}
	if S.csi != _|_ {
		volume: csi: {driver: S.csi.driver, readOnly: true, volumeAttributes: secretProviderClass: S.csi.secretProviderClass}
		if F.reload == "restart" {restartTriggers: [{kind: "SecretProviderClass", name: S.csi.secretProviderClass}]}
	}
}
