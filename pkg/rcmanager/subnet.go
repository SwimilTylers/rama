package rcmanager

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	networkingv1 "github.com/oecp/rama/pkg/apis/networking/v1"
	"github.com/oecp/rama/pkg/constants"
	"github.com/oecp/rama/pkg/utils"
	"gopkg.in/errgo.v2/fmt/errors"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog"
	"k8s.io/utils/pointer"
)

func (m *Manager) reconcileSubnet(key string) error {
	klog.Infof("[remote cluster] Starting reconcile subnet from cluster %v, subnet name=%v", m.ClusterName, key)
	if len(key) == 0 {
		return nil
	}
	subnet, err := m.subnetLister.Get(key)
	if err != nil {
		if k8serror.IsNotFound(err) {
			name := utils.GenRemoteSubnetName(m.ClusterName, key)
			err = m.localClusterRamaClient.NetworkingV1().RemoteSubnets().Delete(context.TODO(), name, metav1.DeleteOptions{})
			if k8serror.IsNotFound(err) {
				return nil
			}
		}
		return err
	}

	localClusterSubnets, err := m.localClusterSubnetLister.List(labels.NewSelector())
	if err != nil {
		return err
	}
	localClusterRemoteSubnets, err := m.remoteSubnetLister.List(utils.SelectorClusterName(m.ClusterName))
	if err != nil {
		return err
	}
	remoteClusterSubnets, err := m.subnetLister.List(labels.NewSelector())
	if err != nil {
		return err
	}
	networks, err := m.networkLister.List(labels.NewSelector())
	if err != nil {
		return err
	}
	networkMap := func() map[string]*networkingv1.Network {
		networkMap := make(map[string]*networkingv1.Network)
		for _, network := range networks {
			networkMap[network.Name] = network
		}
		return networkMap
	}()

	if err = m.validateRemoteClusterOverlap(subnet, localClusterRemoteSubnets); err != nil {
		return err
	}
	if err = m.validateLocalClusterOverlap(subnet, localClusterSubnets); err != nil {
		return err
	}

	add, update, remove := m.diffSubnetAndRCSubnet(remoteClusterSubnets, localClusterRemoteSubnets, networkMap)
	var (
		wg  sync.WaitGroup
		cur = metav1.Now()
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		for _, v := range add {
			rcSubnet, err := m.convertSubnet2RemoteSubnet(v, networkMap[v.Spec.Network])
			if err != nil {
				klog.Warningf("convertSubnet2RemoteSubnet error. err=%v. subnet name=%v. ClusterID=%v", err, v.Name, m.ClusterName)
				continue
			}
			newSubnet, err := m.localClusterRamaClient.NetworkingV1().RemoteSubnets().Create(context.TODO(), rcSubnet, metav1.CreateOptions{})
			if err != nil {
				klog.Warningf("Can't create remote subnet in local cluster. err=%v. remote subnet name=%v", err, rcSubnet.Name)
				continue
			}
			newSubnet.Status.LastModifyTime = cur
			_, err = m.localClusterRamaClient.NetworkingV1().RemoteSubnets().UpdateStatus(context.TODO(), newSubnet, metav1.UpdateOptions{})
			if err != nil {
				klog.Warningf("Can't UpdateStatus remote subnet in local cluster. err=%v. remote subnet name=%v", err, rcSubnet.Name)
			}
		}
	}()

	go func() {
		defer wg.Done()
		for _, v := range update {
			var newRemoteSubnet *networkingv1.RemoteSubnet
			err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				newRemoteSubnet, err = m.localClusterRamaClient.NetworkingV1().RemoteSubnets().Update(context.TODO(), v, metav1.UpdateOptions{})
				if err != nil {
					return err
				}
				newRemoteSubnet.Status.LastModifyTime = cur
				_, err = m.localClusterRamaClient.NetworkingV1().RemoteSubnets().UpdateStatus(context.TODO(), newRemoteSubnet, metav1.UpdateOptions{})
				return err
			})
			if err != nil {
				klog.Warningf("Can't update remote subnet in local cluster. err=%v. name=%v", err, v.Name)
				continue
			}
		}
	}()

	go func() {
		defer wg.Done()
		for _, v := range remove {
			_ = m.localClusterRamaClient.NetworkingV1().RemoteSubnets().Delete(context.TODO(), v.Name, metav1.DeleteOptions{})
			if err != nil && !k8serror.IsNotFound(err) {
				klog.Warningf("Can't delete remote subnet in local cluster. remote subnet name=%v", v.Name)
			}
		}
	}()
	wg.Wait()
	return nil
}

func (m *Manager) RunSubnetWorker() {
	for m.processNextSubnet() {
	}
}

// validate only in local cluster
func (m *Manager) validateLocalClusterOverlap(subnet *networkingv1.Subnet, subnets []*networkingv1.Subnet) error {
	for _, s := range subnets {
		if utils.Intersect(subnet.Spec.Range.CIDR, subnet.Spec.Range.Version, s.Spec.Range.CIDR, s.Spec.Range.Version) {
			klog.Errorf("Two subnet intersect. One is from cluster %v, cidr=%v. Another is from lcoal cluster, cidr=%v",
				m.ClusterName, subnet.Spec.Range.CIDR, s.Spec.Range.CIDR)
			return errors.Newf("Overlap network. overlap with other local cluster subnet")
		}
	}
	return nil
}

// validate whether the subnet to be added is conflict with remoteSubnet in localCluster
// validate between all connected domain expect local cluster
func (m *Manager) validateRemoteClusterOverlap(subnet *networkingv1.Subnet, rcSubnets []*networkingv1.RemoteSubnet) error {
	for _, rc := range rcSubnets {
		if rc.Name == utils.GenRemoteSubnetName(m.ClusterName, subnet.Name) {
			continue
		}
		if utils.Intersect(rc.Spec.Range.CIDR, rc.Spec.Range.Version, subnet.Spec.Range.CIDR, subnet.Spec.Range.Version) {
			klog.Errorf("Two subnet intersect. One is from cluster %v, cidr=%v. Another is from cluster %v, cidr=%v",
				m.ClusterName, subnet.Spec.Range.CIDR, rc.Spec.ClusterName, rc.Spec.Range.CIDR)
			return errors.Newf("Overlap network. overlap with other remoteSubnet")
		}
	}
	return nil
}

// Reconcile local cluster *networkingv1.RemoteSubnet based on remote cluster's subnet.
func (m *Manager) diffSubnetAndRCSubnet(subnets []*networkingv1.Subnet, rcSubnets []*networkingv1.RemoteSubnet,
	networkMap map[string]*networkingv1.Network) (
	add []*networkingv1.Subnet, update []*networkingv1.RemoteSubnet, remove []*networkingv1.RemoteSubnet) {
	subnetMap := func() map[string]*networkingv1.Subnet {
		subnetMap := make(map[string]*networkingv1.Subnet)
		for _, s := range subnets {
			subnetMap[s.Name] = s
		}
		return subnetMap
	}()

	for _, v := range rcSubnets {
		if v.ClusterName != m.ClusterName {
			continue
		}
		if subnet, exists := subnetMap[v.Name]; !exists {
			remove = append(remove, v)
		} else {
			newestRemoteSubnet, err := m.convertSubnet2RemoteSubnet(subnet, networkMap[subnet.Spec.Network])
			if err != nil {
				continue
			}
			if !reflect.DeepEqual(newestRemoteSubnet.Spec, v.Spec) {
				update = append(update, newestRemoteSubnet)
			}
		}
	}
	remoteSubnetMap := func() map[string]*networkingv1.RemoteSubnet {
		remoteSubnetMap := make(map[string]*networkingv1.RemoteSubnet)
		for _, s := range rcSubnets {
			remoteSubnetMap[s.Name] = s
		}
		return remoteSubnetMap
	}()

	for _, s := range subnets {
		remoteSubnetName := utils.GenRemoteSubnetName(m.ClusterName, s.Name)
		if _, exists := remoteSubnetMap[remoteSubnetName]; !exists {
			add = append(add, s)
		}
	}
	return
}

func (m *Manager) convertSubnet2RemoteSubnet(subnet *networkingv1.Subnet, network *networkingv1.Network) (*networkingv1.RemoteSubnet, error) {
	if network == nil {
		return nil, errors.Newf("Subnet corresponding Network is nil. Subnet=%v, Cluster=%v", subnet.Name, m.ClusterName)
	}
	rs := &networkingv1.RemoteSubnet{
		ObjectMeta: metav1.ObjectMeta{
			Name: utils.GenRemoteSubnetName(m.ClusterName, subnet.Name),
			Labels: map[string]string{
				constants.LabelCluster: m.ClusterName,
				constants.LabelSubnet:  subnet.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: networkingv1.SchemeGroupVersion.String(),
					Kind:       "RemoteCluster",
					Name:       m.ClusterName,
					UID:        m.RemoteClusterUID,
					Controller: pointer.BoolPtr(true),
				},
			},
		},
		Spec: networkingv1.RemoteSubnetSpec{
			Range:       subnet.Spec.Range,
			Type:        network.Spec.Type,
			ClusterName: m.ClusterName,
			TunnelNetID: network.Spec.NetID,
		},
	}
	return rs, nil
}

func (m *Manager) filterSubnet(obj interface{}) bool {
	if !m.GetMeetCondition() {
		return false
	}
	_, ok := obj.(*networkingv1.Subnet)
	return ok
}

func (m *Manager) addOrDelSubnet(obj interface{}) {
	subnet, _ := obj.(*networkingv1.Subnet)
	m.enqueueSubnet(subnet.ObjectMeta.Name)
}

func (m *Manager) updateSubnet(oldObj, newObj interface{}) {
	oldRC, _ := oldObj.(*networkingv1.Subnet)
	newRC, _ := newObj.(*networkingv1.Subnet)

	if oldRC.ResourceVersion == newRC.ResourceVersion {
		return
	}
	m.enqueueSubnet(newRC.ObjectMeta.Name)
}

func (m *Manager) enqueueSubnet(subnetName string) {
	m.subnetQueue.Add(subnetName)
}

func (m *Manager) processNextSubnet() bool {
	obj, shutdown := m.subnetQueue.Get()
	if shutdown {
		return false
	}

	err := func(obj interface{}) error {
		defer m.subnetQueue.Done(obj)
		var (
			key string
			ok  bool
		)
		if key, ok = obj.(string); !ok {
			m.subnetQueue.Forget(obj)
			return nil
		}
		if err := m.reconcileSubnet(key); err != nil {
			// TODO: use retry handler to
			// Put the item back on the workqueue to handle any transient errors
			m.subnetQueue.AddRateLimited(key)
			return fmt.Errorf("[subnet] fail to sync '%s' for cluster id=%v: %v, requeuing", key, m.ClusterName, err)
		}
		m.subnetQueue.Forget(obj)
		klog.Infof("[subnet] succeed to sync '%s', cluster id=%v", key, m.ClusterName)
		return nil
	}(obj)

	if err != nil {
		klog.Error(err)
	}

	return true
}
