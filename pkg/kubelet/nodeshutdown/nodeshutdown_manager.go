package nodeshutdown

import (
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	v1 "k8s.io/api/core/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubelet/eviction"
	"k8s.io/kubernetes/pkg/kubelet/nodeshutdown/systemd"
	kubelettypes "k8s.io/kubernetes/pkg/kubelet/types"
	"k8s.io/utils/integer"
)

const (
	NodeShutdownReason  = "Shutdown"
	NodeShutdownMessage = "Node is shutting, evicting pods"
)

type Manager struct {
	getPods eviction.ActivePodsFunc
	killPod eviction.KillPodFunc

	shutdownGracePeriodRequested    time.Duration
	shutdownGracePeriodAllocated    time.Duration
	shutdownGracePeriodCriticalPods time.Duration

	inhibitLock systemd.InhibitLock
	dbusCon     *systemd.DBusCon

	nodeShuttingDownNow bool
}

func NewManager(getPodsFunc eviction.ActivePodsFunc, killPodFunc eviction.KillPodFunc, shutdownGracePeriodRequested, shutdownGracePeriodCriticalPods time.Duration) *Manager {
	return &Manager{
		getPods:                         getPodsFunc,
		killPod:                         killPodFunc,
		shutdownGracePeriodRequested:    shutdownGracePeriodRequested,
		shutdownGracePeriodCriticalPods: shutdownGracePeriodCriticalPods,
	}
}

func (m *Manager) Start() error {
	if !utilfeature.DefaultFeatureGate.Enabled(features.GracefulNodeShutdown) {
		klog.V(0).Infof("porterdavid feature gate disabled")
		return nil
	}

	klog.V(0).Infof("porterdavid shutdownGracePeriodRequested: %v   shutdownGracePeriodCriticalPods: %v", m.shutdownGracePeriodRequested, m.shutdownGracePeriodCriticalPods)
	if m.shutdownGracePeriodRequested == 0 {
		klog.V(0).Info("shutdown duration was zero, skipping shutdown inhibit setup")
	}

	systemBus, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	m.dbusCon = &systemd.DBusCon{DBusConnector: systemBus}

	klog.Infof("porterdavid: shutdown info: shutdownGracePeriodRequested: %v ; shutdownGracePeriodCriticalPods: %v", m.shutdownGracePeriodRequested, m.shutdownGracePeriodCriticalPods)

	if m.shutdownGracePeriodRequested == 0 {
		klog.V(0).Info("shutdown duration was zero, skipping shutdown inhibit setup")
		return nil
	}

	maxInhibitDelay, err := m.dbusCon.CurrentInhibitDelay()
	if err != nil {
		return err
	}

	if m.shutdownGracePeriodRequested > maxInhibitDelay {
		// write config file
		err := systemd.OverrideSystemdInhibitDelay(m.shutdownGracePeriodRequested)
		if err != nil {
			return err
		}
		klog.V(0).Infof("porterdavid: writing inhibit file")

		err = m.dbusCon.ReloadLogindConf()
		if err != nil {
			return err
		}
		klog.V(0).Infof("porterdavid: reloaded logind")

		// read the maxInhibitDelay again, hopefuly updated now
		maxInhibitDelay, err = m.dbusCon.CurrentInhibitDelay()
		if err != nil {
			return err
		}
	}

	klog.V(0).Infof("porterdavid: inhibitDelay: %v shutDownConfig: %v", maxInhibitDelay, m.shutdownGracePeriodRequested)

	m.shutdownGracePeriodAllocated = time.Duration(integer.Int64Min(maxInhibitDelay.Nanoseconds(), m.shutdownGracePeriodRequested.Nanoseconds()))

	if m.shutdownGracePeriodAllocated < m.shutdownGracePeriodRequested {
		klog.Warningf("Node shutdown manager was unable to use %v as shutdownGracePeriodRequested due to unable being to override logind inhibit config, using %v instead", m.shutdownGracePeriodAllocated)
	}

	klog.V(0).Infof("porterdavid: shutdownGracePeriodAllocated %v", m.shutdownGracePeriodAllocated)

	err = m.aquireInhibitLock()
	if err != nil {
		return err
	}

	events, err := m.dbusCon.MonitorShutdown()
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case isShuttingDown := <-events:
				klog.V(0).Infof("porterdavid: Shutdown manager detected new shutdown event, isNodeShuttingDownNow: %t", isShuttingDown)
				m.nodeShuttingDownNow = isShuttingDown
				if isShuttingDown {
					m.processShutdownEvent()
				} else {
					m.aquireInhibitLock()
				}
			}
		}
	}()
	return nil
}

func (m *Manager) ShutdownStatus() error {
	if !utilfeature.DefaultFeatureGate.Enabled(features.GracefulNodeShutdown) {
		return nil
	}

	klog.V(0).Infof("porterdavid: shutdown status called")
	if m.nodeShuttingDownNow {
		klog.V(0).Infof("porterdavid: Shutdown status returning ERROR")
		return fmt.Errorf("Node is shutting down")
	}
	return nil
}

func (m *Manager) aquireInhibitLock() error {
	lock, err := m.dbusCon.InhibitShutdown()
	if err != nil {
		return err
	}
	m.inhibitLock = lock
	return nil
}

func (m *Manager) processShutdownEvent() error {
	klog.V(0).Infof("porterdavid: processShutdownEvent")
	activePods := m.getPods()

	nonCriticalPodGracePeriod := m.shutdownGracePeriodAllocated - m.shutdownGracePeriodCriticalPods

	klog.V(0).Infof("porterdavid: processShutdownEvent m.shutdownGracePeriodCriticalPods %v ; nonCriticalPodGracePeriod: %v", m.shutdownGracePeriodCriticalPods, nonCriticalPodGracePeriod)

	var wg sync.WaitGroup
	for _, pod := range activePods {
		go func(pod *v1.Pod) {
			defer wg.Done()

			var gracePeriodOverride int64
			if kubelettypes.IsCriticalPod(pod) {
				gracePeriodOverride = int64(m.shutdownGracePeriodCriticalPods.Seconds())
				time.Sleep(nonCriticalPodGracePeriod)
			} else {
				gracePeriodOverride = int64(nonCriticalPodGracePeriod.Seconds())
			}

			klog.V(0).Infof("porterdavid: killing pod %v gracePeriod: %v", pod.Name, gracePeriodOverride)

			status := v1.PodStatus{
				Phase:   v1.PodFailed,
				Message: NodeShutdownMessage,
				Reason:  NodeShutdownReason,
			}

			err := m.killPod(pod, status, &gracePeriodOverride)
			if err != nil {
				klog.V(0).Infof("error killing pod %q: %v", pod.Name, err)
			}
			klog.V(0).Infof("porterdavid: done killing pod %v", pod.Name)
		}(pod)
		wg.Add(1)
	}
	wg.Wait()

	// TODO(porterdavid): release the lock and print smthing
	klog.V(0).Infof("porterdavid: release inhibit lock start")
	m.dbusCon.ReleaseInhibitLock(m.inhibitLock)
	klog.V(0).Infof("porterdavid: release inhibit lock done")

	return nil
}
