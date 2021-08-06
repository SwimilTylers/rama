package remotecluster

import (
	"context"
	"runtime/debug"
	"strings"

	apiv1 "k8s.io/api/core/v1"

	networkingv1 "github.com/oecp/rama/pkg/apis/networking/v1"
	"github.com/oecp/rama/pkg/client/clientset/versioned"
	"github.com/oecp/rama/pkg/utils"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeutil "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog"
)

type CheckStatusFunc func(c *Controller, rcRamaClient *versioned.Clientset, clusterName string) ([]networkingv1.ClusterCondition, error)

var (
	InitializeChecker []CheckStatusFunc
	AllReady          = make(map[networkingv1.ClusterConditionType]bool)
)

func init() {
	InitializeChecker = append(InitializeChecker, HealChecker, BidirectionalConnChecker, OverlayNetIDChecker)

	AllReady[utils.TypeHealthCheck] = true
	AllReady[utils.TypeBidirectionalConn] = true
	AllReady[utils.TypeSameOverlayNetID] = true
}

func CheckCondition(c *Controller, ramaClient *versioned.Clientset, clusterName string,
	checkers []CheckStatusFunc) []networkingv1.ClusterCondition {
	conditions := make([]networkingv1.ClusterCondition, 0)
	for _, checker := range checkers {
		cond, err := checker(c, ramaClient, clusterName)
		if err != nil {
			break
		}
		conditions = append(conditions, cond...)
	}
	return conditions
}

func MeetCondition(conditions []networkingv1.ClusterCondition) bool {
	s := 0
	for _, c := range conditions {
		if _, exists := AllReady[c.Type]; exists {
			if c.Status == apiv1.ConditionTrue {
				s = s + 1
			}
		}
	}
	if s == len(AllReady) {
		return true
	}
	return false
}

func HealChecker(c *Controller, ramaClient *versioned.Clientset, clusterName string) ([]networkingv1.ClusterCondition, error) {
	conditions := make([]networkingv1.ClusterCondition, 0)

	body, err := ramaClient.DiscoveryClient.RESTClient().Get().AbsPath("/healthz").Do(context.TODO()).Raw()
	if err != nil {
		runtimeutil.HandleError(errors.Wrapf(err, "Cluster Health Check failed for cluster %v", clusterName))
		conditions = append(conditions, utils.NewClusterOffline(err))
		return conditions, err
	} else {
		if !strings.EqualFold(string(body), "ok") {
			conditions = append(conditions, utils.NewHealthCheckNotReady(err), utils.NewClusterNotOffline())
		} else {
			conditions = append(conditions, utils.NewHealthCheckReady())
		}
	}
	return conditions, nil
}

// BidirectionalConnChecker check if remote cluster has create the remote cluster
func BidirectionalConnChecker(c *Controller, ramaClient *versioned.Clientset, clusterName string) ([]networkingv1.ClusterCondition, error) {
	conditions := make([]networkingv1.ClusterCondition, 0)

	rcs, err := ramaClient.NetworkingV1().RemoteClusters().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		runtimeutil.HandleError(err)
		conditions = append(conditions, utils.NewBidirectionalConnNotReady(err.Error()))
		return conditions, nil
	}
	doubleEndedConn := false
	for _, v := range rcs.Items {
		// has not set uuid, check next time
		if v.Status.UUID == "" {
			continue
		}
		if v.Status.UUID == c.UUID {
			doubleEndedConn = true
			break
		}
	}
	if !doubleEndedConn {
		klog.Warningf("The peer cluster has not created remote cluster. ClusterName=%v", clusterName)
		conditions = append(conditions, utils.NewBidirectionalConnNotReady(utils.MsgBidirectionalConnNotOk))
	} else {
		conditions = append(conditions, utils.NewBidirectionalConnReady())
	}
	return conditions, nil
}

func OverlayNetIDChecker(c *Controller, ramaClient *versioned.Clientset, clusterName string) ([]networkingv1.ClusterCondition, error) {
	defer func() {
		if err := recover(); err != nil {
			klog.Errorf("OverlayNetIDChecker panic. err=%v\n%v", err, debug.Stack())
		}
	}()
	conditions := make([]networkingv1.ClusterCondition, 0)

	networkList, err := ramaClient.NetworkingV1().Networks().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		runtimeutil.HandleError(err)
		conditions = append(conditions, utils.NewOverlayNetIDNotReady(err.Error()))
		return conditions, nil
	}
	var overlayNetID uint32
	for _, item := range networkList.Items {
		if item.Spec.Type == networkingv1.NetworkTypeOverlay && item.Spec.NetID != nil {
			overlayNetID = *item.Spec.NetID
			break
		}
	}
	c.overlayNetIDMU.RLock()
	defer c.overlayNetIDMU.RUnlock()

	if c.OverlayNetID == nil {
		conditions = append(conditions, utils.NewOverlayNetIDNotReady("local cluster has no overlay network"))
	} else if *c.OverlayNetID != overlayNetID {
		conditions = append(conditions, utils.NewOverlayNetIDNotReady("Different overlay net id"))
	} else {
		conditions = append(conditions, utils.NewOverlayNetIDReady())
	}
	return conditions, nil
}
