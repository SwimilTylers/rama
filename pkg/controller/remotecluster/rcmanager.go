package remotecluster

import (
	"fmt"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)

func (c *Controller) startRemoteClusterMgr(clusterName string) error {
	klog.Infof("[debug] processNextRemoteClusterMgr name=%v", clusterName)
	rcManager, exists := c.remoteClusterManagerCache.Get(clusterName)
	if !exists {
		klog.Errorf("Can't find rcManager. clusterName=%v", clusterName)
		return errors.Errorf("Can't find rcManager. clusterName=%v", clusterName)
	}
	klog.Infof("Start single remote cluster manager. clusterName=%v", clusterName)

	managerCh := rcManager.StopCh
	go func() {
		if ok := cache.WaitForCacheSync(managerCh, rcManager.NodeSynced, rcManager.SubnetSynced, rcManager.IPSynced); !ok {
			klog.Errorf("failed to wait for remote cluster caches to sync. clusterName=%v", clusterName)
			return
		}
		go wait.Until(rcManager.RunNodeWorker, 1*time.Second, managerCh)
		go wait.Until(rcManager.RunSubnetWorker, 1*time.Second, managerCh)
		go wait.Until(rcManager.RunIPInstanceWorker, 1*time.Second, managerCh)
	}()
	go rcManager.KubeInformerFactory.Start(managerCh)
	go rcManager.RamaInformerFactory.Start(managerCh)
	return nil
}

func (c *Controller) processRCManagerQueue() {
	for c.processNextRemoteClusterMgr() {
	}
}

func (c *Controller) processNextRemoteClusterMgr() bool {
	defer func() {
		if err := recover(); err != nil {
			klog.Errorf("processNextRemoteClusterMgr panic. err=%v", err)
		}
	}()

	obj, shutdown := c.rcManagerQueue.Get()
	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		defer c.rcManagerQueue.Done(obj)
		var (
			key string
			ok  bool
		)
		if key, ok = obj.(string); !ok {
			c.rcManagerQueue.Forget(obj)
			return nil
		}
		if err := c.startRemoteClusterMgr(key); err != nil {
			// TODO: use retry handler to
			// Put the item back on the workqueue to handle any transient errors
			c.rcManagerQueue.AddRateLimited(key)
			return fmt.Errorf("[remote cluster mgr] fail to sync '%v': %v, requeuing", key, err)
		}
		c.rcManagerQueue.Forget(obj)
		klog.Infof("succeed to sync '%v'", key)
		return nil
	}(obj)

	if err != nil {
		klog.Error(err)
	}

	return true
}
