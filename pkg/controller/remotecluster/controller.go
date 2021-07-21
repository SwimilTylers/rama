package remotecluster

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	networkingv1 "github.com/oecp/rama/pkg/apis/networking/v1"
	"github.com/oecp/rama/pkg/client/clientset/versioned"
	"github.com/oecp/rama/pkg/client/informers/externalversions"
	informers "github.com/oecp/rama/pkg/client/informers/externalversions/networking/v1"
	listers "github.com/oecp/rama/pkg/client/listers/networking/v1"
	"github.com/oecp/rama/pkg/rcmanager"
	"github.com/oecp/rama/pkg/utils"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	runtimeutil "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

const (
	ControllerName = "remotecluster"

	// HealthCheckPeriod Every HealthCheckPeriod will resync remote cluster cache and check rc
	// health. Default: 20 second. Set to zero will also use the default value
	HealthCheckPeriod = 20 * time.Second
)

type Controller struct {
	kubeClient          kubeclientset.Interface
	ramaClient          versioned.Interface
	RamaInformerFactory externalversions.SharedInformerFactory

	remoteClusterLister      listers.RemoteClusterLister
	remoteClusterSynced      cache.InformerSynced
	remoteClusterQueue       workqueue.RateLimitingInterface
	remoteSubnetLister       listers.RemoteSubnetLister
	remoteSubnetSynced       cache.InformerSynced
	remoteVtepLister         listers.RemoteVtepLister
	remoteVtepSynced         cache.InformerSynced
	localClusterSubnetLister listers.SubnetLister
	localClusterSubnetSynced cache.InformerSynced

	remoteClusterManagerCache Cache
	recorder                  record.EventRecorder
	rcManagerQueue            workqueue.RateLimitingInterface
}

func NewController(
	kubeClient kubeclientset.Interface,
	ramaClient versioned.Interface,
	remoteClusterInformer informers.RemoteClusterInformer,
	remoteSubnetInformer informers.RemoteSubnetInformer,
	localClusterSubnetInformer informers.SubnetInformer,
	remoteVtepInformer informers.RemoteVtepInformer) *Controller {
	runtimeutil.Must(networkingv1.AddToScheme(scheme.Scheme))

	klog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(klog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: ControllerName})

	c := &Controller{
		remoteClusterManagerCache: Cache{
			remoteClusterMap: make(map[string]*rcmanager.Manager),
		},
		kubeClient:               kubeClient,
		ramaClient:               ramaClient,
		remoteClusterLister:      remoteClusterInformer.Lister(),
		remoteClusterSynced:      remoteClusterInformer.Informer().HasSynced,
		remoteSubnetLister:       remoteSubnetInformer.Lister(),
		remoteSubnetSynced:       remoteSubnetInformer.Informer().HasSynced,
		localClusterSubnetLister: localClusterSubnetInformer.Lister(),
		localClusterSubnetSynced: localClusterSubnetInformer.Informer().HasSynced,
		remoteVtepLister:         remoteVtepInformer.Lister(),
		remoteVtepSynced:         remoteSubnetInformer.Informer().HasSynced,
		remoteClusterQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		rcManagerQueue:           workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "remoteclustermanager"),
		recorder:                 recorder,
	}

	remoteClusterInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: c.filterRemoteCluster,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    c.addOrDelRemoteCluster,
			UpdateFunc: c.updateRemoteCluster,
			DeleteFunc: c.addOrDelRemoteCluster,
		},
	})

	return c
}

func (c *Controller) Run(stopCh <-chan struct{}) error {
	defer runtimeutil.HandleCrash()
	defer c.rcManagerQueue.ShutDown()
	defer c.remoteClusterQueue.ShutDown()

	klog.Infof("Starting %s controller", ControllerName)

	klog.Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.remoteClusterSynced, c.remoteSubnetSynced, c.remoteVtepSynced, c.localClusterSubnetSynced); !ok {
		return fmt.Errorf("%s failed to wait for caches to sync", ControllerName)
	}

	// start workers
	klog.Info("Starting workers")
	go wait.Until(c.runRemoteClusterWorker, time.Second, stopCh)
	go wait.Until(c.processRCManagerQueue, time.Second, stopCh)
	// wait until remote cluster mgr finish initializing
	time.Sleep(5 * time.Second)
	go wait.Until(c.updateRemoteClusterStatus, HealthCheckPeriod, stopCh)
	<-stopCh

	c.closeRemoteClusterManager()

	klog.Info("Shutting down workers")
	return nil
}

func (c *Controller) closeRemoteClusterManager() {
	// no need to lock
	for _, rcManager := range c.remoteClusterManagerCache.remoteClusterMap {
		close(rcManager.StopCh)
	}
}

// health checking and resync cache. remote cluster is managed by admin, it can be
// treated as desired states
func (c *Controller) updateRemoteClusterStatus() {
	remoteClusters, err := c.remoteClusterLister.List(labels.NewSelector())
	if err != nil {
		klog.Errorf("Can't list remote cluster. err=%v", err)
		return
	}

	var wg sync.WaitGroup
	for _, rc := range remoteClusters {
		manager, exists := c.remoteClusterManagerCache.Get(rc.Name)
		if !exists {
			// error happened this time, add next time
			if err = c.addOrUpdateRemoteClusterManager(rc); err != nil {
				continue
			}
		}
		wg.Add(1)
		go c.healCheck(manager, rc, &wg)
	}
	wg.Wait()
	klog.Infof("Update Remote Cluster Status Finished. len=%v", len(c.remoteClusterManagerCache.remoteClusterMap))
}

// use remove+add instead of update
func (c *Controller) addOrUpdateRemoteClusterManager(rc *networkingv1.RemoteCluster) error {
	// lock in function range to avoid renewing cluster manager when newing one
	c.remoteClusterManagerCache.mu.Lock()
	defer c.remoteClusterManagerCache.mu.Unlock()

	clusterName := rc.Name
	if k, exists := c.remoteClusterManagerCache.remoteClusterMap[clusterName]; exists {
		klog.Infof("Delete cluster %v from cache", clusterName)
		close(k.StopCh)
		delete(c.remoteClusterManagerCache.remoteClusterMap, clusterName)
	}

	rcManager, err := rcmanager.NewRemoteClusterManager(rc, c.kubeClient, c.ramaClient, c.remoteSubnetLister,
		c.localClusterSubnetLister, c.remoteVtepLister)

	if err != nil || rcManager.RamaClient == nil || rcManager.KubeClient == nil {
		c.recorder.Eventf(rc, corev1.EventTypeWarning, "ErrClusterConnectionConfig",
			fmt.Sprintf("Can't connect to remote cluster %v", clusterName))
		return errors.Errorf("Can't connect to remote cluster %v", clusterName)
	}

	c.remoteClusterManagerCache.remoteClusterMap[clusterName] = rcManager
	c.rcManagerQueue.Add(clusterName)
	return nil
}

func (c *Controller) healCheck(manager *rcmanager.Manager, rc *networkingv1.RemoteCluster, wg *sync.WaitGroup) {
	rc = rc.DeepCopy()
	// todo metrics
	defer func() {
		if err := recover(); err != nil {
			klog.Errorf("healCheck panic. err=%v\n%v", err, string(debug.Stack()))
		}
	}()
	defer wg.Done()

	conditions := make([]networkingv1.ClusterCondition, 0)

	body, err := manager.KubeClient.DiscoveryClient.RESTClient().Get().AbsPath("/healthz").Do(context.TODO()).Raw()
	if err != nil {
		runtimeutil.HandleError(errors.Wrapf(err, "Cluster Health Check failed for cluster %v", manager.ClusterName))
		conditions = append(conditions, utils.NewClusterOffline(err))
	} else {
		if !strings.EqualFold(string(body), "ok") {
			conditions = append(conditions, utils.NewClusterNotReady(err), utils.NewClusterNotOffline())
		} else {
			conditions = append(conditions, utils.NewClusterReady())
		}
	}

	updateCondition := func() {
		conditionChanged := false
		if len(conditions) != len(rc.Status.Conditions) {
			conditionChanged = true
		} else {
			for i := range conditions {
				if conditions[i].Status == rc.Status.Conditions[i].Status &&
					conditions[i].Type == rc.Status.Conditions[i].Type {
					continue
				} else {
					conditionChanged = true
					break
				}
			}
		}
		if !conditionChanged {
			for i := range rc.Status.Conditions {
				conditions[i].LastTransitionTime = rc.Status.Conditions[i].LastTransitionTime
			}
		}
		rc.Status.Conditions = conditions
	}
	updateCondition()

	_, err = c.ramaClient.NetworkingV1().RemoteClusters().UpdateStatus(context.TODO(), rc, metav1.UpdateOptions{})
	if err != nil {
		klog.Warningf("[health check] can't update remote cluster. err=%v", err)
	}
}
