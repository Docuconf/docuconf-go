package docuconf

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"
)

// ReloadStatus is the reload state of one file input (SPEC §4.6.2), for a
// health check or a metric. It never holds file content, and encodes as
// JSON with lower-case names.
type ReloadStatus struct {
	// Generation is 1 after boot, plus one per accepted reload. It is 0
	// for an optional input that was absent at boot.
	Generation int64 `json:"generation"`
	// LastReload is when the last accepted reload happened, or the zero
	// time if the input has not been reloaded since boot.
	LastReload time.Time `json:"lastReload,omitzero"`
	// LastRejected is the last changed content that failed its checks
	// and was not used, or nil. An accepted reload clears it.
	LastRejected *RejectedReload `json:"lastRejected,omitempty"`
}

// RejectedReload is a changed file input that failed its checks, so the
// previous content stayed current. It names the violation codes, never
// the content.
type RejectedReload struct {
	Time  time.Time `json:"time"`
	Input string    `json:"input"`
	Codes []Code    `json:"codes"`
}

// reloader holds the current content of a file input. For inputs with
// reload "watch" it re-reads the files when they change, checked at most
// once per interval: when the content is read, and in the background
// while an on-change hook is registered.
//
// A change is detected from each file's identity and metadata (device,
// inode, size and modification time), following symlinks, so the
// kubelet's swap of the ..data symlink shows up as a different file. An
// edit in place that keeps the size, within the file system's timestamp
// resolution, can be missed outside Kubernetes.
//
// A changed file that fails its checks is not used: the previous content
// stays current, and the failure is logged and kept in the status. It is
// checked again at the next interval.
type reloader[V any] struct {
	// check serialises reload checks and the hooks they call. A read that
	// finds a check running (including a read from inside a hook) returns
	// the current value instead of waiting.
	check sync.Mutex

	mu       sync.Mutex
	cur      V
	watch    bool
	interval time.Duration
	next     time.Time
	paths    []string
	stamps   []os.FileInfo
	load     func() (V, []Violation)
	logger   *slog.Logger
	name     string
	status   ReloadStatus
	hooks    []hook[V]
	nextID   int
	stop     chan struct{} // closes the background check, when one runs
}

type hook[V any] struct {
	id int
	fn func(V)
}

func newReloader[V any](b *fileBinding, cur V, paths []string, load func() (V, []Violation)) *reloader[V] {
	return &reloader[V]{
		cur:      cur,
		watch:    b.decl.reload == "watch",
		interval: b.interval,
		next:     time.Now().Add(b.interval),
		paths:    paths,
		stamps:   stampAll(paths),
		load:     load,
		logger:   b.logger,
		name:     b.decl.name,
		status:   ReloadStatus{Generation: 1},
	}
}

// get returns the current value, reloading it first if the input is
// watched, the interval has passed and the files changed.
func (r *reloader[V]) get() V {
	if r.watch && r.check.TryLock() {
		r.reload()
		r.check.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

// reload checks the files and swaps in changed content that passes its
// checks, then calls the hooks. The caller holds r.check.
func (r *reloader[V]) reload() {
	r.mu.Lock()
	now := time.Now()
	if now.Before(r.next) {
		r.mu.Unlock()
		return
	}
	r.next = now.Add(r.interval)
	st := stampAll(r.paths)
	if sameStamps(st, r.stamps) {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	// Load without holding mu, so readers keep the current value.
	v, viols := r.load()

	r.mu.Lock()
	if len(viols) > 0 {
		codes := make([]Code, 0, len(viols))
		for _, viol := range viols {
			if !slices.Contains(codes, viol.Code) {
				codes = append(codes, viol.Code)
			}
		}
		r.status.LastRejected = &RejectedReload{Time: time.Now(), Input: r.name, Codes: codes}
		r.mu.Unlock()
		for _, viol := range viols {
			r.logger.Warn("docuconf: changed file input rejected; keeping the previous content",
				"input", r.name, "code", string(viol.Code), "problem", viol.Message)
		}
		return
	}
	r.cur, r.stamps = v, st
	r.status.Generation++
	r.status.LastReload = time.Now()
	r.status.LastRejected = nil
	hooks := slices.Clone(r.hooks)
	r.mu.Unlock()
	r.logger.Info("docuconf: reloaded file input", "input", r.name)
	for _, h := range hooks {
		r.call(h.fn, v)
	}
}

// call runs one hook. A hook that panics is logged, by input name and the
// panic value's type only, and the other hooks still run.
func (r *reloader[V]) call(fn func(V), v V) {
	defer func() {
		if p := recover(); p != nil {
			r.logger.Error("docuconf: on-change hook failed", "input", r.name, "error", fmt.Sprintf("%T", p))
		}
	}()
	fn(v)
}

// onChange registers fn and, for a watched input, starts the background
// check if it is not running. The returned function unregisters fn and
// stops the check when no hook is left.
func (r *reloader[V]) onChange(fn func(V)) (cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	r.hooks = append(r.hooks, hook[V]{id: id, fn: fn})
	if r.watch && r.stop == nil {
		r.stop = make(chan struct{})
		go r.poll(r.stop)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.hooks = slices.DeleteFunc(r.hooks, func(h hook[V]) bool { return h.id == id })
			if len(r.hooks) == 0 && r.stop != nil {
				close(r.stop)
				r.stop = nil
			}
		})
	}
}

// poll checks the files every interval until stop is closed, so hooks
// fire without a read.
func (r *reloader[V]) poll(stop chan struct{}) {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.check.Lock()
			r.reload()
			r.check.Unlock()
		}
	}
}

func (r *reloader[V]) reloadStatus() ReloadStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.status
	if s.LastRejected != nil {
		rej := *s.LastRejected
		rej.Codes = slices.Clone(rej.Codes)
		s.LastRejected = &rej
	}
	return s
}

func stampAll(paths []string) []os.FileInfo {
	out := make([]os.FileInfo, len(paths))
	for i, p := range paths {
		out[i], _ = os.Stat(p)
	}
	return out
}

func sameStamps(a, b []os.FileInfo) bool {
	for i := range a {
		x, y := a[i], b[i]
		if (x == nil) != (y == nil) {
			return false
		}
		if x == nil {
			continue
		}
		if !os.SameFile(x, y) || !x.ModTime().Equal(y.ModTime()) || x.Size() != y.Size() {
			return false
		}
	}
	return true
}

// nopCancel is what OnChange returns for an input that holds no content.
func nopCancel() {}
