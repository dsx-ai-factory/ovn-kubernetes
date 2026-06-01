// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nadconfig

import (
	"fmt"
	"reflect"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nadinformers "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/informers/externalversions/k8s.cni.cncf.io/v1"
	nadlisters "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/listers/k8s.cni.cncf.io/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/controller"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

// TBD SDN-3998 support dpu rate limiting for multiple secondary UDN pods
type NADInfo interface {
	// set per-NAD configration in case of any change
	SetNADConfig(nadName string, nadConfig *util.NADConfig) error
	// delete per-NAD configration when NAD is deleted
	DeleteNAD(nadName string)
}

// nadConfigController handles namespaced scoped NAD events and their associated per-NAD configuration
type NadConfigController struct {
	name       string
	nadLister  nadlisters.NetworkAttachmentDefinitionLister
	controller controller.Controller
	nadInfo    NADInfo
}

// Create controller to update per-NAD configuration
func NewController(name string, nadInformer nadinformers.NetworkAttachmentDefinitionInformer, nadInfo NADInfo) (*NadConfigController, error) {
	c := &NadConfigController{
		name:      name,
		nadLister: nadInformer.Lister(),
		nadInfo:   nadInfo,
	}

	config := &controller.ControllerConfig[nettypes.NetworkAttachmentDefinition]{
		RateLimiter:    workqueue.DefaultTypedControllerRateLimiter[string](),
		Informer:       nadInformer.Informer(),
		Lister:         c.nadLister.List,
		Reconcile:      c.reconcile,
		ObjNeedsUpdate: c.needsUpdate,
		Threadiness:    1,
	}

	c.controller = controller.NewController[nettypes.NetworkAttachmentDefinition](name, config)
	return c, nil
}

func (c *NadConfigController) initialSync() error {
	nads, err := c.nadLister.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to list objects: %w", err)
	}
	for _, nad := range nads {
		key, err := cache.MetaNamespaceKeyFunc(nad)
		if err != nil {
			return err
		}

		nadConfig, err := util.GetNADConfig(nad)
		if err != nil {
			return err
		}

		err = c.nadInfo.SetNADConfig(key, nadConfig)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *NadConfigController) Start() error {
	if err := controller.StartWithInitialSync(c.initialSync, c.controller); err != nil {
		return fmt.Errorf("failed to start %s controller: %w", c.name, err)
	}
	return nil
}

func (c *NadConfigController) Stop() {
	// c may be nil if the owning node network controller failed to
	// initialize before the NAD controller was created (e.g. gateway
	// init failure), and Stop() is then called during cleanup.
	if c == nil {
		return
	}
	controller.Stop(c.controller)
}

func (c *NadConfigController) reconcile(key string) error {
	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("nad-configuration-controller: failed splitting key %s: %v", key, err)
		return nil
	}

	nad, err := c.nadLister.NetworkAttachmentDefinitions(namespace).Get(name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		c.nadInfo.DeleteNAD(key)
		return nil
	}

	nadConfig, err := util.GetNADConfig(nad)
	if err != nil {
		return err
	}

	return c.nadInfo.SetNADConfig(key, nadConfig)
}

func (c *NadConfigController) needsUpdate(oldObj, newObj *nettypes.NetworkAttachmentDefinition) bool {
	if oldObj == nil || newObj == nil {
		return true
	}

	oldNADConfig, _ := util.GetNADConfig(oldObj)
	newNADConfig, _ := util.GetNADConfig(newObj)
	return !reflect.DeepEqual(oldNADConfig, newNADConfig)
}
