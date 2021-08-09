package remotecluster

import (
	"sync"

	"github.com/oecp/rama/pkg/rcmanager"
	"k8s.io/klog"
)

type Cache struct {
	mu               sync.RWMutex
	remoteClusterMap map[string]*rcmanager.Manager
}

func (c *Cache) Get(clusterName string) (manager *rcmanager.Manager, exists bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	manager, exists = c.remoteClusterMap[clusterName]
	return
}

func (c *Cache) Set(clusterName string, manager *rcmanager.Manager) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.remoteClusterMap[clusterName] = manager
}

func (c *Cache) Del(clusterName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rc, exists := c.remoteClusterMap[clusterName]; exists {
		klog.Infof("Delete cluster %v from cache", clusterName)
		close(rc.StopCh)
		delete(c.remoteClusterMap, clusterName)
	}
}
