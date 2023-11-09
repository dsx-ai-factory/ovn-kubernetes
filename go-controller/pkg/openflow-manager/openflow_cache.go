package openFlowManager

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/syncmap"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"k8s.io/klog/v2"
)

type bridgeOpenflowCache struct {
	sync.Mutex
	bridge    string
	isRunning bool
	flowChan  chan struct{}
	stopChan  chan struct{}
	// flow cache, use map instead of array for readability when debugging
	// first map key is owner name, second map key is flow cookie, must be unique for this bridge
	flowCache map[string]map[string][]string
}

type OpenFlowManager struct {
	flowCacheMap *syncmap.SyncMap[*bridgeOpenflowCache]
	wg           *sync.WaitGroup
	stopChan     chan struct{}
}

// OpenFlowCacheManager managers open-flows of all bridges across board
var OpenFlowCacheManager *OpenFlowManager

func NewOpenFlowCacheManager(wg *sync.WaitGroup, stopChan chan struct{}) {
	OpenFlowCacheManager = &OpenFlowManager{
		flowCacheMap: syncmap.NewSyncMap[*bridgeOpenflowCache](),
		wg:           wg,
		stopChan:     stopChan,
	}
}

// "/" is not a valid character in the bridge name, use it as the separator
func generateID(bridge string) string {
	return fmt.Sprintf("%s/%s", bridge, uuid.New())
}

func getBridgeFromID(id string) string {
	a := strings.Split(id, "/")
	return a[0]
}

// CreateFlowCache creaste flow cache for the specified bridge, return unique id, which will be used
// to update/delete flows.
func (ofcm *OpenFlowManager) CreateFlowCache(bridgeName string) (id string) {
	id = generateID(bridgeName)
	_ = ofcm.flowCacheMap.DoWithLock(bridgeName, func(bridge string) error {
		bridgeFlowCache, found := ofcm.flowCacheMap.LoadOrStore(bridge, &bridgeOpenflowCache{
			bridge:    bridge,
			flowCache: map[string]map[string][]string{id: make(map[string][]string)},
			flowChan:  make(chan struct{}, 1),
			stopChan:  make(chan struct{}),
		})
		bridgeFlowCache.Lock()
		if found {
			_, ok := bridgeFlowCache.flowCache[id]
			if !ok {
				bridgeFlowCache.flowCache[id] = make(map[string][]string)
			}
		}
		bridgeFlowCache.Unlock()
		return nil
	})
	return
}

func (ofcm *OpenFlowManager) StartFlowCacheWorker(id string) {
	bridgeName := getBridgeFromID(id)
	_ = ofcm.flowCacheMap.DoWithLock(bridgeName, func(bridge string) error {
		bridgeFlowCache, found := ofcm.flowCacheMap.Load(bridge)
		if found && !bridgeFlowCache.isRunning {
			bridgeFlowCache.isRunning = true
			bridgeFlowCache.run(ofcm.wg, ofcm.stopChan)
		}
		return nil
	})
}

func (ofcm *OpenFlowManager) SyncFlows(id string) {
	bridgeName := getBridgeFromID(id)
	_ = ofcm.flowCacheMap.DoWithLock(bridgeName, func(bridge string) error {
		bridgeFlowCache, found := ofcm.flowCacheMap.Load(bridge)
		if found {
			bridgeFlowCache.syncFlows()
		}
		return nil
	})
}

func (ofcm *OpenFlowManager) DeleteCache(id string) {
	bridgeName := getBridgeFromID(id)
	_ = ofcm.flowCacheMap.DoWithLock(bridgeName, func(bridge string) error {
		bridgeFlowCache, found := ofcm.flowCacheMap.Load(bridge)
		if !found {
			return nil
		}
		bridgeFlowCache.Lock()
		_, ok := bridgeFlowCache.flowCache[id]
		if ok {
			delete(bridgeFlowCache.flowCache, id)
			bridgeFlowCache.requestFlowSync()
		}
		if len(bridgeFlowCache.flowCache) == 0 {
			close(bridgeFlowCache.stopChan)
			ofcm.flowCacheMap.Delete(bridge)
		}
		bridgeFlowCache.Unlock()
		return nil
	})
}

func (ofcm *OpenFlowManager) UpdateFlowCacheEntry(id string, key string, flows []string, syncFlow bool) {
	bridgeName := getBridgeFromID(id)
	_ = ofcm.flowCacheMap.DoWithLock(bridgeName, func(bridge string) error {
		bridgeFlowCache, loaded := ofcm.flowCacheMap.Load(bridge)
		if loaded {
			bridgeFlowCache.Lock()
			flowCache, ok := bridgeFlowCache.flowCache[id]
			if ok {
				flowCache[key] = flows
				if syncFlow {
					bridgeFlowCache.requestFlowSync()
				}
			}
			bridgeFlowCache.Unlock()
		}
		return nil
	})
}

func (ofcm *OpenFlowManager) DeleteFlowsByKey(id, key string, syncFlow bool) {
	bridgeName := getBridgeFromID(id)
	_ = ofcm.flowCacheMap.DoWithLock(bridgeName, func(bridge string) error {
		bridgeFlowCache, loaded := ofcm.flowCacheMap.Load(bridge)
		if loaded {
			bridgeFlowCache.Lock()
			if flowCache, ok := bridgeFlowCache.flowCache[id]; ok {
				if _, ok = flowCache[key]; ok {
					delete(flowCache, key)
					if syncFlow {
						bridgeFlowCache.requestFlowSync()
					}
				}
			}
			bridgeFlowCache.Unlock()
		}
		return nil
	})
}

// GetFlowsByKey get flows - used in test only
func (ofcm *OpenFlowManager) GetFlowsByKey(id, key string) (flows []string) {
	bridgeName := getBridgeFromID(id)
	_ = ofcm.flowCacheMap.DoWithLock(bridgeName, func(bridge string) error {
		bridgeFlowCache, loaded := ofcm.flowCacheMap.Load(bridge)
		if loaded {
			bridgeFlowCache.Lock()
			if flowCache, ok := bridgeFlowCache.flowCache[id]; ok {
				flows = flowCache[key]
			}
			bridgeFlowCache.Unlock()
		}
		return nil
	})
	return
}

func (ofcm *OpenFlowManager) RequestFlowSync(id string) {
	bridgeName := getBridgeFromID(id)
	_ = ofcm.flowCacheMap.DoWithLock(bridgeName, func(bridge string) error {
		bridgeFlowCache, loaded := ofcm.flowCacheMap.Load(bridge)
		if loaded {
			bridgeFlowCache.Lock()
			if _, ok := bridgeFlowCache.flowCache[id]; ok {
				bridgeFlowCache.requestFlowSync()
			}
			bridgeFlowCache.Unlock()
		}
		return nil
	})
}

func (c *bridgeOpenflowCache) requestFlowSync() {
	select {
	case c.flowChan <- struct{}{}:
		klog.V(5).Infof("Gateway OpenFlow sync requested")
	default:
		klog.V(5).Infof("Gateway OpenFlow sync already requested")
	}
}

func (c *bridgeOpenflowCache) syncFlows() {
	c.Lock()
	defer c.Unlock()
	flows := []string{}
	for _, flowCache := range c.flowCache {
		for _, entry := range flowCache {
			flows = append(flows, entry...)
		}
	}

	_, stderr, err := util.ReplaceOFFlows(c.bridge, flows)
	if err != nil {
		klog.Errorf("Failed to add flows, error: %v, stderr, %s, flows: %s", err, stderr, c.flowCache)
	}
}

// checkDefaultOpenFlow checks for the existence of default OpenFlow rules and
// exits if the output is not as expected
func (c *bridgeOpenflowCache) run(wg *sync.WaitGroup, stopChan <-chan struct{}) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		syncPeriod := 15 * time.Second
		timer := time.NewTicker(syncPeriod)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				c.syncFlows()
			case <-c.flowChan:
				c.syncFlows()
				timer.Reset(syncPeriod)
			case <-stopChan:
				return
			case <-c.stopChan:
				return
			}
		}
	}()
}
