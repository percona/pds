package hub

import "sync"

// spokeLocks serializes Git writes and heartbeats for one environment. A slow
// commit for spoke-a must not hold up spoke-b's heartbeat past the stale
// window.
type spokeLocks struct {
	mu   sync.Mutex
	held map[string]*spokeLock
}

type spokeLock struct {
	mu   sync.Mutex
	refs int
}

func newSpokeLocks() *spokeLocks {
	return &spokeLocks{held: map[string]*spokeLock{}}
}

func (l *spokeLocks) lock(name string) func() {
	if l == nil {
		return func() {}
	}
	l.mu.Lock()
	e := l.held[name]
	if e == nil {
		e = &spokeLock{}
		l.held[name] = e
	}
	e.refs++
	l.mu.Unlock()
	e.mu.Lock()

	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(l.held, name)
		}
		l.mu.Unlock()
	}
}
