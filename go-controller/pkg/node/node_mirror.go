package node

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	portmirror "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/portmirror/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

type portMirrorRetryRequestDPU struct {
	pm  *util.PortMirror
	pod *kapi.Pod
}

func (n *OvnNode) syncPortMirrors(portMirrors []interface{}) error {
	// if the dpu is coming back from a reboot; all the ovs ports will be "no device found"
	// since the corresponding sf won't have been created; delete the ports so that we can
	// create the sfs when handling the pods. Such ovs ports will have ofport as -1

	stdout, stderr, err := util.RunOVSVsctl("--no-headings", "--data=bare", "--format=csv",
		"--columns=name", "find", "Interface", "external-ids:port-mirrored=true", "ofport=-1")
	if err != nil {
		klog.Errorf("Failed to get mirrored OVS ports that are not associated with any SF: %v (%q)", err, stderr)
	} else if stdout != "" {
		// Delete these ports
		staleSFPorts := strings.Split(stdout, "\n")
		for i := range staleSFPorts {
			_, stderr, err := util.RunOVSVsctl("del-port", "br-int", staleSFPorts[i])
			if err != nil {
				klog.Errorf("Failed to delete %s from br-int: (%v) (%q)", staleSFPorts[i], err, stderr)
				// Remove the mirror:id external-id so that it can be reused.
				clearExtIdCmd := []string{
					"remove", "interface", staleSFPorts[i], "external_ids", "mirror-id",
					"external_ids", "port-mirrored",
				}
				_, stderr, err := util.RunOVSVsctl(clearExtIdCmd...)
				if err != nil {
					klog.Errorf("Failed to clear external_ids:mirror-id from %s: (%v) (%q)", staleSFPorts[i], err, stderr)
				}
			} else {
				klog.Infof("Deleted OVS port %s that doesn't have an SF device", staleSFPorts[i])
			}
		}
	}

	for _, portMirror := range portMirrors {
		pm, ok := portMirror.(*portmirror.PortMirror)
		if !ok {
			klog.Errorf("Spurious object in portmirrors: %v", portMirror)
			continue
		}

		portMirrorID := util.GetPortMirrorID(pm)

		// no explicit locking is required to update SF details struct, as all
		// operations accessing the struct are serialized.
		// check whether any SF's are associated with this portmirror
		portMirrorKey := util.GetNamespacedName(pm.Namespace, pm.Name)
		s, ok := n.portMirrorIDToSFMap.Load(portMirrorID)
		if ok {
			sfInfo := s.(*sfDetails)
			sfInfo.mirrorCount++
			sfInfo.portMirrorInUse[portMirrorKey] = true
			continue
		}

		// currently, no SF's mirrorID has been associated or populated with this portMirrorID.
		stdout, stderr, err := util.RunOVSVsctl("--no-headings", "--data=bare", "--format=csv",
			"--columns=name", "find", "Interface", "external-ids:mirror-id="+portMirrorID)
		if err != nil {
			klog.Errorf("Failed to get Interface with mirror-id %s :%v (%q)", portMirrorID, err, stderr)
			continue
		}

		if stdout == "" {
			continue
		}

		names := strings.Split(stdout, "\n")
		if len(names) > 1 {
			klog.Errorf("PortMirrorID %s has been associated with multiple SF Interfaces: %v", portMirrorID, names)
			continue
		}

		sfInfo := &sfDetails{
			portMirrorInUse: make(map[string]bool),
			sfRepName:       names[0],
		}
		// get SF external_id info and populate SF details
		stdout, stderr, err = util.RunOVSVsctl("--no-headings", "--data=bare", "--format=csv",
			"--columns=external_ids", "list", "Interface", names[0])
		if err != nil {
			klog.Errorf("Failed to get interface %s external_ids info :%v (%q)", names[0], err, stderr)
			continue
		}

		for _, kvPair := range strings.Fields(stdout) {
			elem := strings.Split(kvPair, "=")
			if len(elem) != 2 {
				continue
			}
			switch elem[0] {
			case "sf-netdevicename":
				sfInfo.sfNetDeviceName = elem[1]
			case "sf-uplinkport":
				sfInfo.uplinkPhysPort = elem[1]
			case "sf-portindex":
				pIndex, err := strconv.ParseUint(elem[1], 10, 32)
				if err != nil {
					klog.Errorf("Failed to parse portIndex %s :(%v)", elem[1], err)
				} else {
					sfInfo.portIndex = uint32(pIndex)
				}
			case "sf-number":
				sfNumber, err := strconv.ParseUint(elem[1], 10, 32)
				if err != nil {
					klog.Errorf("Failed to parse sfNum %s :(%v)", elem[1], err)
				} else {
					sfInfo.sfNum = uint32(sfNumber)
				}
			case "mirror-to-pod":
				sfInfo.mirroredToPod = elem[1]
			}
		}

		klog.Infof("The sf details are %+v", *sfInfo)

		sfInfo.mirrorCount++
		// store the portmirrorkey in portMirrorInUse map
		sfInfo.portMirrorInUse[portMirrorKey] = true
		// update SFMap to mark this SFnum as in use
		n.setSFPortNum(sfInfo.sfNum)
		// store the sfInfo
		n.portMirrorIDToSFMap.Store(portMirrorID, sfInfo)
	}
	return nil
}

// watchPortMirrorDPU starts watching for port mirror resource and for any target (sink) pod
// on the DPU, configures mirroring
func (n *OvnNode) watchPortMirrorDPU() error {
	start := time.Now()
	n.portMirrorRetryQueueDPU = workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "portMirror")
	_, err := n.watchFactory.AddPortMirrorHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			portmirror := obj.(*portmirror.PortMirror)
			err := n.addDPUPortMirror(portmirror)
			if err != nil {
				klog.Errorf(err.Error())
			}
		},
		UpdateFunc: func(old, new interface{}) {
			newPortMirror := new.(*portmirror.PortMirror)
			oldPortMirror := old.(*portmirror.PortMirror)
			// Check the changes in the spec; we could check for the sink details specifically.
			if !reflect.DeepEqual(oldPortMirror.Spec, newPortMirror.Spec) {
				if err := n.deleteDPUPortMirror(oldPortMirror); err != nil {
					klog.Errorf(err.Error())
				}
				if err := n.addDPUPortMirror(newPortMirror); err != nil {
					klog.Errorf(err.Error())
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			portmirror := obj.(*portmirror.PortMirror)
			err := n.deleteDPUPortMirror(portmirror)
			if err != nil {
				klog.Errorf(err.Error())
			}
		},
	}, n.syncPortMirrors)
	if err != nil {
		return err
	}
	// for closing the portmirror queue
	go func() {
		ticker := time.NewTicker(types.PortMirrorResyncInterval)
		for {
			select {
			case <-ticker.C:
				n.syncPortMirrorsDPUPeriodic()
			case <-n.stopChan:
				ticker.Stop()
				n.portMirrorRetryQueueDPU.ShutDown()
				return
			}
		}
	}()

	// for portmirror retry dpu operations
	go func() {
		for n.retryPortMirrorOperationsDPU() {
		}
	}()

	klog.Infof("Bootstrapping portmirror took %v", time.Since(start))
	return nil
}

func (n *OvnNode) retryOnAddFailure(sfInfo *sfDetails, errMsg, key string, pm *util.PortMirror, pod *kapi.Pod) {
	// delete sf
	if sfInfo != nil {
		err := n.deleteSF(sfInfo.uplinkPhysPort, sfInfo.portIndex, sfInfo.sfNum)
		if err != nil {
			klog.Errorf("Failed to delete SF port %s :(%v)", sfInfo.sfNetDeviceName, err)
		}
	}
	util.UpdatePortMirrorStatusOnError(pm, n.Kube, errMsg, key)
	n.requeueSinkPodAddForPortMirror(pm, pod)
}

// handlePortMirrorSinkPodAdd creates an SF for portmirror if there are no SF's
// associated with mirrorID and then adds the sfRep to br-int.
func (n *OvnNode) handlePortMirrorSinkPodAdd(pm *util.PortMirror, pod *kapi.Pod) error {
	klog.Infof("Adding mirror rules for sink pod %s/%s under portmirror %s/%s", pod.Namespace, pod.Name, pm.Namespace, pm.Name)

	// for now, sink pod must be a hostnetwork pod.
	if !pod.Spec.HostNetwork {
		klog.Infof("%s/%s not host-network pod..skipping", pod.Namespace, pod.Name)
		return nil
	}

	portMirrorIDUnlock := util.GetLockByPMId(pm.SinkLocalDetails.PortMirrorId)
	defer portMirrorIDUnlock()

	// sinkPodKeyForFailedOps is used as key for FailedPortMirrorOps for sinkpod ops
	sinkPodKeyForFailedOps := util.GetPortMirrorSinkPodKeyForFailedOps(pod)
	// clear any error messages for this portmirror,
	// if there are any during previous iteration
	if _, ok := pm.FailedPortMirrorOps[sinkPodKeyForFailedOps]; ok {
		util.ClearPortMirrorErrorMessage(pm, n.Kube, sinkPodKeyForFailedOps)
	}

	// XXX-Check if an OVS port already exists for this portMirrorId.
	// If it does, increase the mirrorCount and update portMirrorInUse map
	portMirrorKey := util.GetNamespacedName(pm.Namespace, pm.Name)
	var sfInfo *sfDetails
	s, ok := n.portMirrorIDToSFMap.Load(pm.SinkLocalDetails.PortMirrorId)
	if ok {
		sfInfo = s.(*sfDetails)
		klog.Infof("Found SF %s for portmirror %s/%s associated with portMirrorID %s",
			sfInfo.sfNetDeviceName, pm.Namespace, pm.Name, pm.SinkLocalDetails.PortMirrorId)

		if _, ok := sfInfo.portMirrorInUse[portMirrorKey]; !ok {
			// increase the mirrorCount used by portMirrorID
			sfInfo.mirrorCount++
			// store the portmirrorkey in portMirrorInUse map
			sfInfo.portMirrorInUse[portMirrorKey] = true
		}

		// update the status to success
		updateErr := util.UpdatePortMirrorStatusWithRetry(n.Kube, pm.Namespace, pm.Name, types.OvnK8sStatusSucceeded, "", "")
		if updateErr != nil {
			klog.Errorf("Failed to update portmirror %s/%s status to success state :(%v)", pm.Namespace, pm.Name, updateErr)
		}
		return nil
	}

	// currently no SF is mapped to this mirrorID, create a new one

	// Get the uplink from the management port - slightly hackish way of doing this; we could
	// get this from the shared gw, but that could be a bond, so we might not get the actual
	// uplink port. Going forward, we could use the rep of the src pod interface being mirrored
	// Or once we have multi-port eswitch we can always use a designated uplink port.

	physUplinkPort, physUplinkPortNum, err := getUplinkPhysPortInfo(types.K8sMgmtIntfName)
	if err != nil {
		// retry
		errMsg := fmt.Sprintf("%s - Failed to get %s uplink port info for %s on DPU %s",
			util.MessagePrefixPortMirrorErr, types.K8sMgmtIntfName, sinkPodKeyForFailedOps, n.dpuName)
		n.retryOnAddFailure(nil, errMsg, sinkPodKeyForFailedOps, pm, pod)
		return fmt.Errorf("failed to get uplink port info for %s on DPU %s for sink pod %s to create sf: (%v)",
			types.K8sMgmtIntfName, n.dpuName, util.GetNamespacedName(pod.Namespace, pod.Name), err)
	}
	sfInfo, err = n.getSFInfo(physUplinkPort, physUplinkPortNum, pm.SinkLocalDetails.SinkSFNum)
	if err != nil {
		// retry
		errMsg := fmt.Sprintf("%s - Failed to create SF for %s on DPU %s", util.MessagePrefixPortMirrorErr,
			sinkPodKeyForFailedOps, n.dpuName)
		n.retryOnAddFailure(nil, errMsg, sinkPodKeyForFailedOps, pm, pod)
		return fmt.Errorf("failed to create a sf on DPU %s for sink pod %s: (%v)",
			n.dpuName, util.GetNamespacedName(pod.Namespace, pod.Name), err)
	}
	sfInfo.uplinkPhysPort = physUplinkPort
	// increase the mirrorCount used by portMirrorID
	sfInfo.mirrorCount++
	// store the portmirrorkey in portMirrorInUse map
	sfInfo.portMirrorInUse[portMirrorKey] = true
	sfInfo.mirroredToPod = util.GetNamespacedName(pod.Namespace, pod.Name)
	klog.Infof("Created a new SF %s for portmirror %s/%s", sfInfo.sfNetDeviceName, pm.Namespace, pm.Name)

	// on failure we'll delete the SF and let it be recreated in retry; just so that SFs are not
	// held on failures (at the expense of creating SF on retry)

	// rename the sf to the InterfaceName mentioned in the portmirror spec.
	if pm.SinkLocalDetails.SinkIfName != "" {
		err := util.RenameLink(sfInfo.sfNetDeviceName, pm.SinkLocalDetails.SinkIfName)
		// retry
		if err != nil {
			errMsg := fmt.Sprintf("%s - Failed to rename sf netdevice for %s on DPU %s",
				util.MessagePrefixPortMirrorErr, sinkPodKeyForFailedOps, n.dpuName)
			n.retryOnAddFailure(sfInfo, errMsg, sinkPodKeyForFailedOps, pm, pod)
			return fmt.Errorf("failed to rename link name from %s to %s :(%v)",
				sfInfo.sfNetDeviceName, pm.SinkLocalDetails.SinkIfName, err)
		}
	}

	var sinkNetName string
	if pm.SinkLocalDetails.SinkIfName != "" {
		sinkNetName = pm.SinkLocalDetails.SinkIfName
	} else {
		sinkNetName = sfInfo.sfNetDeviceName
	}

	externalIDsMirrorToPod := fmt.Sprintf("%s/%s:%s", pod.Namespace, pod.Name, sinkNetName)
	sfInterfaceCmd := []string{
		"--may-exist", "add-port", "br-int", sfInfo.sfRepName, "--", "set", "Interface",
		sfInfo.sfRepName,
		fmt.Sprintf("external-ids:mirror-id=%s", pm.SinkLocalDetails.PortMirrorId),
		fmt.Sprintf("external-ids:sf-number=%d", sfInfo.sfNum),
		fmt.Sprintf("external-ids:sf-netdevicename=%s", sfInfo.sfNetDeviceName),
		fmt.Sprintf("external-ids:sf-uplinkport=%s", sfInfo.uplinkPhysPort),
		fmt.Sprintf("external-ids:sf-portindex=%d", sfInfo.portIndex),
		fmt.Sprintf("external-ids:mirror-to-pod=%s", externalIDsMirrorToPod),
		"external-ids:port-mirrored=true",
	}

	// add sf-rep to br-int and set external-ids
	_, stderr, err := util.RunOVSVsctl(sfInterfaceCmd...)
	if err != nil {
		errMsg := fmt.Sprintf("%s - Failed to add SfRep %s to br-int for %s on DPU %s",
			util.MessagePrefixPortMirrorErr, sfInfo.sfRepName, sinkPodKeyForFailedOps, n.dpuName)
		n.retryOnAddFailure(sfInfo, errMsg, sinkPodKeyForFailedOps, pm, pod)
		return fmt.Errorf("failed to add %s sfrep to br-int for sink pod %s: (%v) (%q)",
			sfInfo.sfRepName, util.GetNamespacedName(pod.Namespace, pod.Name), err, stderr)
	}

	// We don't set no-flood on the port assuming OVN doesn't add flood rules, but if that
	// is not the case we need to make sure this is set so that the mirror port doesn't get
	// packets other than from/to the port being mirrored.

	n.portMirrorIDToSFMap.Store(pm.SinkLocalDetails.PortMirrorId, sfInfo)
	// update the status to success
	updateErr := util.UpdatePortMirrorStatusWithRetry(n.Kube, pm.Namespace, pm.Name, types.OvnK8sStatusSucceeded, "", "")
	if updateErr != nil {
		klog.Errorf("Failed to update portmirror %s/%s status to success state :(%v)", pm.Namespace, pm.Name, updateErr)
	}
	return nil
}

func (n *OvnNode) handlePortMirrorSinkPodDelete(pm *util.PortMirror, pod *kapi.Pod) error {
	klog.Infof("Deleting mirror rules for sink pod %s/%s under portmirror %s/%s", pod.Namespace, pod.Name, pm.Namespace, pm.Name)
	// We wouldn't have configured any for these
	if !pod.Spec.HostNetwork {
		klog.Infof("Pod %s/%s not host-network pod or DPU not primary .. skipping", pod.Namespace, pod.Name)
		return nil
	}
	portMirrorIDUnlock := util.GetLockByPMId(pm.SinkLocalDetails.PortMirrorId)
	defer portMirrorIDUnlock()

	// clear any error message if something went wrong while sinkPod addition.
	sinkPodKeyForFailedOps := util.GetPortMirrorSinkPodKeyForFailedOps(pod)
	if _, ok := pm.FailedPortMirrorOps[sinkPodKeyForFailedOps]; ok {
		// using sinkPodKeyForFailedOps as key.
		util.ClearPortMirrorErrorMessage(pm, n.Kube, sinkPodKeyForFailedOps)
	}

	// get the sfInfo details
	s, ok := n.portMirrorIDToSFMap.Load(pm.SinkLocalDetails.PortMirrorId)
	if !ok {
		klog.V(5).Infof("SF for portmirror %s/%s is already deleted", pm.Namespace, pm.Name)
		return nil
	}
	sfInfo := s.(*sfDetails)

	// if this SF has not been created by this sinkPod, then don't do any SF related operations
	if sfInfo.mirroredToPod != util.GetNamespacedName(pod.Namespace, pod.Name) {
		klog.Infof("SF %s is not mirrored to sink pod %s, so not performing any SF operations",
			sfInfo.sfNetDeviceName, util.GetNamespacedName(pod.Namespace, pod.Name))
		return nil
	}

	portMirrorKey := util.GetNamespacedName(pm.Namespace, pm.Name)
	_, ok = sfInfo.portMirrorInUse[portMirrorKey]
	if !ok {
		// portMirrorKey has already been deleted so no need to
		// do any operations on the SF side.
		klog.Infof("Mirror key already deleted for portmirror %s/%s", pm.Namespace, pm.Name)
		return nil
	}
	sfInfo.mirrorCount--
	delete(sfInfo.portMirrorInUse, portMirrorKey)

	// if the mirorrCount is 0, then this is the last
	// portmirrorID associated with the sf, delete SF.
	if sfInfo.mirrorCount == 0 {
		// delete the portmirrorID key from portMirrorIDToSFMap map
		n.portMirrorIDToSFMap.Delete(pm.SinkLocalDetails.PortMirrorId)
		err := n.unConfigureSFInterface(sfInfo)
		if err != nil {
			return err
		}
	}
	return nil
}

// unConfigureSFInterface deletes the sf from br-int and
// deletes corresponding SF created for the portmirrorID.
func (n *OvnNode) unConfigureSFInterface(sfInfo *sfDetails) error {
	klog.Infof("Unconfiguring SF interface %s", sfInfo.sfNetDeviceName)

	// del rep from br-int
	_, stderr, err := util.RunOVSVsctl("del-port", "br-int", sfInfo.sfRepName)
	if err != nil {
		klog.Errorf("Failed to delete %s sfrep from br-int: (%v) (%q)", sfInfo.sfRepName, err, stderr)
		// Remove the mirror:id external-id so that it can be reused.
		clearExtIdCmd := []string{
			"remove", "interface", sfInfo.sfRepName, "external_ids", "mirror-id",
			"external_ids", "port-mirrored",
		}
		_, stderr, err := util.RunOVSVsctl(clearExtIdCmd...)
		if err != nil {
			klog.Errorf("Failed to clear external_ids:mirror-id from %s: (%v) (%q)", sfInfo.sfRepName, err, stderr)
		}
	}

	// delete sf
	err = n.deleteSF(sfInfo.uplinkPhysPort, sfInfo.portIndex, sfInfo.sfNum)
	if err != nil {
		return fmt.Errorf("failed to delete SF port %s :(%v)", sfInfo.sfNetDeviceName, err)
	}
	klog.Infof("Deleted SF %s", sfInfo.sfNetDeviceName)
	return nil
}

func (n *OvnNode) handleSinkPodSelectors(pm *util.PortMirror) {
	klog.Infof("Adding portmirror podhandler %s/%s", pm.Namespace, pm.Name)
	pm.SinkLocalDetails.PodHandler, _ = n.watchFactory.AddFilteredPodHandler(pm.Namespace, pm.SinkLocalDetails.PodSelector,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				portMirrorNameUnlock := util.GetLockByPMName(pm.Namespace, pm.Name)
				defer portMirrorNameUnlock()
				pod := obj.(*kapi.Pod)
				if err := n.handlePortMirrorSinkPodAdd(pm, pod); err != nil {
					klog.Errorf(err.Error())
				}
			},
			DeleteFunc: func(obj interface{}) {
				portMirrorNameUnlock := util.GetLockByPMName(pm.Namespace, pm.Name)
				defer portMirrorNameUnlock()
				pod := obj.(*kapi.Pod)
				if err := n.handlePortMirrorSinkPodDelete(pm, pod); err != nil {
					klog.Errorf(err.Error())
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				// Add
			}}, nil)
}

func (n *OvnNode) addDPUPortMirror(portMirror *portmirror.PortMirror) error {
	klog.Infof("Adding portmirror %s/%s", portMirror.Namespace, portMirror.Name)
	pMirror, err := util.NewPortMirror(portMirror)
	if err != nil {
		return err
	}
	portMirrorKey := util.GetNamespacedName(portMirror.Namespace, portMirror.Name)
	portMirrorNameUnlock := util.GetLockByPMName(portMirror.Namespace, portMirror.Name)

	portMirrorIDUnlock := util.GetLockByPMId(pMirror.SinkLocalDetails.PortMirrorId)

	var portMirrorList []*util.PortMirror
	pmList, ok := n.mirrorIDToPortMirrorMap.Load(pMirror.SinkLocalDetails.PortMirrorId)
	if !ok {
		portMirrorList = make([]*util.PortMirror, 0)
	} else {
		portMirrorList = pmList.([]*util.PortMirror)
		// check if the new portmirror has the same sinkLocal details
		// as existing one's with same mirrorID
		match, err := util.DoesPortMirrorSinkDetailsMatch(pMirror, portMirrorList)
		if !match {
			errMsg := fmt.Sprintf("%s - Invalid configuration: %s", util.MessagePrefixPortMirrorErr, err.Error())
			util.UpdatePortMirrorStatusOnError(pMirror, n.Kube, errMsg, n.dpuName)
			portMirrorIDUnlock()
			portMirrorNameUnlock()
			return err
		}
	}
	portMirrorList = append(portMirrorList, pMirror)
	n.mirrorIDToPortMirrorMap.Store(pMirror.SinkLocalDetails.PortMirrorId, portMirrorList)
	portMirrorIDUnlock()

	n.portMirrorMap.Store(portMirrorKey, pMirror)
	portMirrorNameUnlock()
	n.handleSinkPodSelectors(pMirror)

	return nil
}

func (n *OvnNode) deleteDPUPortMirror(portMirror *portmirror.PortMirror) error {
	portMirrorNameUnlock := util.GetLockByPMName(portMirror.Namespace, portMirror.Name)
	defer portMirrorNameUnlock()

	portMirrorKey := util.GetNamespacedName(portMirror.Namespace, portMirror.Name)
	klog.Infof("Deleting portmirror %s/%s", portMirror.Namespace, portMirror.Name)
	p, ok := n.portMirrorMap.LoadAndDelete(portMirrorKey)
	if !ok {
		klog.Errorf("Deleting portmirror %s/%s which was not created succesfully", portMirror.Namespace, portMirror.Name)
		return nil
	}

	pm := p.(*util.PortMirror)
	// delete the sinkpod handler
	n.watchFactory.RemovePodHandler(pm.SinkLocalDetails.PodHandler.(*factory.Handler))

	// reduce the count of portMirrorID mapping.
	// obtain portMirrorID lock to do sfInfo handling
	portMirrorIDUnlock := util.GetLockByPMId(pm.SinkLocalDetails.PortMirrorId)
	defer portMirrorIDUnlock()

	// delete the portmirror form mirrorIDToPortMirrorMap
	pList, ok := n.mirrorIDToPortMirrorMap.Load(pm.SinkLocalDetails.PortMirrorId)
	if !ok {
		klog.Errorf("Deleting portmirror %s/%s which was not created succesfully", portMirror.Namespace, portMirror.Name)
		return nil
	}
	pmList := pList.([]*util.PortMirror)
	portMirrList := make([]*util.PortMirror, 0)
	for _, pMirror := range pmList {
		// delete this portmirror from the list
		if util.GetNamespacedName(pMirror.Namespace, pMirror.Name) == util.GetNamespacedName(pm.Namespace, pm.Name) {
			continue
		}
		portMirrList = append(portMirrList, pMirror)
	}

	if len(portMirrList) == 0 {
		n.mirrorIDToPortMirrorMap.Delete(pm.SinkLocalDetails.PortMirrorId)
	} else {
		// store the updated portmirror list
		n.mirrorIDToPortMirrorMap.Store(pm.SinkLocalDetails.PortMirrorId, portMirrList)
	}

	s, ok := n.portMirrorIDToSFMap.Load(pm.SinkLocalDetails.PortMirrorId)
	if !ok {
		klog.Infof("SF created for portmirror %s/%s is already deleted", pm.Namespace, pm.Name)
		return nil
	}

	sfInfo := s.(*sfDetails)
	// check if the portmirror key is present in portMirrorInUse map.
	// if present, reduce the count of mirrorCount.
	_, ok = sfInfo.portMirrorInUse[portMirrorKey]
	if !ok {
		// portMirrorKey has already been deleted so no need to
		// do any operations on the SF side.
		klog.Infof("Mirror key already deleted for portmirror %s/%s", pm.Namespace, pm.Name)
		return nil
	}
	sfInfo.mirrorCount--
	delete(sfInfo.portMirrorInUse, portMirrorKey)
	// if the mirrorCount is 0, then this is the last
	// portmirrorID associated with the sf, delete the SF.
	if sfInfo.mirrorCount == 0 {
		n.portMirrorIDToSFMap.Delete(pm.SinkLocalDetails.PortMirrorId)
		err := n.unConfigureSFInterface(sfInfo)
		if err != nil {
			return err
		}
	}
	return nil
}

// syncPortMirrorsDPUPeriodic deletes unused SF's by portmirror
func (n *OvnNode) syncPortMirrorsDPUPeriodic() {
	// get list of SF's from br-int and see if it's associated with any portMirrorID
	stdout, _, err := util.RunOVSVsctl("--no-headings", "--data=bare", "--format=csv",
		"--columns=name", "find", "Interface", "external-ids:port-mirrored=true")
	if err != nil || stdout == "" {
		if err != nil {
			klog.Errorf("Failed to list mirrored ports from br-int :(%v)", err)
		}
		return
	}

	portMirrors, err := n.watchFactory.GetPortMirrors()
	if err != nil {
		klog.Errorf("Failed to get portmirrors from informer cache :(%v)", err)
		return
	}

	portMirrorIDMap := make(map[string]int)
	for _, portMirror := range portMirrors {
		portMirrorID := util.GetPortMirrorID(portMirror)
		portMirrorIDMap[portMirrorID]++
	}

	sfInterfaceList := strings.Split(stdout, "\n")
	for _, sfInterface := range sfInterfaceList {
		mirrorID, _, err := util.RunOVSVsctl("--no-headings", "--data=bare", "--format=csv",
			"get", "Interface", sfInterface, "external-ids:mirror-id")
		if err != nil {
			klog.Errorf("Failed to get Interface %s external_ids mirror-id :(%v)", sfInterface, err)
			continue
		}
		// if correspodning mirrorID is not found in portMirrorIDMap,
		// its a stale SF interface and delete it.
		portMirrorIDUnlock := util.GetLockByPMId(mirrorID)
		if _, ok := portMirrorIDMap[mirrorID]; !ok {
			// get the SF details
			s, ok := n.portMirrorIDToSFMap.Load(mirrorID)
			if !ok {
				klog.Errorf("SF Info not found for portmirrorID %s", mirrorID)
				portMirrorIDUnlock()
				continue
			}
			sfInfo := s.(*sfDetails)
			// delete stale SF
			klog.Infof("Deleting stale SF %s during periodic sync", sfInfo.sfNetDeviceName)
			n.portMirrorIDToSFMap.Delete(mirrorID)
			// if present, delete mirrorID key from mirrorIDToPortMirrorMap
			if _, ok := n.mirrorIDToPortMirrorMap.Load(mirrorID); ok {
				n.mirrorIDToPortMirrorMap.Delete(mirrorID)
			}
			err := n.unConfigureSFInterface(sfInfo)
			if err != nil {
				klog.Errorf(err.Error())
			}
		}
		portMirrorIDUnlock()
	}
}

func (n *OvnNode) retryPortMirrorOperationsDPU() bool {
	item, quit := n.portMirrorRetryQueueDPU.Get()
	if quit {
		return false
	}
	n.portMirrorRetryQueueDPU.Done(item)
	retry, ok := item.(*portMirrorRetryRequestDPU)
	if !ok {
		return true
	}
	klog.V(4).Infof("Retrying portmirror event : %+v", *retry)

	portMirrorNameUnlock := util.GetLockByPMName(retry.pm.Namespace, retry.pm.Name)
	defer portMirrorNameUnlock()

	// check if the sink pod and corresponding portmirror exists while being retried.
	// if either of them doesn't exist, stop retrying
	if _, err := n.watchFactory.GetPortMirror(retry.pm.Namespace, retry.pm.Name); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Stop retrying sink pod %s/%s addition as corresponding portmirror %s/%s does not exist",
				retry.pod.Namespace, retry.pod.Name, retry.pm.Namespace, retry.pm.Name)
		} else {
			// requeue
			n.requeueSinkPodAddForPortMirror(retry.pm, retry.pod)
		}
		return true
	}

	if _, err := n.watchFactory.GetPod(retry.pod.Namespace, retry.pod.Name); err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Stop retrying pod addition %s/%s as it does not exist",
				retry.pod.Namespace, retry.pod.Name)
		} else {
			// requeue
			n.requeueSinkPodAddForPortMirror(retry.pm, retry.pod)
		}
		return true
	}
	err := n.handlePortMirrorSinkPodAdd(retry.pm, retry.pod)
	if err != nil {
		klog.Errorf("Mirroring ops failed for portmirror %s/%s on (%s) during retry: (%v)",
			retry.pm.Namespace, retry.pm.Name, util.GetNamespacedName(retry.pod.Namespace, retry.pod.Name), err)
	}
	return true
}

func (n *OvnNode) requeueSinkPodAddForPortMirror(portmirror *util.PortMirror, pod *kapi.Pod) {
	req := &portMirrorRetryRequestDPU{
		pm:  portmirror,
		pod: pod,
	}
	klog.V(4).Infof("Requeue portmirror %s/%s for sink pod %s/%s Add event to retry",
		portmirror.Namespace, portmirror.Name, pod.Namespace, pod.Name)
	n.portMirrorRetryQueueDPU.AddAfter(req, 3*time.Second)
}
