package util

import (
	"sync"
)

type lockInfo struct {
	sync.Mutex
	refCnt uint64
}

type lockByKey struct {
	sync.Mutex
	lockKeys map[string]*lockInfo
}

// create a mutex associated with the lock key
func (lbk *lockByKey) Acquire(lockKey string) func() {
	li := &lockInfo{refCnt: 0}
	lbk.Lock()
	val, ok := lbk.lockKeys[lockKey]
	if ok {
		li = val
	} else {
		lbk.lockKeys[lockKey] = li
	}
	li.refCnt++
	lbk.Unlock()

	// take the lock and then return function to unlock
	li.Lock()
	return func() {
		li.Unlock()
		lbk.Lock()
		li.refCnt--
		if li.refCnt == 0 {
			delete(lbk.lockKeys, lockKey)
		}
		lbk.Unlock()
	}
}

var LockByKey = lockByKey{
	lockKeys: make(map[string]*lockInfo),
}
