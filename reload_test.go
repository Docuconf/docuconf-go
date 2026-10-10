package docuconf_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docuconf/docuconf-go"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a log destination that hooks and the background check
// may write to concurrently.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type motdConfig struct {
	// Message of the day.
	Motd docuconf.TextFile `file:"motd,required" path:"/etc/app/motd/motd.txt" pattern:"^ok" reload:"watch"`
}

// replace writes content to p as Kubernetes does: a new file, renamed
// over the old one.
func replace(t *testing.T, p, content string) {
	t.Helper()
	tmp := p + ".tmp"
	writeFile(t, tmp, []byte(content))
	require.NoError(t, os.Rename(tmp, p))
}

func receive[V any](t *testing.T, ch <-chan V) V {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no hook call")
	}
	panic("unreachable")
}

func TestOnChangeAndReloadStatus(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "etc/app/motd/motd.txt")
	writeFile(t, p, []byte("ok 1"))
	logs := &syncBuffer{}
	cfg, err := docuconf.ParseWithOptions[motdConfig](docuconf.Options{
		Environment: map[string]string{}, FileRoot: root, TerminationLog: "-",
		WatchInterval: time.Millisecond, Logger: slog.New(slog.NewTextHandler(logs, nil)),
	})
	require.NoError(t, err)
	require.Equal(t, docuconf.ReloadStatus{Generation: 1}, cfg.Motd.ReloadStatus())

	// Several hooks; the first panics, and the second still runs.
	got := make(chan string, 10)
	cancelPanic := cfg.Motd.OnChange(func(string) { panic(errors.New("ok 2 leaked")) })
	defer cancelPanic()
	cancel := cfg.Motd.OnChange(func(s string) { got <- s })

	// An accepted change: the hooks run without a read, in the background.
	start := time.Now()
	replace(t, p, "ok 2, longer")
	require.Equal(t, "ok 2, longer", receive(t, got))
	require.Equal(t, "ok 2, longer", cfg.Motd.Content())
	st := cfg.Motd.ReloadStatus()
	require.Equal(t, int64(2), st.Generation)
	require.False(t, st.LastReload.Before(start))
	require.Nil(t, st.LastRejected)
	// The failing hook is logged by input name and error type, never with
	// the value or the error's text.
	require.Contains(t, logs.String(), `msg="docuconf: on-change hook failed" input=motd error=*errors.errorString`)
	require.NotContains(t, logs.String(), "leaked")

	// A rejected change: no hook call, the previous value stays, and the
	// status names the input and the codes, never the content.
	replace(t, p, "bad content")
	require.Eventually(t, func() bool { return cfg.Motd.ReloadStatus().LastRejected != nil }, 5*time.Second, time.Millisecond)
	st = cfg.Motd.ReloadStatus()
	require.Equal(t, int64(2), st.Generation)
	require.Equal(t, "motd", st.LastRejected.Input)
	require.Equal(t, []docuconf.Code{docuconf.CodePatternMismatch}, st.LastRejected.Codes)
	require.False(t, st.LastRejected.Time.Before(st.LastReload))
	require.Equal(t, "ok 2, longer", cfg.Motd.Content())
	j, err := json.Marshal(st)
	require.NoError(t, err)
	require.NotContains(t, string(j), "bad content")
	require.Contains(t, string(j), `"generation":2`)
	require.Contains(t, string(j), `"codes":["pattern_mismatch"]`)
	select {
	case s := <-got:
		t.Fatalf("hook called with %q for a rejected change", s)
	case <-time.After(20 * time.Millisecond):
	}

	// The next accepted change clears the rejection.
	replace(t, p, "ok 3")
	require.Equal(t, "ok 3", receive(t, got))
	st = cfg.Motd.ReloadStatus()
	require.Equal(t, int64(3), st.Generation)
	require.Nil(t, st.LastRejected)

	// After cancel, the hook is not called; a read still reloads.
	cancel()
	cancel() // a second call does nothing
	replace(t, p, "ok 4!")
	require.Eventually(t, func() bool { return cfg.Motd.Content() == "ok 4!" }, 5*time.Second, time.Millisecond)
	select {
	case s := <-got:
		t.Fatalf("cancelled hook called with %q", s)
	case <-time.After(20 * time.Millisecond):
	}
}

// A hook may read the input it watches: the read returns the new value
// and does not wait for the reload that called the hook.
func TestOnChangeHookReads(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "etc/app/motd/motd.txt")
	writeFile(t, p, []byte("ok 1"))
	cfg, err := docuconf.ParseWithOptions[motdConfig](docuconf.Options{
		Environment: map[string]string{}, FileRoot: root, TerminationLog: "-", WatchInterval: time.Millisecond,
	})
	require.NoError(t, err)
	got := make(chan string, 10)
	defer cfg.Motd.OnChange(func(string) { got <- cfg.Motd.Content() })()
	replace(t, p, "ok 2")
	require.Equal(t, "ok 2", receive(t, got))
}

// An input with reload "restart" never reloads: its hooks never run and
// its generation stays 1. An absent optional input has generation 0.
func TestReloadStatusRestartAndAbsent(t *testing.T) {
	type cfgT struct {
		// Licence key.
		Licence docuconf.TextFile `file:"licence" path:"/etc/app/lic/key.txt"`
		// Banner.
		Banner docuconf.TextFile `file:"banner" path:"/etc/app/banner/banner.txt" reload:"watch"`
	}
	root := t.TempDir()
	p := filepath.Join(root, "etc/app/lic/key.txt")
	writeFile(t, p, []byte("v1"))
	cfg, err := docuconf.ParseWithOptions[cfgT](docuconf.Options{
		Environment: map[string]string{}, FileRoot: root, TerminationLog: "-", WatchInterval: time.Millisecond,
	})
	require.NoError(t, err)
	called := false
	defer cfg.Licence.OnChange(func(string) { called = true })()
	replace(t, p, "v2, longer")
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, "v1", cfg.Licence.Content())
	require.Equal(t, docuconf.ReloadStatus{Generation: 1}, cfg.Licence.ReloadStatus())
	require.False(t, called)

	require.False(t, cfg.Banner.Present())
	require.Equal(t, docuconf.ReloadStatus{}, cfg.Banner.ReloadStatus())
	cfg.Banner.OnChange(func(string) {})() // registering on an absent input is harmless
}

// A keystore reload uses the password read at boot. A new keystore
// written with another password is rejected as keystore_unreadable, and
// the previous keystore stays current.
func TestKeystoreReloadKeepsBootPassword(t *testing.T) {
	type cfgT struct {
		// Keystore password.
		Password string `env:"KS_PASSWORD" secret:"true"`
		// Client keystore.
		KS docuconf.Keystore `file:"ks,required" path:"/etc/app/ks/ks.p12" passwordVar:"KS_PASSWORD" reload:"watch"`
	}
	root := t.TempDir()
	ca := newCA(t)
	p := filepath.Join(root, "etc/app/ks/ks.p12")
	writeFile(t, p, keystore(t, ca, "boot-password"))
	env := map[string]string{"KS_PASSWORD": "boot-password"}
	cfg, err := docuconf.ParseWithOptions[cfgT](docuconf.Options{
		Environment: env, FileRoot: root, TerminationLog: "-", WatchInterval: time.Millisecond,
	})
	require.NoError(t, err)
	before := cfg.KS.Certificate().Leaf.SerialNumber
	got := make(chan string, 10)
	defer cfg.KS.OnChange(func(c tls.Certificate) { got <- c.Leaf.SerialNumber.String() })()

	// Same password: accepted.
	tmp := p + ".tmp"
	writeFile(t, tmp, keystore(t, ca, "boot-password"))
	require.NoError(t, os.Rename(tmp, p))
	serial := receive(t, got)
	require.NotEqual(t, before.String(), serial)

	// A new password: rejected, even when the environment it was read
	// from has changed since boot.
	env["KS_PASSWORD"] = "new-password"
	writeFile(t, tmp, keystore(t, ca, "new-password"))
	require.NoError(t, os.Rename(tmp, p))
	require.Eventually(t, func() bool { return cfg.KS.ReloadStatus().LastRejected != nil }, 5*time.Second, time.Millisecond)
	st := cfg.KS.ReloadStatus()
	require.Equal(t, []docuconf.Code{docuconf.CodeKeystoreUnreadable}, st.LastRejected.Codes)
	require.Equal(t, int64(2), st.Generation)
	require.Equal(t, serial, cfg.KS.Certificate().Leaf.SerialNumber.String())
}

// Contract-first mode reloads a watched file input exactly as a declared
// struct does, and rejects a watched overlay, which it reads once.
func TestLoadContractWatch(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "etc/app/motd.txt")
	writeFile(t, p, []byte("ok 1"))
	c := contractWithFiles(``, `"motd": {"type": "text", "description": "Message of the day", "path": "/etc/app/motd.txt", "pattern": "^ok", "reload": "watch"}`)
	vals, err := docuconf.LoadContract(c, docuconf.Options{Environment: map[string]string{}, FileRoot: root, TerminationLog: "-", WatchInterval: time.Millisecond})
	require.NoError(t, err)
	motd := vals["motd"].(docuconf.TextFile)
	got := make(chan string, 10)
	defer motd.OnChange(func(s string) { got <- s })()
	replace(t, p, "ok 2, longer")
	require.Equal(t, "ok 2, longer", receive(t, got))
	require.Equal(t, int64(2), motd.ReloadStatus().Generation)

	overlay := []byte(`{"apiVersion": "docuconf.dev/v1alpha1", "kind": "ConfigContract",
		"metadata": {"name": "svc", "generator": {"language": "dotnet", "sdk": "x", "version": "1"}},
		"vars": {"PAGE_SIZE": {"type": "int", "description": "Items per page", "configKey": "Catalog:PageSize"}},
		"overlays": {"platform": {"format": "json", "path": "/app/config/platform.json", "keySeparator": ":", "reload": "watch"}}}`)
	_, err = docuconf.LoadContract(overlay, docuconf.Options{Environment: map[string]string{}, TerminationLog: "-"})
	var derr *docuconf.DeclarationError
	require.True(t, errors.As(err, &derr), "%v", err)
	require.Contains(t, err.Error(), "overlay platform: reload watch is not supported in contract-first mode")
	// The same contract formats, and loads with reload restart.
	_, err = docuconf.ContractCUE(overlay, "")
	require.NoError(t, err)
	_, err = docuconf.LoadContract([]byte(strings.Replace(string(overlay), `"watch"`, `"restart"`, 1)), docuconf.Options{Environment: map[string]string{}, TerminationLog: "-"})
	require.NoError(t, err)
}
