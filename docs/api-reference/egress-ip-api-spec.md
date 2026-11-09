# API Reference

## Packages
- [k8s.ovn.org/v1](#k8sovnorgv1)


## k8s.ovn.org/v1

Package v1 contains API Schema definitions for the network v1 API group

### Resource Types
- [EgressIP](#egressip)



#### EgressIP



EgressIP is a CRD allowing the user to define a fixed
source IP for all egress traffic originating from any pods which
match the EgressIP resource according to its spec definition.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `k8s.ovn.org/v1` | | |
| `kind` _string_ | `EgressIP` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[EgressIPSpec](#egressipspec)_ | Specification of the desired behavior of EgressIP. |  |  |
| `status` _[EgressIPStatus](#egressipstatus)_ | Observed status of EgressIP. Read-only. |  | Optional: {} <br /> |


#### EgressIPSpec



EgressIPSpec is a desired state description of EgressIP.



_Appears in:_
- [EgressIP](#egressip)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `egressIPs` _string array_ | EgressIPs is the list of egress IP addresses requested. Can be IPv4 and/or IPv6.<br />This field is mandatory. |  |  |
| `namespaceSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta)_ | NamespaceSelector applies the egress IP only to the namespace(s) whose label<br />matches this definition. This field is mandatory. |  |  |
| `podSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta)_ | PodSelector applies the egress IP only to the pods whose label<br />matches this definition. This field is optional, and in case it is not set:<br />results in the egress IP being applied to all pods in the namespace(s)<br />matched by the NamespaceSelector. In case it is set: is intersected with<br />the NamespaceSelector, thus applying the egress IP to the pods<br />(in the namespace(s) already matched by the NamespaceSelector) which<br />match this pod selector. |  | Optional: {} <br /> |


#### EgressIPStatus







_Appears in:_
- [EgressIP](#egressip)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `items` _[EgressIPStatusItem](#egressipstatusitem) array_ | The list of assigned egress IPs and their corresponding node assignment. |  |  |


#### EgressIPStatusItem



The per node status, for those egress IPs who have been assigned.



_Appears in:_
- [EgressIPStatus](#egressipstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `node` _string_ | Assigned node name |  |  |
| `egressIP` _string_ | Assigned egress IP |  |  |


