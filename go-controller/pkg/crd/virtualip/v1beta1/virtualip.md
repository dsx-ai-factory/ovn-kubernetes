---
title: API
---
<p>Packages:</p>
<ul>
<li>
<a href="#k8s.ovn.org%2fv1beta1">k8s.ovn.org/v1beta1</a>
</li>
</ul>
<h2 id="k8s.ovn.org/v1beta1">k8s.ovn.org/v1beta1</h2>
<p>
<p>Package v1beta1 contains API Schema definitions for the k8s.ovn.org v1beta1 API group</p>
</p>
###VirtualIP { #k8s.ovn.org/v1beta1.VirtualIP }
<p>
<p>VirtualIP API provides necessary plumbing in the overlay network so that
the consumers of the API can implement highly available service instances
using, for example, keepalived.</p>
<p>This API leverages OVN’s Virtual Port feature. This port represents a
virtual ip that is backed by one or more Pods configured in active-standby
setup. The virtual ip resides on one of the Pods. When the active pod dies, the
virtual IP moves to one of the standby Pods and the OVN SDN control
plane discovers this move and ensures that all the packets to virtual
IP are forwarded to the now active Pod.</p>
</p>
<table>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td>
<code>metadata</code></br>
<em>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.19/#objectmeta-v1-meta">
Kubernetes meta/v1.ObjectMeta
</a>
</em>
</td>
<td>
Refer to the Kubernetes API documentation for the fields of the
<code>metadata</code> field.
</td>
</tr>
<tr>
<td>
<code>spec</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.VirtualIPSpec">
VirtualIPSpec
</a>
</em>
</td>
<td>
<br/>
<br/>
<table>
<tr>
<td>
<code>virtualIP</code></br>
<em>
string
</em>
</td>
<td>
<p>VirtualIP specifies the address behind which an highly available
service instance is going to run. The HA implementation itself is
provided by mechanisms such as keepalived, for example.</p>
</td>
</tr>
<tr>
<td>
<code>podSelector</code></br>
<em>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.19/#labelselector-v1-meta">
Kubernetes meta/v1.LabelSelector
</a>
</em>
</td>
<td>
<p>Selects the pods that form the backend for the virtual IP address. That is,
the virtual IP would move between these Pods during the failover. This field
is NOT optional and follows standard label selector semantics. An empty
podSelector matches all pods in this namespace.</p>
</td>
</tr>
<tr>
<td>
<code>networkAttachment</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.VirtualIPNetworkAttachment">
VirtualIPNetworkAttachment
</a>
</em>
</td>
<td>
<p>Selects the network-attachment-definition on which the virtualIP is going
to reside. Currently, the support vor Virtual IP is for Layer-2 NADs.</p>
</td>
</tr>
</table>
</td>
</tr>
<tr>
<td>
<code>status</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.VirtualIPStatus">
VirtualIPStatus
</a>
</em>
</td>
<td>
</td>
</tr>
</tbody>
</table>
###VirtualIPNetworkAttachment { #k8s.ovn.org/v1beta1.VirtualIPNetworkAttachment }
<p>
(<em>Appears on:</em>
<a href="#k8s.ovn.org/v1beta1.VirtualIPSpec">VirtualIPSpec</a>)
</p>
<p>
<p>VirtualIPNetworkAttachment Specifies the network attachment on which the virtual IP
must be configured</p>
</p>
<table>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td>
<code>name</code></br>
<em>
string
</em>
</td>
<td>
<p>Name of the network-attachment-definition on which virtual IP needs to
be configured in he format: <namespace>/<nadName></p>
</td>
</tr>
</tbody>
</table>
###VirtualIPSpec { #k8s.ovn.org/v1beta1.VirtualIPSpec }
<p>
(<em>Appears on:</em>
<a href="#k8s.ovn.org/v1beta1.VirtualIP">VirtualIP</a>)
</p>
<p>
<p>VirtualIPSpec defines the desired state of VirtualIP</p>
</p>
<table>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td>
<code>virtualIP</code></br>
<em>
string
</em>
</td>
<td>
<p>VirtualIP specifies the address behind which an highly available
service instance is going to run. The HA implementation itself is
provided by mechanisms such as keepalived, for example.</p>
</td>
</tr>
<tr>
<td>
<code>podSelector</code></br>
<em>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.19/#labelselector-v1-meta">
Kubernetes meta/v1.LabelSelector
</a>
</em>
</td>
<td>
<p>Selects the pods that form the backend for the virtual IP address. That is,
the virtual IP would move between these Pods during the failover. This field
is NOT optional and follows standard label selector semantics. An empty
podSelector matches all pods in this namespace.</p>
</td>
</tr>
<tr>
<td>
<code>networkAttachment</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.VirtualIPNetworkAttachment">
VirtualIPNetworkAttachment
</a>
</em>
</td>
<td>
<p>Selects the network-attachment-definition on which the virtualIP is going
to reside. Currently, the support vor Virtual IP is for Layer-2 NADs.</p>
</td>
</tr>
</tbody>
</table>
###VirtualIPStatus { #k8s.ovn.org/v1beta1.VirtualIPStatus }
<p>
(<em>Appears on:</em>
<a href="#k8s.ovn.org/v1beta1.VirtualIP">VirtualIP</a>)
</p>
<p>
<p>VirtualIPStatus describes the current status of the VirtualIP.</p>
</p>
<table>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td>
<code>activePod</code></br>
<em>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.19/#objectreference-v1-core">
Kubernetes core/v1.ObjectReference
</a>
</em>
</td>
<td>
<p>Reference to the Pod that currently owns this virtual IP</p>
</td>
</tr>
<tr>
<td>
<code>lastTransitionTime</code></br>
<em>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.19/#time-v1-meta">
Kubernetes meta/v1.Time
</a>
</em>
</td>
<td>
<em>(Optional)</em>
<p>Information when was the last time virtualIP moved between the Pods.</p>
</td>
</tr>
<tr>
<td>
<code>backingPods</code></br>
<em>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.19/#objectreference-v1-core">
[]Kubernetes core/v1.ObjectReference
</a>
</em>
</td>
<td>
<em>(Optional)</em>
<p>A list of pointers to all the Pods backing this virtual IP</p>
</td>
</tr>
<tr>
<td>
<code>message</code></br>
<em>
string
</em>
</td>
<td>
<em>(Optional)</em>
<p>A human-readable message indicating details about the status of the object.</p>
</td>
</tr>
<tr>
<td>
<code>status</code></br>
<em>
string
</em>
</td>
<td>
<em>(Optional)</em>
<p>A concise indication of whether the virtualIP resource is applied or not</p>
</td>
</tr>
</tbody>
</table>
<hr/>
