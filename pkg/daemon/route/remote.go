package route

import (
	"fmt"
	"net"

	daemonutils "github.com/oecp/rama/pkg/daemon/utils"
)

const (
	RemoteOverlayNotExists = "#nonexist"
	RemoteOverlayConflict  = "#conflict"
)

func (m *Manager) ResetRemoteInfos() {
	m.remoteOverlaySubnetInfoMap = SubnetInfoMap{}
	m.remoteUnderlaySubnetInfoMap = SubnetInfoMap{}
	m.remoteOverlayIfName = RemoteOverlayNotExists
}

func (m *Manager) AddRemoteOverlaySubnetInfo(cidr *net.IPNet, gateway, start, end net.IP, excludeIPs []net.IP, forwardNodeIfName string) {
	m.addRemoteSubnetInfo(cidr, gateway, start, end, excludeIPs, forwardNodeIfName, true)
}

func (m *Manager) AddRemoteUnderlaySubnetInfo(cidr *net.IPNet, gateway, start, end net.IP, excludeIPs []net.IP) {
	m.addRemoteSubnetInfo(cidr, gateway, start, end, excludeIPs, "", false)
}

func (m *Manager) addRemoteSubnetInfo(cidr *net.IPNet, gateway, start, end net.IP, excludeIPs []net.IP,
	forwardNodeIfName string, isOverlay bool) {

	var subnetInfo *SubnetInfo
	if isOverlay {
		cidrString := cidr.String()
		if _, exists := m.remoteOverlaySubnetInfoMap[cidrString]; !exists {
			m.remoteOverlaySubnetInfoMap[cidrString] = &SubnetInfo{
				cidr:              cidr,
				forwardNodeIfName: forwardNodeIfName,
				gateway:           gateway,
				includedIPRanges:  []*daemonutils.IPRange{},
				excludeIPs:        []net.IP{},
			}
		}

		subnetInfo = m.remoteOverlaySubnetInfoMap[cidrString]
	} else {
		cidrString := cidr.String()
		if _, exists := m.remoteUnderlaySubnetInfoMap[cidrString]; !exists {
			m.remoteUnderlaySubnetInfoMap[cidrString] = &SubnetInfo{
				cidr:              cidr,
				forwardNodeIfName: forwardNodeIfName,
				gateway:           gateway,
				includedIPRanges:  []*daemonutils.IPRange{},
				excludeIPs:        []net.IP{},
			}
		}

		subnetInfo = m.remoteUnderlaySubnetInfoMap[cidrString]
	}

	if len(excludeIPs) != 0 {
		subnetInfo.excludeIPs = append(subnetInfo.excludeIPs, excludeIPs...)
	}

	if start != nil || end != nil {
		if start == nil {
			start = cidr.IP
		}

		if end == nil {
			end = daemonutils.LastIP(cidr)
		}

		if ipRange, _ := daemonutils.CreateIPRange(start, end); ipRange != nil {
			subnetInfo.includedIPRanges = append(subnetInfo.includedIPRanges, ipRange)
		}
	}

	if isOverlay {
		// fresh remoteOverlayIfName
		switch m.remoteOverlayIfName {
		case RemoteOverlayNotExists:
			m.remoteOverlayIfName = forwardNodeIfName
		case RemoteOverlayConflict:
		default:
			if m.remoteOverlayIfName != forwardNodeIfName {
				m.remoteOverlayIfName = RemoteOverlayConflict
			}
		}
	}
}

func (m *Manager) configureRemote() (bool, error) {
	if len(m.remoteOverlaySubnetInfoMap) == 0 && len(m.remoteUnderlaySubnetInfoMap) == 0 {
		return false, nil
	}

	if len(m.remoteOverlaySubnetInfoMap) != 0 {
		if m.remoteOverlayIfName != RemoteOverlayConflict &&
			m.remoteOverlayIfName != RemoteOverlayNotExists &&
			(m.overlayIfName == "" || m.remoteOverlayIfName == m.overlayIfName) {
			return true, nil
		}

		return false, fmt.Errorf("invalid remote overlay net interface configuration [local=%s, remote=%s]", m.overlayIfName, m.remoteOverlayIfName)
	}

	return true, nil
}

func (m *Manager) isValidRemoteOverlayIfName() bool {
	return m.remoteOverlayIfName != RemoteOverlayNotExists && m.remoteOverlayIfName != RemoteOverlayConflict
}
