package util

import (
	"sync"
)

type lockByKey struct {
	sync.Map
}

// create a mutex associate with the lock key
func (lbk *lockByKey) Acquire(lockKey string) func() {
	m := &sync.Mutex{}
	if obj, loaded := lbk.LoadOrStore(lockKey, m); loaded {
		m = obj.(*sync.Mutex)
	}
	m.Lock()
	return func() { m.Unlock() }
}

var LockByKey lockByKey
