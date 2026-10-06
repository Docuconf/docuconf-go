package docuconf

import (
	"log/slog"
	"os"
	"sync"
	"time"
)

// reloader holds the current content of a file input. For inputs with
// reload "watch" it re-reads the files when they change, checked at most
// once per interval. Kubernetes updates projected volumes by swapping a
// symlink, which os.Stat follows, so a swap shows up as a different file.
//
// A changed file that fails its checks is not used: the previous content
// stays current and the failure is logged.
type reloader[V any] struct {
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
	}
}

func (r *reloader[V]) get() V {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.watch {
		return r.cur
	}
	now := time.Now()
	if now.Before(r.next) {
		return r.cur
	}
	r.next = now.Add(r.interval)
	st := stampAll(r.paths)
	if sameStamps(st, r.stamps) {
		return r.cur
	}
	v, viols := r.load()
	if len(viols) > 0 {
		for _, viol := range viols {
			r.logger.Warn("docuconf: changed file input rejected; keeping the previous content",
				"input", r.name, "code", string(viol.Code), "problem", viol.Message)
		}
		return r.cur
	}
	r.cur, r.stamps = v, st
	r.logger.Info("docuconf: reloaded file input", "input", r.name)
	return r.cur
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
