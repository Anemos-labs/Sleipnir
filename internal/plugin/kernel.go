package plugin

import (
	"errors"
	"fmt"
	"sync"
)

// Scope collects what one plugin registered, as disposers. Closing the scope
// runs them in reverse order of registration, so a plugin that is turned off
// leaves nothing behind.
type Scope struct {
	ID       string
	mu       sync.Mutex
	disposer []func()
	closed   bool
}

// OnDispose records fn to run when the scope closes. On a closed scope fn runs at once.
func (sc *Scope) OnDispose(fn func()) {
	if fn == nil {
		return
	}
	sc.mu.Lock()
	if sc.closed {
		sc.mu.Unlock()
		fn()
		return
	}
	sc.disposer = append(sc.disposer, fn)
	sc.mu.Unlock()
}

// Close runs the disposers, last first, once. A disposer that panics does not
// stop the others; the first panic is returned as an error.
func (sc *Scope) Close() (err error) {
	sc.mu.Lock()
	if sc.closed {
		sc.mu.Unlock()
		return nil
	}
	sc.closed = true
	ds := sc.disposer
	sc.disposer = nil
	sc.mu.Unlock()
	for i := len(ds) - 1; i >= 0; i-- {
		func() {
			defer func() {
				if r := recover(); r != nil && err == nil {
					err = fmt.Errorf("plugin %s: disposer: %v", sc.ID, r)
				}
			}()
			ds[i]()
		}()
	}
	return err
}

// Activator turns a plugin on inside its scope: it registers what the plugin
// contributes and records a disposer for each registration.
type Activator func(sc *Scope) error

// Kernel activates the plugins of a Set that have an Activator, in the set's
// order, and turns them off again in reverse order. A failing optional plugin
// is marked in error and the others carry on; a failing core plugin stops
// activation with its error.
type Kernel struct {
	mu     sync.Mutex
	acts   map[string]Activator
	active []*Scope // activation order
	set    *Set
	// OnState, when set, is told every state change: id, state, reason.
	OnState func(id, state, reason string)
}

// NewKernel returns a kernel with the given activators by plugin id.
func NewKernel(acts map[string]Activator) *Kernel {
	k := &Kernel{acts: map[string]Activator{}}
	for id, a := range acts {
		k.acts[id] = a
	}
	return k
}

// ErrCore wraps the error of a core plugin that failed to activate.
var ErrCore = errors.New("core plugin failed")

// Start activates every plugin of set that is on and has an activator.
func (k *Kernel) Start(set *Set) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.set = set
	for _, id := range set.Order() {
		st, _ := set.Get(id)
		if st.State != StateOn {
			continue
		}
		if err := k.activate(st); err != nil {
			return err
		}
	}
	return nil
}

// activate runs one plugin's activator in a new scope (k.mu held).
func (k *Kernel) activate(st *Status) error {
	act := k.acts[st.ID]
	if act == nil {
		return nil
	}
	sc := &Scope{ID: st.ID}
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return act(sc)
	}()
	if err != nil {
		_ = sc.Close()
		if st.Core {
			return fmt.Errorf("%w: %s: %v", ErrCore, st.ID, err)
		}
		st.State, st.Reason = StateError, err.Error()
		k.tell(st.ID, StateError, err.Error())
		k.dependents(st.ID)
		return nil
	}
	k.active = append(k.active, sc)
	k.tell(st.ID, StateOn, "")
	return nil
}

// dependents marks plugins that need a failed one as pending (k.mu held).
func (k *Kernel) dependents(failed string) {
	for _, st := range k.set.List() {
		if st.State == StateOn && contains(st.Requires, failed) {
			if orig, ok := k.set.Get(st.ID); ok {
				orig.State, orig.Reason = StatePending, "needs "+failed+", which is "+StateError
			}
		}
	}
}

// tell reports a state change.
func (k *Kernel) tell(id, state, reason string) {
	if k.OnState != nil {
		k.OnState(id, state, reason)
	}
}

// Active lists the ids of the plugins activated, in activation order.
func (k *Kernel) Active() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, 0, len(k.active))
	for _, sc := range k.active {
		out = append(out, sc.ID)
	}
	return out
}

// Reconcile moves the kernel to next: plugins that are no longer on are
// turned off (in reverse activation order) and plugins newly on are activated
// (in next's order). Plugins on in both stay as they are.
func (k *Kernel) Reconcile(next *Set) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	var keep []*Scope
	var errs []error
	for i := len(k.active) - 1; i >= 0; i-- {
		sc := k.active[i]
		if next.On(sc.ID) {
			keep = append([]*Scope{sc}, keep...)
			continue
		}
		if err := sc.Close(); err != nil {
			errs = append(errs, err)
		}
		k.tell(sc.ID, StateOff, "")
	}
	k.active = keep
	k.set = next
	running := map[string]bool{}
	for _, sc := range keep {
		running[sc.ID] = true
	}
	for _, id := range next.Order() {
		st, _ := next.Get(id)
		if st.State != StateOn || running[id] {
			continue
		}
		if err := k.activate(st); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Stop turns every active plugin off, last activated first.
func (k *Kernel) Stop() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	var errs []error
	for i := len(k.active) - 1; i >= 0; i-- {
		if err := k.active[i].Close(); err != nil {
			errs = append(errs, err)
		}
		k.tell(k.active[i].ID, StateOff, "")
	}
	k.active = nil
	return errors.Join(errs...)
}
