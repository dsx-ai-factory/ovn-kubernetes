package ovn

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	ovsDBCache "github.com/ovn-org/libovsdb/cache"
	"github.com/ovn-org/libovsdb/client"
	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	virtualipv1beta1 "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/virtualip/v1beta1"
	virtualipscheme "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/virtualip/v1beta1/apis/clientset/versioned/scheme"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/sbdb"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	corev1 "k8s.io/api/core/v1"
	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	ref "k8s.io/client-go/tools/reference"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

const (
	actionAddVirtualIP      action = "add-virtualIP"
	actionAddVirtualIPod    action = "add-pod-for-virtualIP"
	optionsVirtualIP        string = "virtual-ip"
	optionsVirtualIPParents string = "virtual-parents"
)

type virtualIPRetryRequest struct {
	action action
	virtIP *virtualipv1beta1.VirtualIP
	vip    *virtualIP
	pod    *corev1.Pod
}

type virtualIP struct {
	name      string
	namespace string
	// logicalPortName is virtualip logicalswitch port name
	logicalPortName string
	// to store info about pods backed by this vip
	backingPods sync.Map

	// map for pods which didn't have
	// networking set when virtualip controller starts
	podRetry   sync.Map
	nadName    string
	vipAddress string
	podHandler *factory.Handler

	// map for all failed virtualIP operations
	failedVirtualIPOps map[string]bool

	// for updating virtualIP status
	status         ovntypes.OvnK8sStatus
	messages       []string
	backingPodsRef []corev1.ObjectReference
	activePodInfo  *backingPodInfo
	// lastTransitionTime represents the
	// time when virtualip changed from one to other pod
	lastTransitionTime *metav1.Time
}

type backingPodInfo struct {
	portName string
	podRef   corev1.ObjectReference
}

func NewVirtualIP(virtIP *virtualipv1beta1.VirtualIP) *virtualIP {
	vip := &virtualIP{
		name:               virtIP.Name,
		namespace:          virtIP.Namespace,
		nadName:            virtIP.Spec.NetworkAttachmentName,
		backingPods:        sync.Map{},
		vipAddress:         virtIP.Spec.VirtualIP,
		backingPodsRef:     make([]corev1.ObjectReference, 0),
		activePodInfo:      &backingPodInfo{},
		failedVirtualIPOps: make(map[string]bool),
	}
	return vip
}

func getVipKey(vip *virtualipv1beta1.VirtualIP) string {
	return fmt.Sprintf("%s/%s", vip.Namespace, vip.Name)
}

func getVirtualPortName(namespace, name string) string {
	return util.GetClusterScopedName(fmt.Sprintf("%s_%s_%s", ovntypes.VirtualPortPrefix, namespace, name))
}

func getVirtualIPLockKey(nadName, ipAddress string) string {
	return fmt.Sprintf("%s/%s", nadName, ipAddress)
}

func getVirtualIPPodLockKey(portName string) string {
	return fmt.Sprintf("virtualip/%s", portName)
}

func (oc *Controller) recordVirtualIPEvent(reason string, err string, virtualIP *virtualipv1beta1.VirtualIP) {
	virtIPRef, refErr := ref.GetReference(virtualipscheme.Scheme, virtualIP)
	if refErr != nil {
		klog.Errorf("Couldn't get a reference to virtualIP %s/%s to post an event: '%v'",
			virtualIP.Namespace, virtualIP.Name, refErr)
	} else {
		klog.V(5).Infof("Posting a %s event for virtualIP %s/%s", kapi.EventTypeWarning, virtualIP.Namespace, virtualIP.Name)
		oc.mc.recorder.Eventf(virtIPRef, kapi.EventTypeWarning, reason, err)
	}
}

func (oc *Controller) updateVIPActivePodInstance(pb *sbdb.PortBinding) error {
	if !util.HasExternalIDsForCluster(pb.ExternalIDs) {
		return nil
	}
	virtualIPNadName := pb.ExternalIDs[ovntypes.ExternalIDNetAttachDef]
	virtualIPAddress := pb.Options[optionsVirtualIP]
	unlock := util.LockByKey.Acquire(getVirtualIPLockKey(virtualIPNadName, virtualIPAddress))
	defer unlock()

	vipKey := pb.ExternalIDs[ovntypes.ExternalIDNamespace] + "/" + pb.ExternalIDs[ovntypes.ExternalIDName]
	v, ok := oc.virtualIPs.Load(vipKey)
	if !ok {
		// perhaps, we got called much before the VIP object got added by the oc controller.
		return nil
	}

	vip := v.(*virtualIP)

	// this case occurs when all the all virtualIP parent pods are deleted,
	// so active pod info of virtualIP need to be reset
	if pb.VirtualParent == nil {
		// no need to update if active podInfo is not set
		if vip.activePodInfo.portName == "" {
			return nil
		}
		vip.activePodInfo = &backingPodInfo{}
	} else if vip.activePodInfo.portName == *(pb.VirtualParent) {
		// if the port_binding's virtual_parent is same as current active pod and if the current active pod is still
		// one of the backing pods, then there is nothing to do
		if strings.Contains(pb.Options[optionsVirtualIPParents], vip.activePodInfo.portName) {
			return nil
		}
		vip.activePodInfo = &backingPodInfo{}
		vip.lastTransitionTime = &metav1.Time{Time: time.Now()}
	} else {
		vip.backingPods.Range(func(k, v interface{}) bool {
			podInfo := v.(*backingPodInfo)
			if podInfo.portName == *(pb.VirtualParent) {
				vip.activePodInfo = podInfo
				return false
			}
			return true
		})
		// update the transition time when there are backing pods for virtualIP
		// and activePod changes
		vip.lastTransitionTime = &metav1.Time{Time: time.Now()}
	}

	klog.V(5).Infof("Updating active pod status for virtualIP %s/%s to (%s)", vip.namespace, vip.name,
		vip.activePodInfo.portName)
	return oc.updateVirtualIPStatusWithRetry(vip.namespace, vip.name, "", nil, nil, vip.activePodInfo, vip.lastTransitionTime)
}

// watchPortBindingTable registers a event handler on SBDB updates
// and updates the virtualIP status active pod info on virtual port parent change.
func (oc *Controller) watchPortBindingTable(sbClient libovsdbclient.Client) error {

	// register Event handler to lookout for port_binding table Updates
	sbClient.Cache().AddEventHandler(&ovsDBCache.EventHandlerFuncs{
		AddFunc: func(table string, model model.Model) {
			if table != ovntypes.TablePortBinding {
				return
			}
			pb := model.(*sbdb.PortBinding)
			if pb.Type == ovntypes.VirtualPortType {
				err := oc.updateVIPActivePodInstance(pb)
				if err != nil {
					klog.Errorf(err.Error())
				}
			}
		},
		UpdateFunc: func(table string, oldModel model.Model, newModel model.Model) {
			if table != ovntypes.TablePortBinding {
				return
			}
			oldPortBinding := oldModel.(*sbdb.PortBinding)
			newPortBinding := newModel.(*sbdb.PortBinding)
			// this is needed since Conditional monitoring is not working
			if oldPortBinding == nil || newPortBinding == nil || oldPortBinding.Type != ovntypes.VirtualPortType ||
				newPortBinding.Type != ovntypes.VirtualPortType {
				return
			}
			// if there is no change to the active pod for this VIP, then nothing to do
			if oldPortBinding.VirtualParent == nil && newPortBinding.VirtualParent == nil {
				return
			}
			err := oc.updateVIPActivePodInstance(newPortBinding)
			if err != nil {
				klog.Errorf(err.Error())
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), ovntypes.OVSDBTimeout)
	go func() {
		<-oc.mc.stopChan
		cancel()
	}()

	// monitor only port_binding table for logical switch ports of type virtual
	pb := &sbdb.PortBinding{}
	_, err := sbClient.Monitor(ctx,
		sbClient.NewMonitor(
			client.WithConditionalTable(pb, model.Condition{
				Field:    &pb.Type,
				Function: ovsdb.ConditionEqual,
				Value:    ovntypes.VirtualPortType,
			}),
		),
	)

	if err != nil {
		sbClient.Close()
		return err
	}
	return nil
}

func (vip *virtualIP) getAllBackingPodPortandPodRef() (string, []corev1.ObjectReference) {
	var portsList = make([]string, 0)
	var backingPodsRef = make([]corev1.ObjectReference, 0)
	vip.backingPods.Range(func(k, v interface{}) bool {
		value := v.(*backingPodInfo)
		portsList = append(portsList, value.portName)
		backingPodsRef = append(backingPodsRef, value.podRef)
		return true
	})
	return strings.Join(portsList, ","), backingPodsRef
}

func (oc *Controller) updateVirtualPortOptions(vip *virtualIP, portsList string) error {
	var ops []ovsdb.Operation
	var err error
	lsp := &nbdb.LogicalSwitchPort{
		Name: vip.logicalPortName,
		Options: map[string]string{
			optionsVirtualIP:        vip.vipAddress,
			optionsVirtualIPParents: portsList,
		},
	}
	ops, err = oc.mc.nbClient.Where(lsp).Update(lsp, &lsp.Options)
	if err != nil {
		return fmt.Errorf("could not create commands to update logical switch port %s - %+v", lsp.Name, err)
	}
	_, err = libovsdbops.TransactAndCheckAndSetUUIDs(oc.mc.nbClient, lsp, ops)
	if err != nil {
		return fmt.Errorf("error transacting operations for virtualIP %s/%s is %+v err:(%v)",
			vip.namespace, vip.name, ops, err)
	}
	return nil
}

func (vip *virtualIP) needsRetry(pod *kapi.Pod) bool {
	_, boolVal := vip.podRetry.Load(getPodKey(pod))
	return boolVal
}

func (oc *Controller) updateVirtualIPStatusWithRetry(namespace, name string, status ovntypes.OvnK8sStatus, messages []string,
	backingPodsRef []corev1.ObjectReference, activePodInfo *backingPodInfo, lastTransitionTime *metav1.Time) error {
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the latest version of virtual IP object to modify it
		latestVIP, err := oc.mc.kube.GetVirtualIP(namespace, name)
		if err != nil {
			klog.Errorf("Unable to get virtualIP %s/%s for updating status, most likely it would be deleted",
				namespace, name)
			return err
		}

		latestVIP = latestVIP.DeepCopy()
		if status != "" {
			latestVIP.Status.Status = status
		}
		if messages != nil {
			latestVIP.Status.Messages = messages
		}
		if backingPodsRef != nil {
			latestVIP.Status.BackingPods = backingPodsRef

		}
		if activePodInfo != nil {
			latestVIP.Status.ActivePod = activePodInfo.podRef
		}
		if !lastTransitionTime.IsZero() {
			latestVIP.Status.LastTransitionTime = lastTransitionTime
		}
		return oc.mc.kube.UpdateVirtualIPStatus(latestVIP)
	})
	if retryErr != nil {
		return fmt.Errorf("error in updating status on virtualIP %s/%s: %v", namespace, name, retryErr)
	}
	return nil
}

// updateVirtualIPStatusOnPodError sets the virtualIP status object to failed state
// and updates messages field with error message
func (oc *Controller) updateVirtualIPStatusOnError(vip *virtualIP, errMsg string) {
	vip.status = ovntypes.OvnK8sStatusFailed
	vip.messages = append(vip.messages, errMsg)
	err := oc.updateVirtualIPStatusWithRetry(vip.namespace, vip.name, vip.status, vip.messages, nil, nil, nil)
	if err != nil {
		klog.Errorf(err.Error())
	}
}

func (oc *Controller) setErrorMessage(vip *virtualIP, errMsg, failedOpsKey string) {
	vip.failedVirtualIPOps[failedOpsKey] = true
	oc.updateVirtualIPStatusOnError(vip, errMsg)
}

// clearErrorMessage removes the corresponding podAdd failure or
// virtualIP creation failure from errorMessages list
func (oc *Controller) clearErrorMessage(vip *virtualIP, failedOpsKey string) {
	delete(vip.failedVirtualIPOps, failedOpsKey)
	errMessages := make([]string, 0)
	for _, errMsg := range vip.messages {
		if !strings.Contains(errMsg, failedOpsKey) {
			errMessages = append(errMessages, errMsg)
		}
	}
	vip.messages = errMessages
}

func (oc *Controller) removeVirtualIPFromPodPortSecurity(vipAddress string, portName string) error {
	// lock using (virtualIP/portName) as key, as same pod may be added to
	// multiple virtualIP's as backend.
	unlock := util.LockByKey.Acquire(getVirtualIPPodLockKey(portName))
	defer unlock()

	klog.Infof("Deleting virtualIP address %s from logical switch port %s port security", vipAddress, portName)
	portInfo, err := oc.logicalPortCache.get(portName)
	if err != nil {
		klog.V(5).Infof("Port %s not found in logical port cache: (%v)", portName, err)
		return nil
	}

	if !portInfo.expires.IsZero() {
		klog.V(5).Infof("Port %s is already marked for deletion", portName)
		return nil
	}

	lsp := &nbdb.LogicalSwitchPort{Name: portName}
	podLSP, err := libovsdbops.GetLogicalSwitchPort(oc.mc.nbClient, lsp)
	if err != nil && err != libovsdbclient.ErrNotFound {
		return fmt.Errorf("failed to get logical switch port %s info from NB DB (%v)", portName, err)
	}
	// if we don't find the lsp it means it might have deleted before so just return
	if err == libovsdbclient.ErrNotFound {
		return nil
	}

	addrList := make([]string, 0)
	for _, addr := range podLSP.PortSecurity {
		if strings.HasSuffix(addr, vipAddress) {
			continue
		}
		addrList = append(addrList, addr)
	}

	podLSP.PortSecurity = addrList
	ops, err := oc.mc.nbClient.Where(podLSP).Update(podLSP, &podLSP.PortSecurity)
	if err != nil {
		return fmt.Errorf("could not create commands to update logical switch port %s - %+v", portName, err)
	}

	_, err = libovsdbops.TransactAndCheckAndSetUUIDs(oc.mc.nbClient, podLSP, ops)
	if err != nil {
		return fmt.Errorf("could not perform update of logical switch port %s - %+v", portName, err)
	}

	return nil
}

func (oc *Controller) handleVIPPodDelete(vip *virtualIP, pod *kapi.Pod) error {
	unlock := util.LockByKey.Acquire(getVirtualIPLockKey(vip.nadName, vip.vipAddress))
	defer unlock()

	podKey := getPodKey(pod)
	val, ok := vip.backingPods.LoadAndDelete(podKey)
	if !ok {
		return fmt.Errorf("pod %s/%s not found in virtualIP backing pods list: %s/%s", pod.Namespace, pod.Name,
			vip.namespace, vip.name)
	}

	klog.Infof("Deleting pod: %s/%s from virtualIP: %s/%s", pod.Namespace, pod.Name, vip.namespace, vip.name)

	portsList, backingPodsReference := vip.getAllBackingPodPortandPodRef()
	klog.V(5).Infof("Parent ports for virtualIP %s/%s on pod %s/%s delete event are %s", vip.namespace, vip.name,
		pod.Namespace, pod.Name, portsList)

	err := oc.updateVirtualPortOptions(vip, portsList)
	if err != nil {
		return err
	}

	// if skipSpoofCheck annotation is added and this virtualIP nad is in that list,
	// we can skip the deletion part as virtualIP address is not added to pod port security
	skipPortSecurity := util.SkipSpoofCheckForNAD(pod.Annotations, vip.nadName)
	if !skipPortSecurity {
		podInfo := val.(*backingPodInfo)
		err = oc.removeVirtualIPFromPodPortSecurity(vip.vipAddress, podInfo.portName)
		if err != nil {
			return fmt.Errorf("failed to remove virtualIP address %s from lsp %s port-security (%v)", vip.vipAddress, podInfo.portName, err)
		}
	}
	// update the status of virtualIP object with updated list of backing pods
	vip.backingPodsRef = backingPodsReference
	err = oc.updateVirtualIPStatusWithRetry(vip.namespace, vip.name, "", nil, vip.backingPodsRef, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to update virtualIP %s/%s status", vip.namespace, vip.name)
	}

	return err
}

func (oc *Controller) addVirtualIPToPodPortSecurity(vipAddress, portName, portMacAddr string) error {
	// lock using (virtualIP/portName) as key, as same pod may be added to
	// multiple virtualIP's as backend.
	unlock := util.LockByKey.Acquire(getVirtualIPPodLockKey(portName))
	defer unlock()

	lsp := &nbdb.LogicalSwitchPort{Name: portName}
	podLSP, err := libovsdbops.GetLogicalSwitchPort(oc.mc.nbClient, lsp)
	if err != nil {
		return fmt.Errorf("unable to get the lsp %s from the nbdb: %s", portName, err)
	}

	var found bool
	// append vipAddress if its not found in lsp portSecurity field
	for _, addr := range podLSP.PortSecurity {
		if strings.HasSuffix(addr, vipAddress) {
			found = true
			break
		}
	}

	if found {
		return nil
	}

	addresses := portMacAddr + " " + vipAddress
	podLSP.PortSecurity = append(podLSP.PortSecurity, addresses)

	ops, err := oc.mc.nbClient.Where(podLSP).Update(podLSP, &podLSP.PortSecurity)
	if err != nil {
		return fmt.Errorf("could not create commands to update logical switch port %s - %+v", portName, err)
	}

	_, err = libovsdbops.TransactAndCheckAndSetUUIDs(oc.mc.nbClient, podLSP, ops)
	if err != nil {
		return fmt.Errorf("could not perform update of logical switch port %s - %+v", portName, err)
	}
	return nil
}

func (oc *Controller) handleVIPPodAdd(vip *virtualIP, pod *kapi.Pod) error {
	unlock := util.LockByKey.Acquire(getVirtualIPLockKey(vip.nadName, vip.vipAddress))
	defer unlock()

	// clear any error messages from virtualIP status if
	// this pod addition to virtualIP was failed before
	podKey := getPodKey(pod)
	if _, ok := vip.failedVirtualIPOps[podKey]; ok {
		oc.clearErrorMessage(vip, podKey)

		// check whether this pod exists when this pod addition to virtualIP is retried
		if _, err := oc.mc.watchFactory.GetPod(pod.Namespace, pod.Name); err != nil {
			if errors.IsNotFound(err) {
				klog.Infof("Stop retrying pod addition %s/%s as it does not exist", pod.Namespace, pod.Name)
				return nil
			} else {
				errMsg := fmt.Sprintf("Failed to get pod %s from informer cache", podKey)
				oc.setErrorMessage(vip, errMsg, podKey)
				// add this pod to retryQueue
				oc.requeuePodAdd4VirtualIP(actionAddVirtualIPod, vip, pod)
				return fmt.Errorf("failed to retrieve pod %s/%s from informer cache: (%v)", pod.Namespace, pod.Name, err)
			}
		}
	}

	if pod.Spec.HostNetwork {
		// skip host network pods
		klog.Errorf("Backing pods for virtualIP %s/%s can't be host network pods", vip.namespace, vip.name)
		return nil
	}

	on, _, err := util.IsNetworkOnPod(pod, oc.nadInfo)
	if err != nil || !on {
		// pod is not attached to this specific network
		klog.Errorf("Pod %s is not attached to network %s error (%v)", pod.Name, oc.nadInfo.NetName, err)
		return nil
	}

	klog.Infof("Adding pod: %s/%s to VirtualIP: %s/%s", pod.Namespace, pod.Name, vip.namespace, vip.name)
	portName := util.GetLogicalPortName(pod.Namespace, pod.Name, vip.nadName, !oc.nadInfo.IsSecondary)
	// check if the network for pod is established
	portInfo, err := oc.logicalPortCache.get(portName)
	if err != nil {
		klog.Errorf("%s/%s pod networking is not set yet : (%v)", pod.Namespace, pod.Name, err)
		vip.podRetry.Store(podKey, true)
		return nil
	}

	// this is portInfo of the previous deleted Pod of the same name
	// wait for the next Pod update event
	if !portInfo.expires.IsZero() {
		klog.Errorf("Port %s is already marked for removal", portName)
		vip.podRetry.Store(podKey, true)
		return nil
	}

	// delete the pod from podRetry list as port plumbing has been successful
	if vip.needsRetry(pod) {
		vip.podRetry.Delete(podKey)
	}

	// Need to update the pod logical switch port security to include
	// this virtualIP address
	// if skipSpoofcheckannotation is present on pod for this nad,
	// then don't add this virtualIP address to pod lsp port security
	skipPortSecurity := util.SkipSpoofCheckForNAD(pod.Annotations, vip.nadName)
	if !skipPortSecurity {
		err = oc.addVirtualIPToPodPortSecurity(vip.vipAddress, portName, portInfo.mac.String())
		if err != nil {
			errMsg := fmt.Sprintf("Failed to add virtualIP address to pod %s port security", podKey)
			oc.setErrorMessage(vip, errMsg, podKey)
			// add this pod to retryQueue
			oc.requeuePodAdd4VirtualIP(actionAddVirtualIPod, vip, pod)
			return fmt.Errorf("failed to add virtualIP address %s to lsp %s port-security (%v)", vip.vipAddress, portName, err)
		}
	} else {
		klog.Infof("Skip adding virtualIP address %s to pod %s/%s port security", vip.vipAddress, pod.Namespace, pod.Name)
	}

	podInfo := &backingPodInfo{
		portName: portName,
		podRef: corev1.ObjectReference{
			Name: pod.ObjectMeta.Name,
		},
	}
	vip.backingPods.Store(podKey, podInfo)
	portsList, backingPodsReference := vip.getAllBackingPodPortandPodRef()
	klog.V(5).Infof("Parent ports for virtualIP %s/%s after pod %s/%s add event are %s", vip.namespace, vip.name,
		pod.Namespace, pod.Name, portsList)
	err = oc.updateVirtualPortOptions(vip, portsList)
	if err != nil {
		// delete this pod from backingPods list as there was a failure
		// in updating virtual parents options with pod addition.
		vip.backingPods.Delete(podKey)
		errMsg := fmt.Sprintf("Failed to add pod %s as virtual parent to virtualIP", podKey)
		oc.setErrorMessage(vip, errMsg, podKey)
		// add this pod to retryQueue
		oc.requeuePodAdd4VirtualIP(actionAddVirtualIPod, vip, pod)
		return err
	}

	// set virtualIP status to succceeded if failedVirtualIPOps map is empty
	if len(vip.failedVirtualIPOps) == 0 {
		vip.status = ovntypes.OvnK8sStatusSucceeded
	}

	// update the virtual ip status with updated backing pod info
	vip.backingPodsRef = backingPodsReference
	return oc.updateVirtualIPStatusWithRetry(vip.namespace, vip.name, vip.status, vip.messages, vip.backingPodsRef, nil, nil)
}

// isVirtualIPAddressValid checks if vipAddress is contained in
// networkAttachmentDefinition netCIDRs & exclude_cidrs range
// and also checks if it is duplicate of already existing virtualIP's ipaddress
func (oc *Controller) isVirtualIPAddressValid(vip *virtualIP) (bool, error) {
	// vipAddr will never be a invalid one(nil) as it
	// will be validated while creation of virtualIP
	vipAddr := net.ParseIP(vip.vipAddress)
	// this will be validated while creation of network-attachment-definition
	// so no need to recheck for error here.
	netCIDRs, _ := config.ParseClusterSubnetEntries(oc.nadInfo.NetCidr, false)
	var inNetCIDRRange bool
	// check if virtualIP networkAttachmentDefinition netCIDRs contain vipAddress
	for _, netCIDR := range netCIDRs {
		if netCIDR.CIDR.Contains(vipAddr) {
			inNetCIDRRange = true
			break
		}
	}
	if !inNetCIDRRange {
		return false, fmt.Errorf("VirtualIP (%s/%s)'s address %s must belong to network-attachment-definiton (%s)'s "+
			"CIDR range", vip.namespace, vip.name, vip.vipAddress, oc.nadInfo.NetName)
	}

	// virtualIP must be in networkAttachmentDefinition's excludeIP range
	var inExcludeIPRange bool
	for _, excludeIP := range oc.nadInfo.ExcludeIPs {
		if excludeIP.Equal(vipAddr) {
			inExcludeIPRange = true
			break
		}
	}
	if !inExcludeIPRange {
		return false, fmt.Errorf("VirtualIP (%s/%s)'s address %s must be inside network-attachment-definiton (%s)'s) "+
			"exclude_cidr range", vip.namespace, vip.name, vip.vipAddress, oc.nadInfo.NetName)
	}

	// check if virtualIP adddress is duplicate of already existing virtualIP's ipaddress
	var isDuplicate bool
	var virtIP *virtualIP
	oc.virtualIPs.Range(func(k, v interface{}) bool {
		virtIP = v.(*virtualIP)
		if vip.vipAddress == virtIP.vipAddress {
			isDuplicate = true
			return false
		}
		return true
	})
	if isDuplicate {
		return false, fmt.Errorf("VirtualIP (%s/%s)'s address %s is duplicate of existing virtualIP (%s/%s) address",
			vip.namespace, vip.name, vip.vipAddress, virtIP.name, virtIP.namespace)
	}

	return true, nil
}

func (oc *Controller) createVIP(vip *virtualIP) error {
	ls, err := oc.waitForNodeLogicalSwitch(util.GetClusterScopedName(oc.nadInfo.Prefix + ovntypes.OvnLayer2Switch))
	if err != nil {
		return err
	}
	vip.logicalPortName = getVirtualPortName(vip.namespace, vip.name)
	lsp := &nbdb.LogicalSwitchPort{Name: vip.logicalPortName}
	_, err = libovsdbops.GetLogicalSwitchPort(oc.mc.nbClient, lsp)
	if err != nil && err != libovsdbclient.ErrNotFound {
		return fmt.Errorf("failed while checking for existence of virtual port %s in the NB DB: (%v)",
			vip.logicalPortName, err)
	}

	if err == nil {
		klog.Infof("Virtual port %s is already present in OVN", vip.logicalPortName)
		return nil
	}

	externalIDs := util.ExternalIDsForCluster(map[string]string{
		ovntypes.ExternalIDNamespace:    vip.namespace,
		ovntypes.ExternalIDName:         vip.name,
		ovntypes.ExternalIDNetAttachDef: vip.nadName,
	})

	// port doesn't exist and need to create one
	lsp = &nbdb.LogicalSwitchPort{
		Name:        vip.logicalPortName,
		Type:        ovntypes.VirtualPortType,
		ExternalIDs: externalIDs,
		// setting this intially as we are using lock based on (nadName/VipAddress)
		Options: map[string]string{
			optionsVirtualIP: vip.vipAddress,
		},
	}

	err = libovsdbops.CreateOrUpdateLogicalSwitchPortsOnSwitch(oc.mc.nbClient, ls, lsp)
	if err != nil {
		return fmt.Errorf("error creating logical switch port %+v on switch %+v: %+v", *lsp, *ls, err)
	}

	return nil
}

func (oc *Controller) addVirtualIP(virtIP *virtualipv1beta1.VirtualIP) error {
	klog.Infof("Adding virtualIP %s/%s on network %s", virtIP.Namespace, virtIP.Name, oc.nadInfo.NetName)
	// lock using (virtualIPNadName/virtualIPAddress) to handle
	// virtualIP with duplicate IP addresses
	virtIPLockKey := getVirtualIPLockKey(virtIP.Spec.NetworkAttachmentName, virtIP.Spec.VirtualIP)
	unlock := util.LockByKey.Acquire(virtIPLockKey)

	vip := NewVirtualIP(virtIP)
	// clear any error message for this virtualIP
	// if there were any errors during virtual port creation in OVN
	virtualIPKey := getVipKey(virtIP)
	if _, ok := vip.failedVirtualIPOps[virtualIPKey]; ok {
		oc.clearErrorMessage(vip, virtualIPKey)

		// check if the virtualIP exists when virtualIP creation in OVN is retried
		if _, err := oc.mc.watchFactory.GetVirtualIP(virtIP.Namespace, virtIP.Name); err != nil {
			var updatedErr error
			if errors.IsNotFound(err) {
				klog.Infof("Stop retrying virtualIP %s/%s as it does not exist", virtIP.Namespace, virtIP.Name)
			} else {
				errMsg := fmt.Sprintf("Failed to retrieve virtualIP %s from cache", virtualIPKey)
				oc.setErrorMessage(vip, errMsg, virtualIPKey)
				// add this operation to virtualIPRetryQueue
				oc.requeueVirtualIP(actionAddVirtualIP, virtIP)
				updatedErr = fmt.Errorf("failed in retrieving virtualIP %s/%s from cache: %v", virtIP.Namespace, virtIP.Name, err)
			}
			unlock()
			return updatedErr
		}
	}

	if oc.nadInfo.TopoType != ovntypes.Layer2AttachDefTopoType {
		err := fmt.Errorf("VirtualIP (%s/%s)'s network-attachment-defintion %s is not a L2 network type",
			vip.namespace, vip.name, oc.nadInfo.NetName)
		oc.updateVirtualIPStatusOnError(vip, err.Error())
		unlock()
		return err
	}

	if isVIPAddressValid, err := oc.isVirtualIPAddressValid(vip); !isVIPAddressValid {
		oc.updateVirtualIPStatusOnError(vip, err.Error())
		unlock()
		return err
	}

	err := oc.createVIP(vip)
	if err != nil {
		errMsg := fmt.Sprintf("Failed to create virtual port for %s in OVN", virtualIPKey)
		oc.setErrorMessage(vip, errMsg, virtualIPKey)
		// add this operation to virtualIPRetryQueue
		oc.requeueVirtualIP(actionAddVirtualIP, virtIP)

		unlock()
		return fmt.Errorf("failed to create virtualIP (%s/%s) - %v", virtIP.Namespace, virtIP.Name, err)
	}

	vip.status = ovntypes.OvnK8sStatusSucceeded
	vip.messages = append(vip.messages, "Created virtual port in OVN")
	// Store in cache only if virtualIP port creation is successful
	oc.virtualIPs.Store(virtualIPKey, vip)
	err = oc.updateVirtualIPStatusWithRetry(vip.namespace, vip.name, vip.status, vip.messages, nil, nil, nil)
	if err != nil {
		klog.Errorf(err.Error())
	}
	unlock()

	sel, _ := metav1.LabelSelectorAsSelector(&virtIP.Spec.PodSelector)
	vip.podHandler, _ = oc.mc.watchFactory.AddFilteredPodHandler(vip.namespace, sel,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pod := obj.(*kapi.Pod)
				if err := oc.handleVIPPodAdd(vip, pod); err != nil {
					klog.Errorf(err.Error())
					oc.recordVirtualIPEvent("VirtualIPPodAddError", err.Error(), virtIP)
				}
			},
			DeleteFunc: func(obj interface{}) {
				pod := obj.(*kapi.Pod)
				if err := oc.handleVIPPodDelete(vip, pod); err != nil {
					klog.Errorf(err.Error())
					oc.recordVirtualIPEvent("VirtualIPPodDelError", err.Error(), virtIP)
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				newPod := newObj.(*kapi.Pod)
				if vip.needsRetry(newPod) {
					if err := oc.handleVIPPodAdd(vip, newPod); err != nil {
						klog.Errorf(err.Error())
						oc.recordVirtualIPEvent("VirtualIPPodAddError", err.Error(), virtIP)
					}
				}
			}}, nil)
	return nil
}

func (oc *Controller) deleteVirtualIP(virtIP *virtualipv1beta1.VirtualIP) error {
	// lock using (virtualIPNadName/virtualIPAddress) to handle
	// virtualIP with duplicate IP addresses
	virtIPLockKey := getVirtualIPLockKey(virtIP.Spec.NetworkAttachmentName, virtIP.Spec.VirtualIP)
	unlock := util.LockByKey.Acquire(virtIPLockKey)
	defer unlock()

	v, ok := oc.virtualIPs.LoadAndDelete(getVipKey(virtIP))
	if !ok {
		klog.Errorf("Deleting virtualIP %s/%s which was not created successfully", virtIP.Namespace, virtIP.Name)
		return nil
	}

	klog.Infof("Deleting virtualIP %s/%s on network %s", virtIP.Namespace, virtIP.Name, oc.nadInfo.NetName)
	vip := v.(*virtualIP)
	// remove filtered namespace pod handler
	oc.mc.watchFactory.RemovePodHandler(vip.podHandler)

	switchName := util.GetClusterScopedName(oc.nadInfo.Prefix + ovntypes.OvnLayer2Switch)
	lsp := nbdb.LogicalSwitchPort{Name: vip.logicalPortName}
	sw := nbdb.LogicalSwitch{Name: switchName}
	err := libovsdbops.DeleteLogicalSwitchPorts(oc.mc.nbClient, &sw, &lsp)
	if err != nil {
		klog.Errorf("Failed to delete virtual port %s from logical switch %s: %v",
			vip.logicalPortName, switchName, err)
	}

	// delete the virtual-ip address from pod port security list backed by this virtual ip
	vip.backingPods.Range(func(k, v interface{}) bool {
		value := v.(*backingPodInfo)
		err := oc.removeVirtualIPFromPodPortSecurity(vip.vipAddress, value.portName)
		if err != nil {
			klog.Errorf("Failed to remove virtualIP address %s from logical switch port %s's port-security (%v)",
				vip.vipAddress, value.portName, err)
		}
		return true
	})
	return nil
}

// syncVirtualIPPods checks for any stale pod logical port names from virtualport parent options
func (oc *Controller) syncVirtualIPPods(vip *virtualIP, vipLSP *nbdb.LogicalSwitchPort, podSelectorLabels metav1.LabelSelector) {
	// Delete stale pod logical port names from virtual port parent options
	parentPorts := vipLSP.Options[optionsVirtualIPParents]
	if parentPorts == "" {
		klog.V(5).Infof("VirtualIP %s/%s has no backing pods", vip.namespace, vip.name)
		return
	}

	// get all the pods backed by this virtualIP
	pods, err := oc.mc.watchFactory.GetPodsBySelector(vip.namespace, podSelectorLabels)
	if err != nil {
		klog.Errorf("Failed to get pods backed for virtualIP %s/%s: (%v)", vip.namespace, vip.name, err)
		return
	}

	updatedVirtualParentPortsList := make([]string, 0)
	for _, parentPortName := range strings.Split(parentPorts, ",") {
		for _, pod := range pods {
			// Get pod logical port name
			PodPortName := util.GetLogicalPortName(pod.Namespace, pod.Name, vip.nadName, !oc.nadInfo.IsSecondary)
			if parentPortName == PodPortName {
				updatedVirtualParentPortsList = append(updatedVirtualParentPortsList, parentPortName)
				break
			}
		}
	}

	updatedVIPParentPorts := strings.Join(updatedVirtualParentPortsList, ",")
	// update the virtualIP status only if there are stale pod logicalports
	if updatedVIPParentPorts != parentPorts {
		klog.V(5).Infof("Updating virtualIP %s/%s virtual-parent port options from %s to %s",
			vip.namespace, vip.name, parentPorts, updatedVIPParentPorts)
		err = oc.updateVirtualPortOptions(vip, updatedVIPParentPorts)
		if err != nil {
			klog.Errorf(err.Error())
		}
	}
}

func (oc *Controller) syncVirtualIPsPeriodic() {
	klog.Infof("Starting VirtualIP sync for network %s", oc.nadInfo.NetName)
	switchName := util.GetClusterScopedName(oc.nadInfo.Prefix + ovntypes.OvnLayer2Switch)
	sw := &nbdb.LogicalSwitch{Name: switchName}
	ls, err := libovsdbops.GetLogicalSwitch(oc.mc.nbClient, sw)
	if err != nil {
		klog.Errorf("Failed to get logical switch %s from OVN (%v)", switchName, err)
		return
	}

	lookupFunc := func(item *nbdb.LogicalSwitchPort) bool {
		return item.Type == ovntypes.VirtualPortType && util.HasExternalIDsForCluster(item.ExternalIDs)
	}
	vipLSPList, err := libovsdbops.FindLogicalSwitchPortsWithPredicate(oc.mc.nbClient, sw, lookupFunc)
	if err != nil {
		klog.Errorf("Failed to get list of virtual ports from logical switch %s (%v)", switchName, err)
		return
	}

	for _, lsp := range vipLSPList {
		nadName := lsp.ExternalIDs[ovntypes.ExternalIDNetAttachDef]
		vipAddress := lsp.Options[optionsVirtualIP]
		vipName := lsp.ExternalIDs[ovntypes.ExternalIDName]
		vipNamespace := lsp.ExternalIDs[ovntypes.ExternalIDNamespace]
		unlock := util.LockByKey.Acquire(getVirtualIPLockKey(nadName, vipAddress))

		virtIP, err := oc.mc.watchFactory.GetVirtualIP(vipNamespace, vipName)
		if err != nil && !errors.IsNotFound(err) {
			// skip this virtualIP sync in this round
			klog.Errorf("Failed to get virtualIP %s/%s from informer cache: (%v)",
				virtIP.Namespace, virtIP.Name, err)
			unlock()
			continue
		}

		var vipFoundInK8s bool
		if virtIP != nil {
			// virtualIP will be nil when we don't find virtualIP object in inforemer cache
			vipFoundInK8s = true
		}

		if vipFoundInK8s {
			// this means port is present and we need to check for any
			// stale pod logical port names from virtualport parent options
			v, ok := oc.virtualIPs.Load(getVipKey(virtIP))
			if !ok {
				// this virtual port backed virtaulIP is not present in controller,
				// this might happen if the virtualIP addition wasn't successful and
				// virtualIP operation is in virtualIP retry queue
				unlock()
				continue
			}
			vip := v.(*virtualIP)
			oc.syncVirtualIPPods(vip, lsp, virtIP.Spec.PodSelector)
		} else {
			// if the virtual port is not backed by any virtualIP,
			// then its a stale port and needs to be removed from OVN
			klog.V(5).Infof("Removing stale virtual port %s from NBDB", lsp.Name)
			ops, err := oc.mc.nbClient.Where(ls).Mutate(ls, model.Mutation{
				Field:   &ls.Ports,
				Mutator: ovsdb.MutateOperationDelete,
				Value:   []string{lsp.UUID},
			})
			if err != nil {
				klog.Errorf("Could not generate ops to delete stale port from logical switch %s (%+v)", lsp.Name, switchName, err)
				unlock()
				continue
			}

			_, err = libovsdbops.TransactAndCheck(oc.mc.nbClient, ops)
			if err != nil {
				klog.Errorf("Could not remove stale logical port (%s) of type virtual for logical switch %s (%+v)",
					lsp.Name, switchName, err)
			}
		}
		unlock()
	}
	klog.Infof("VirtualIP sync complete for network %s", oc.nadInfo.NetName)
}

// retryVirtualIPOperations retries the failed virtualIP operations.
// currently, retrying only add operations during virtualIP creation
// & adding a backing pod to virtualIP
func (oc *Controller) retryVirtualIPOperations() bool {
	item, quit := oc.virtualIPRetryQueue.Get()
	if quit {
		return false
	}
	oc.virtualIPRetryQueue.Done(item)
	retry, ok := item.(*virtualIPRetryRequest)
	if !ok {
		return true
	}
	klog.V(4).Infof("Retrying virtualIP event: %v", retry)
	switch retry.action {
	case actionAddVirtualIP:
		err := oc.addVirtualIP(retry.virtIP)
		if err != nil {
			klog.Errorf("Failed creating virtualIP %s during retry", getVipKey(retry.virtIP))
		}
	case actionAddVirtualIPod:
		err := oc.handleVIPPodAdd(retry.vip, retry.pod)
		if err != nil {
			klog.Errorf("Failed adding pod %s to virtualIP (%s) during retry",
				getPodKey(retry.pod), getVipKey(retry.virtIP))
		}
	}
	return true
}

func (oc *Controller) requeueVirtualIP(ra action, virtualIP *virtualipv1beta1.VirtualIP) {
	req := &virtualIPRetryRequest{
		action: ra,
		virtIP: virtualIP,
	}
	klog.V(4).Infof("Requeue VirtualIP %s/%s Add event to retry: %v", virtualIP.Namespace, virtualIP.Name)
	oc.virtualIPRetryQueue.AddAfter(req, time.Duration(3*time.Second))
}

func (oc *Controller) requeuePodAdd4VirtualIP(ra action, vip *virtualIP, pod *corev1.Pod) {
	req := &virtualIPRetryRequest{
		action: ra,
		vip:    vip,
		pod:    pod,
	}
	klog.V(4).Infof("Requeue virtualIP %s/%s pod Add event to retry", vip.namespace, vip.name)
	oc.virtualIPRetryQueue.AddAfter(req, time.Duration(3*time.Second))
}
