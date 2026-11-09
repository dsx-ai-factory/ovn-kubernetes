# API Reference

## Packages
- [k8s.ovn.org/types](#k8sovnorgtypes)


## k8s.ovn.org/types

Package types contains shared types across API Schema definitions



#### ClusterUserDefinedNetworkSelector



ClusterUserDefinedNetworkSelector selects ClusterUserDefinedNetworks.



_Appears in:_
- [NetworkSelector](#networkselector)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `networkSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta)_ | networkSelector selects ClusterUserDefinedNetworks by label. A null<br />selector will mot match anything, while an empty (\{\}) selector will match<br />all. |  | Required: {} <br /> |


#### NetworkAttachmentDefinitionSelector



NetworkAttachmentDefinitionSelector selects networks defined in the selected NetworkAttachmentDefinitions.



_Appears in:_
- [NetworkSelector](#networkselector)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `namespaceSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta)_ | namespaceSelector selects namespaces where the<br />NetworkAttachmentDefinitions are defined. This field follows standard<br />label selector semantics. |  | Required: {} <br /> |
| `networkSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta)_ | networkSelector selects NetworkAttachmentDefinitions within the selected<br />namespaces by label. This field follows standard label selector<br />semantics. |  | Required: {} <br /> |


#### NetworkSelectionType

_Underlying type:_ _string_

NetworkSelectionType determines the type of networks selected.

_Validation:_
- Enum: [DefaultNetwork ClusterUserDefinedNetworks PrimaryUserDefinedNetworks SecondaryUserDefinedNetworks NetworkAttachmentDefinitions]

_Appears in:_
- [NetworkSelector](#networkselector)

| Field | Description |
| --- | --- |
| `DefaultNetwork` | DefaultNetwork determines that the default pod network is selected.<br /> |
| `ClusterUserDefinedNetworks` | ClusterUserDefinedNetworks determines that ClusterUserDefinedNetworks are selected.<br /> |
| `PrimaryUserDefinedNetworks` | PrimaryUserDefinedNetworks determines that primary UserDefinedNetworks are selected.<br /> |
| `SecondaryUserDefinedNetworks` | SecondaryUserDefinedNetworks determines that secondary UserDefinedNetworks are selected.<br /> |
| `NetworkAttachmentDefinitions` | NetworkAttachmentDefinitions determines that networks defined in NetworkAttachmentDefinitions are selected.<br /> |


#### NetworkSelector



NetworkSelector selects a set of networks.



_Appears in:_
- [NetworkSelectors](./networkselectors-api-spec.md#networkselectors)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `networkSelectionType` _[NetworkSelectionType](#networkselectiontype)_ | networkSelectionType determines the type of networks selected. |  | Enum: [DefaultNetwork ClusterUserDefinedNetworks PrimaryUserDefinedNetworks SecondaryUserDefinedNetworks NetworkAttachmentDefinitions] <br />Required: {} <br /> |
| `clusterUserDefinedNetworkSelector` _[ClusterUserDefinedNetworkSelector](#clusteruserdefinednetworkselector)_ | clusterUserDefinedNetworkSelector selects ClusterUserDefinedNetworks when<br />NetworkSelectionType is 'ClusterUserDefinedNetworks'. |  | Optional: {} <br /> |
| `primaryUserDefinedNetworkSelector` _[PrimaryUserDefinedNetworkSelector](#primaryuserdefinednetworkselector)_ | primaryUserDefinedNetworkSelector selects primary UserDefinedNetworks when<br />NetworkSelectionType is 'PrimaryUserDefinedNetworks'. |  | Optional: {} <br /> |
| `secondaryUserDefinedNetworkSelector` _[SecondaryUserDefinedNetworkSelector](#secondaryuserdefinednetworkselector)_ | secondaryUserDefinedNetworkSelector selects secondary UserDefinedNetworks<br />when NetworkSelectionType is 'SecondaryUserDefinedNetworks'. |  | Optional: {} <br /> |
| `networkAttachmentDefinitionSelector` _[NetworkAttachmentDefinitionSelector](#networkattachmentdefinitionselector)_ | networkAttachmentDefinitionSelector selects networks defined in the<br />selected NetworkAttachmentDefinitions when NetworkSelectionType is<br />'NetworkAttachmentDefinitions'. |  | Optional: {} <br /> |


#### NetworkSelectors

_Underlying type:_ _[NetworkSelector](#networkselector)_

NetworkSelectors selects multiple sets of networks.

_Validation:_
- MaxItems: 5
- MinItems: 1



| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `networkSelectionType` _[NetworkSelectionType](#networkselectiontype)_ | networkSelectionType determines the type of networks selected. |  | Enum: [DefaultNetwork ClusterUserDefinedNetworks PrimaryUserDefinedNetworks SecondaryUserDefinedNetworks NetworkAttachmentDefinitions] <br />Required: {} <br /> |
| `clusterUserDefinedNetworkSelector` _[ClusterUserDefinedNetworkSelector](#clusteruserdefinednetworkselector)_ | clusterUserDefinedNetworkSelector selects ClusterUserDefinedNetworks when<br />NetworkSelectionType is 'ClusterUserDefinedNetworks'. |  | Optional: {} <br /> |
| `primaryUserDefinedNetworkSelector` _[PrimaryUserDefinedNetworkSelector](#primaryuserdefinednetworkselector)_ | primaryUserDefinedNetworkSelector selects primary UserDefinedNetworks when<br />NetworkSelectionType is 'PrimaryUserDefinedNetworks'. |  | Optional: {} <br /> |
| `secondaryUserDefinedNetworkSelector` _[SecondaryUserDefinedNetworkSelector](#secondaryuserdefinednetworkselector)_ | secondaryUserDefinedNetworkSelector selects secondary UserDefinedNetworks<br />when NetworkSelectionType is 'SecondaryUserDefinedNetworks'. |  | Optional: {} <br /> |
| `networkAttachmentDefinitionSelector` _[NetworkAttachmentDefinitionSelector](#networkattachmentdefinitionselector)_ | networkAttachmentDefinitionSelector selects networks defined in the<br />selected NetworkAttachmentDefinitions when NetworkSelectionType is<br />'NetworkAttachmentDefinitions'. |  | Optional: {} <br /> |


#### PrimaryUserDefinedNetworkSelector



PrimaryUserDefinedNetworkSelector selects primary UserDefinedNetworks.



_Appears in:_
- [NetworkSelector](#networkselector)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `namespaceSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta)_ | namespaceSelector select the primary UserDefinedNetworks that are servind<br />the selected namespaces. This field follows standard label selector<br />semantics. |  | Required: {} <br /> |


#### SecondaryUserDefinedNetworkSelector



SecondaryUserDefinedNetworkSelector selects secondary UserDefinedNetworks.



_Appears in:_
- [NetworkSelector](#networkselector)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `namespaceSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta)_ | namespaceSelector selects namespaces where the secondary<br />UserDefinedNetworks are defined. This field follows standard label<br />selector semantics. |  | Required: {} <br /> |
| `networkSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.28/#labelselector-v1-meta)_ | networkSelector selects secondary UserDefinedNetworks within the selected<br />namespaces by label. This field follows standard label selector<br />semantics. |  | Required: {} <br /> |


