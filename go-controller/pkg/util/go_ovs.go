package util

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/ebay/libovsdb"
	"k8s.io/klog"
)

const (
	ovsDBSocketPath  string = "unix:/var/run/openvswitch/db.sock"
	ovsDB            string = "Open_vSwitch"
	TableBridge      string = "Bridge"
	TablePort        string = "Port"
	TableInterface   string = "Interface"
	TableOpenVswitch string = "Open_vSwitch"
)

type OvsdbConfig struct {
	TableCols map[string][]string
	Reconnect bool
}

type OvsdbClient struct {
	client     *libovsdb.OvsdbClient
	cache      map[string]map[string]libovsdb.Row
	cacheMutex sync.RWMutex
	tableCols  map[string][]string
	reconn     bool
}

type ovsNotifier struct {
	client *OvsdbClient
}

func newOvsNotifier(c *OvsdbClient) *ovsNotifier {
	return &ovsNotifier{
		client: c,
	}
}

func (n ovsNotifier) Update(context interface{}, tableUpdates libovsdb.TableUpdates) {
	n.client.populateCache(tableUpdates)
}

func (n ovsNotifier) Locked([]interface{}) {
}

func (n ovsNotifier) Stolen([]interface{}) {
}

func (n ovsNotifier) Echo([]interface{}) {
}

func (n ovsNotifier) Disconnected(c *libovsdb.OvsdbClient) {
	if n.client.reconn {
		n.client.reconnect()
	}
}

// OVSDB (Bridge, Port, Interface, Open_Vswitch) Tables structure
type Bridge struct {
	UUID  string
	Name  string
	Ports []string
}

type Port struct {
	UUID       string
	Name       string
	Interfaces []string
}

type Interface struct {
	UUID                 string
	Name                 string
	Duplex               string
	Type                 string
	AdminState           string
	LinkState            string
	IfIndex              float64
	LinkResets           float64
	LinkSpeed            float64
	Mtu                  float64
	OfPort               float64
	IngressPolicingBurst float64
	IngressPolicingRate  float64
	Statistics           map[string]float64
	Status               map[string]string
}

type OpenVswitch struct {
	UUID        string
	OtherConfig map[string]string
	ExternalIds map[string]string
}

func (c *OvsdbClient) GetOvsBridgeTable() ([]*Bridge, error) {
	c.cacheMutex.RLock()
	defer c.cacheMutex.RUnlock()

	cacheBridge, ok := c.cache[TableBridge]

	if !ok {
		return nil, fmt.Errorf("failed to get the bridge table")
	}

	listBridge := make([]*Bridge, 0, len(cacheBridge))
	for uuid := range cacheBridge {
		br, err := c.rowToBridge(uuid)
		if err != nil {
			return nil, err
		}
		listBridge = append(listBridge, br)
	}
	return listBridge, nil
}

func (c *OvsdbClient) rowToBridge(uuid string) (*Bridge, error) {
	cacheBridge, ok := c.cache[TableBridge][uuid]
	if !ok {
		return nil, fmt.Errorf("row with uuid %s not found in bridge table", uuid)
	}
	br := &Bridge{
		UUID: uuid,
		Name: cacheBridge.Fields["name"].(string),
	}

	var ports []string
	if portInfo, ok := cacheBridge.Fields["ports"]; ok {
		switch port := portInfo.(type) {
		case libovsdb.UUID:
			ports = append(ports, port.GoUUID)
		case libovsdb.OvsSet:
			for _, p := range port.GoSet {
				if puid, ok := p.(libovsdb.UUID); ok {
					ports = append(ports, puid.GoUUID)
				}
			}
		}
	}
	br.Ports = ports
	return br, nil
}

func (c *OvsdbClient) GetOvsPortTable() ([]*Port, error) {
	c.cacheMutex.RLock()
	defer c.cacheMutex.RUnlock()

	cachePort, ok := c.cache[TablePort]

	if !ok {
		return nil, fmt.Errorf("failed to get the port table")
	}

	listPort := make([]*Port, 0, len(cachePort))
	for uuid := range cachePort {
		po, err := c.rowToPort(uuid)
		if err != nil {
			return nil, err
		}
		listPort = append(listPort, po)
	}
	return listPort, nil
}

func (c *OvsdbClient) rowToPort(uuid string) (*Port, error) {
	cachePort, ok := c.cache[TablePort][uuid]
	if !ok {
		return nil, fmt.Errorf("row with uuid %s not found in port table", uuid)
	}
	po := &Port{
		UUID: uuid,
		Name: cachePort.Fields["name"].(string),
	}

	var interfaces []string
	if interfaceInfo, ok := cachePort.Fields["interfaces"]; ok {
		switch in := interfaceInfo.(type) {
		case libovsdb.UUID:
			interfaces = append(interfaces, in.GoUUID)
		case libovsdb.OvsSet:
			for _, i := range in.GoSet {
				if interfaceID, ok := i.(libovsdb.UUID); ok {
					interfaces = append(interfaces, interfaceID.GoUUID)
				}
			}
		}
	}
	po.Interfaces = interfaces
	return po, nil
}

func (c *OvsdbClient) GetOvsInterfaceTable() ([]*Interface, error) {
	c.cacheMutex.RLock()
	defer c.cacheMutex.RUnlock()

	cacheInterface, ok := c.cache[TableInterface]

	if !ok {
		return nil, fmt.Errorf("failed to get the interface table")
	}

	listInterface := make([]*Interface, 0, len(cacheInterface))
	for uuid := range cacheInterface {
		in, err := c.rowToInterface(uuid)
		if err != nil {
			return nil, err
		}
		listInterface = append(listInterface, in)
	}
	return listInterface, nil
}

func (c *OvsdbClient) rowToInterface(uuid string) (*Interface, error) {
	cacheInterface, ok := c.cache[TableInterface][uuid]
	if !ok {
		return nil, fmt.Errorf("row with %s uuid not found in interface table", uuid)
	}

	interfaceInfo := &Interface{
		UUID:                 uuid,
		Name:                 cacheInterface.Fields["name"].(string),
		Type:                 cacheInterface.Fields["type"].(string),
		AdminState:           cacheInterface.Fields["admin_state"].(string),
		LinkState:            cacheInterface.Fields["link_state"].(string),
		IfIndex:              cacheInterface.Fields["ifindex"].(float64),
		LinkResets:           cacheInterface.Fields["link_resets"].(float64),
		OfPort:               cacheInterface.Fields["ofport"].(float64),
		IngressPolicingBurst: cacheInterface.Fields["ingress_policing_burst"].(float64),
		IngressPolicingRate:  cacheInterface.Fields["ingress_policing_rate"].(float64),
	}
	interfaceInfo.Duplex = getColumnFieldStringValue(&cacheInterface, "duplex")
	interfaceInfo.LinkSpeed = getColumnFieldFloat64Value(&cacheInterface, "link_speed")
	interfaceInfo.Mtu = getColumnFieldFloat64Value(&cacheInterface, "mtu")

	statsMap := make(map[string]float64)
	if stats, ok := cacheInterface.Fields["statistics"]; ok {
		if stMap, ok := stats.(libovsdb.OvsMap); ok {
			for field, value := range stMap.GoMap {
				if fi, ok := field.(string); ok {
					if v, ok := value.(float64); ok {
						statsMap[fi] = v
					}
				}
			}
		} else {
			return nil, fmt.Errorf("type libovsdb.OvsMap casting failed")
		}
	}
	interfaceInfo.Statistics = statsMap

	statusMap := make(map[string]string)
	if status, ok := cacheInterface.Fields["status"]; ok {
		if sMap, ok := status.(libovsdb.OvsMap); ok {
			for field, value := range sMap.GoMap {
				if fi, ok := field.(string); ok {
					if v, ok := value.(string); ok {
						statusMap[fi] = v
					}
				}
			}
		} else {
			return nil, fmt.Errorf("type libovsdb.OvsMap casting failed")
		}
	}
	interfaceInfo.Status = statusMap
	return interfaceInfo, nil
}

func (c *OvsdbClient) GetOvsOpenVswitchTable() ([]*OpenVswitch, error) {
	c.cacheMutex.RLock()
	defer c.cacheMutex.RUnlock()

	cacheOpenVswitch, ok := c.cache[TableOpenVswitch]

	if !ok {
		return nil, fmt.Errorf("failed to get the openvswitch table")
	}

	listOpenVswitch := make([]*OpenVswitch, 0, len(cacheOpenVswitch))
	for uuid := range cacheOpenVswitch {
		op, err := c.rowToOpenVswitch(uuid)
		if err != nil {
			return nil, err
		}
		listOpenVswitch = append(listOpenVswitch, op)
	}
	return listOpenVswitch, nil
}

func (c *OvsdbClient) rowToOpenVswitch(uuid string) (*OpenVswitch, error) {
	cacheOpenVswitch, ok := c.cache[TableOpenVswitch][uuid]
	if !ok {
		return nil, fmt.Errorf("row with %s uuid not found in openvswitch table", uuid)
	}

	openVswitchTable := &OpenVswitch{UUID: uuid}
	externalIdsMap := make(map[string]string)
	if externalIds, ok := cacheOpenVswitch.Fields["external_ids"]; ok {
		if extIdMap, ok := externalIds.(libovsdb.OvsMap); ok {
			for field, value := range extIdMap.GoMap {
				if fi, ok := field.(string); ok {
					if v, ok := value.(string); ok {
						externalIdsMap[fi] = v
					}
				}
			}
		} else {
			return nil, fmt.Errorf("type libovsdb.OvsMap casting failed")
		}
	}
	openVswitchTable.ExternalIds = externalIdsMap

	otherConfigMap := make(map[string]string)
	if otherConfig, ok := cacheOpenVswitch.Fields["other_config"]; ok {
		if ocMap, ok := otherConfig.(libovsdb.OvsMap); ok {
			for field, value := range ocMap.GoMap {
				if fi, ok := field.(string); ok {
					if v, ok := value.(string); ok {
						otherConfigMap[fi] = v
					}
				}
			}
		} else {
			return nil, fmt.Errorf("type libovsdb.OvsMap casting failed")
		}
	}
	openVswitchTable.OtherConfig = otherConfigMap
	return openVswitchTable, nil
}

func getColumnFieldStringValue(row *libovsdb.Row, col string) string {
	var value string

	if fv, ok := row.Fields[col]; ok {
		switch fieldValue := fv.(type) {
		case string:
			value = fieldValue
		case libovsdb.OvsSet:
			if len(fieldValue.GoSet) > 0 {
				value = fieldValue.GoSet[0].(string)
			}
		}
	}
	return value
}

func getColumnFieldFloat64Value(row *libovsdb.Row, col string) float64 {
	var value float64

	if fv, ok := row.Fields[col]; ok {
		switch fieldValue := fv.(type) {
		case float64:
			value = fieldValue
		case libovsdb.OvsSet:
			if len(fieldValue.GoSet) > 0 {
				switch fv := fieldValue.GoSet[0].(type) {
				case float64:
					value = fv
				case int64:
					value = float64(fv)
				}
			}
		}
	}
	return value
}

func (c *OvsdbClient) populateCache(updates libovsdb.TableUpdates) {
	c.cacheMutex.Lock()
	defer c.cacheMutex.Unlock()

	empty := libovsdb.Row{}
	for table, tableUpdate := range updates.Updates {
		if _, ok := c.cache[table]; !ok {
			c.cache[table] = make(map[string]libovsdb.Row)
		}

		for uuid, row := range tableUpdate.Rows {
			if !reflect.DeepEqual(row.New, empty) {
				c.cache[table][uuid] = row.New
			} else {
				delete(c.cache[table], uuid)
			}
		}
	}
}

func (c *OvsdbClient) connect() (err error) {
	ovsdb, err := libovsdb.Connect(ovsDBSocketPath, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to ovsdb :(%v)", err)
	}

	c.client = ovsdb
	defer func() {
		if err != nil {
			c.client.Disconnect()
			c.client = nil
		}
	}()

	intialUpdates, err := c.monitorOvsdb("")
	if err != nil {
		return fmt.Errorf("failed to start monitoring of ovsdb :(%v)", err)
	}
	// send the intial updates
	c.populateCache(*intialUpdates)
	n := newOvsNotifier(c)
	ovsdb.Register(n)
	return nil
}

func (c *OvsdbClient) monitorOvsdb(jsonContext interface{}) (*libovsdb.TableUpdates, error) {
	if len(c.tableCols) != 0 {
		ovsdbSchemaTables := make(map[string]bool)
		dbSchema := c.client.Schema[ovsDB]
		for table := range dbSchema.Tables {
			ovsdbSchemaTables[table] = true
		}
		// check if given columns exits in the
		// ovsdb schema
		for name := range c.tableCols {
			if _, ok := ovsdbSchemaTables[name]; !ok {
				return nil, fmt.Errorf("%s table doesn't exist in ovsdb schema", name)
			}
		}
		requests := make(map[string]libovsdb.MonitorRequest)
		for table, columns := range c.tableCols {
			requests[table] = libovsdb.MonitorRequest{
				Columns: columns,
				Select: libovsdb.MonitorSelect{
					Initial: true,
					Insert:  true,
					Delete:  true,
					Modify:  true,
				},
			}
		}
		return c.client.Monitor("Open_vSwitch", jsonContext, requests)
	} else {
		return nil, fmt.Errorf("mention list of ovsdb tables to monitor")
	}
}

func NewOvsDbClient(cfg *OvsdbConfig) (*OvsdbClient, error) {
	client := &OvsdbClient{
		cache:     make(map[string]map[string]libovsdb.Row),
		tableCols: cfg.TableCols,
		reconn:    cfg.Reconnect,
	}

	err := client.connect()
	if err != nil {
		return nil, err
	}
	return client, nil
}

func (c *OvsdbClient) reconnect() {
	ticker := time.NewTicker(500 * time.Millisecond)
	go func() {
		klog.Infof("OVSDB client disconnected. Reconnecting ... \n")
		retry := 0
		for range ticker.C {
			if err := c.connect(); err != nil {
				if retry < 10 {
					klog.Infof("OVSDB reconnect failed (%v). Retry...\n", err)
				} else if retry == 10 {
					klog.Infof("OVSDB reconnect failed (%v). "+
						"continue retrying but log will be supressed.\n", err)
				}
				retry++
				continue
			}
			klog.Infof("OVSDB client reconnected.\n")
			return
		}
	}()
}
