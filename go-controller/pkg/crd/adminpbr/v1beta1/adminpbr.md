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
###AdminPolicyBasedRoute { #k8s.ovn.org/v1beta1.AdminPolicyBasedRoute }
<p>
<p>AdminPolicyBasedRoute cluster-scoped API provides a way to influence routing decisions
in the SDN network. The API can be used to match the packets originating from and/or
&ndash; kubernetes nodes, namespaces, pods &ndash; and forward them to different destination for
further processing.</p>
<p>This API leverages OVN&rsquo;s Logical Router Policy feature. The AdminPolicyBasedRoute policies
or translated to OVN policies and these policies override OVN&rsquo;s static routing decision.
The priority of the policy rules is set to 100 (priority in range of 0 to 32,767, with
numerically higher priority taking precedence over those with lower), and it processed
after all the OVN K8s&rsquo; pre-defined rules.</p>
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
<a href="#k8s.ovn.org/v1beta1.AdminPolicyBasedRouteSpec">
AdminPolicyBasedRouteSpec
</a>
</em>
</td>
<td>
<br/>
<br/>
<table>
<tr>
<td>
<code>networkAttachmentName</code></br>
<em>
string
</em>
</td>
<td>
<p>Selects the network-attachment-definition for which the policy routes need to be
applied. The NAD should be of type Layer-3. Specifying anything else (Layer-2 or
Localnet) will be an invalid configuration.</p>
</td>
</tr>
<tr>
<td>
<code>policies</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.RoutingPolicyRule">
[]RoutingPolicyRule
</a>
</em>
</td>
<td>
<p>a collection of policy objects to influence routing</p>
</td>
</tr>
</table>
</td>
</tr>
<tr>
<td>
<code>status</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.AdminPolicyBasedRouteStatus">
AdminPolicyBasedRouteStatus
</a>
</em>
</td>
<td>
</td>
</tr>
</tbody>
</table>
###AdminPolicyBasedRouteSpec { #k8s.ovn.org/v1beta1.AdminPolicyBasedRouteSpec }
<p>
(<em>Appears on:</em>
<a href="#k8s.ovn.org/v1beta1.AdminPolicyBasedRoute">AdminPolicyBasedRoute</a>)
</p>
<p>
<p>AdminPolicyBasedRouteSpec defines the desired state of cluster scoped routing policies</p>
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
<code>networkAttachmentName</code></br>
<em>
string
</em>
</td>
<td>
<p>Selects the network-attachment-definition for which the policy routes need to be
applied. The NAD should be of type Layer-3. Specifying anything else (Layer-2 or
Localnet) will be an invalid configuration.</p>
</td>
</tr>
<tr>
<td>
<code>policies</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.RoutingPolicyRule">
[]RoutingPolicyRule
</a>
</em>
</td>
<td>
<p>a collection of policy objects to influence routing</p>
</td>
</tr>
</tbody>
</table>
###AdminPolicyBasedRouteStatus { #k8s.ovn.org/v1beta1.AdminPolicyBasedRouteStatus }
<p>
(<em>Appears on:</em>
<a href="#k8s.ovn.org/v1beta1.AdminPolicyBasedRoute">AdminPolicyBasedRoute</a>)
</p>
<p>
<p>AdminPolicyBasedRouteStatus describes the current status of the AdminPolicyBasedRoute.</p>
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
<p>A concise indication of whether the AdminPolicyBasedRoute resource is applied or not</p>
</td>
</tr>
</tbody>
</table>
###RoutingPolicyMatch { #k8s.ovn.org/v1beta1.RoutingPolicyMatch }
<p>
(<em>Appears on:</em>
<a href="#k8s.ovn.org/v1beta1.RoutingPolicyRule">RoutingPolicyRule</a>)
</p>
<p>
<p>RoutingPolicyMatch provides a way to select nodes, namespaces, and pods such that
the packets originating from them will be subjected to routing policies.</p>
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
<code>nodeSelector</code></br>
<em>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.19/#nodeselector-v1-core">
Kubernetes core/v1.NodeSelector
</a>
</em>
</td>
<td>
<p>NodeSelector matches the source packets only from the node(s) whose label
matches this definition. This field is optional.</p>
</td>
</tr>
<tr>
<td>
<code>namespaceSelector</code></br>
<em>
<a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.19/#labelselector-v1-meta">
Kubernetes meta/v1.LabelSelector
</a>
</em>
</td>
<td>
<p>NamespaceSelector matches the source packets only from the namespace(s) whose label
matches this definition. This field is optional.</p>
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
<em>(Optional)</em>
<p>PodSelector matches the source packets only from the pods whose label
matches this definition. This field is optional, and in case it is not set:
results in matching packets from all the pods in the namespace(s)
matched by the NamespaceSelector. In case it is set: is intersected with
the NamespaceSelector, thus matching the packets from the pods
(in the namespace(s) already matched by the NamespaceSelector) which
match this pod selector.</p>
</td>
</tr>
</tbody>
</table>
###RoutingPolicyNextHop { #k8s.ovn.org/v1beta1.RoutingPolicyNextHop }
<p>
(<em>Appears on:</em>
<a href="#k8s.ovn.org/v1beta1.RoutingPolicyRule">RoutingPolicyRule</a>)
</p>
<p>
<p>RoutingPolicyNextHop specifies the next-hop IP address for the policy route.</p>
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
<code>ips</code></br>
<em>
[]string
</em>
</td>
<td>
<p>NextHopIPs is the list of next-hop IP addresses for this route. With more than
one IP address, ECMP will kick in and one of the IP address will be selected
based on the 5-tuple hashing of the packet header. Currently, only one IP address
is supported. Furthermore, this IP address must be an overlay address, i.e., an
address within the OVN Logical Topology.</p>
</td>
</tr>
</tbody>
</table>
###RoutingPolicyRule { #k8s.ovn.org/v1beta1.RoutingPolicyRule }
<p>
(<em>Appears on:</em>
<a href="#k8s.ovn.org/v1beta1.AdminPolicyBasedRouteSpec">AdminPolicyBasedRouteSpec</a>)
</p>
<p>
<p>RoutingPolicyRule is a single routing policy rule object</p>
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
<code>from</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.RoutingPolicyMatch">
RoutingPolicyMatch
</a>
</em>
</td>
<td>
<p>From matches the packets for the routing policy</p>
</td>
</tr>
<tr>
<td>
<code>nexthop</code></br>
<em>
<a href="#k8s.ovn.org/v1beta1.RoutingPolicyNextHop">
RoutingPolicyNextHop
</a>
</em>
</td>
<td>
<p>NextHop defines where the matched packets should be forwarded</p>
</td>
</tr>
</tbody>
</table>
<hr/>
