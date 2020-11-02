// +build linux

/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nodeshutdown

import (
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/clock"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubelet/eviction"
	"k8s.io/kubernetes/pkg/kubelet/nodeshutdown/systemd"
	kubelettypes "k8s.io/kubernetes/pkg/kubelet/types"
)

const (
	nodeShutdownReason  = "Shutdown"
	nodeShutdownMessage = "Node is shutting, evicting pods"
)

var getSystemDbus = systemDbus

type dbusInhibiter interface {
	CurrentInhibitDelay() (time.Duration, error)
	InhibitShutdown() (systemd.InhibitLock, error)
	ReleaseInhibitLock(lock systemd.InhibitLock) error
	ReloadLogindConf() error
	MonitorShutdown() (<-chan bool, error)
	OverrideInhibitDelay(inhibitDelayMax time.Duration) error
}

type Manager struct {
	getPods eviction.ActivePodsFunc
	killPod eviction.KillPodFunc

	shutdownGracePeriodRequested    time.Duration
	shutdownGracePeriodCriticalPods time.Duration

	inhibitLock systemd.InhibitLock
	dbusCon     dbusInhibiter

	clock               clock.Clock
	nodeShuttingDownNow bool
}

func systemDbus() (dbusInhibiter, error) {
	bus, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	return &systemd.DBusCon{SystemBus: bus}, nil
}

func NewManager(getPodsFunc eviction.ActivePodsFunc, killPodFunc eviction.KillPodFunc, shutdownGracePeriodRequested, shutdownGracePeriodCriticalPods time.Duration) *Manager {
	return &Manager{
		getPods:                         getPodsFunc,
		killPod:                         killPodFunc,
		shutdownGracePeriodRequested:    shutdownGracePeriodRequested,
		shutdownGracePeriodCriticalPods: shutdownGracePeriodCriticalPods,
		clock:                           clock.RealClock{},
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

	systemBus, err := getSystemDbus()
	if err != nil {
		return err
	}
	m.dbusCon = systemBus

	klog.Infof("porterdavid: shutdown info: shutdownGracePeriodRequested: %v ; shutdownGracePeriodCriticalPods: %v", m.shutdownGracePeriodRequested, m.shutdownGracePeriodCriticalPods)

	if m.shutdownGracePeriodRequested == 0 {
		klog.V(0).Info("shutdown duration was zero, skipping shutdown inhibit setup")
		return nil
	}

	currentInhibitDelay, err := m.dbusCon.CurrentInhibitDelay()
	if err != nil {
		return err
	}

	if m.shutdownGracePeriodRequested > currentInhibitDelay {
		// write config file
		err := m.dbusCon.OverrideInhibitDelay(m.shutdownGracePeriodRequested)
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
		updatedInhibitDelay, err := m.dbusCon.CurrentInhibitDelay()
		if err != nil {
			return err
		}

		if updatedInhibitDelay != m.shutdownGracePeriodRequested {
			return fmt.Errorf("node shutdown manager was unable to update logind InhibitDelayMaxSec to %v (ShutdownGracePeriod), current value of InhibitDelayMaxSec (%v) is less than requested ShutdownGracePeriod", m.shutdownGracePeriodRequested, updatedInhibitDelay)
		}
	}

	klog.V(0).Infof("porterdavid: inhibitDelay: %v shutDownConfig: %v", currentInhibitDelay, m.shutdownGracePeriodRequested)

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
		return fmt.Errorf("node is shutting down")
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

	nonCriticalPodGracePeriod := m.shutdownGracePeriodRequested - m.shutdownGracePeriodCriticalPods

	var wg sync.WaitGroup
	for _, pod := range activePods {
		go func(pod *v1.Pod) {
			defer wg.Done()

			var gracePeriodOverride int64
			if kubelettypes.IsCriticalPod(pod) {
				gracePeriodOverride = int64(m.shutdownGracePeriodCriticalPods.Seconds())
				m.clock.Sleep(nonCriticalPodGracePeriod)
			} else {
				gracePeriodOverride = int64(nonCriticalPodGracePeriod.Seconds())
			}

			klog.V(0).Infof("porterdavid: killing pod %v gracePeriod: %v", pod.Name, gracePeriodOverride)

			status := v1.PodStatus{
				Phase:   v1.PodFailed,
				Message: nodeShutdownMessage,
				Reason:  nodeShutdownReason,
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

	klog.V(0).Infof("porterdavid: release inhibit lock start")
	m.dbusCon.ReleaseInhibitLock(m.inhibitLock)
	klog.V(0).Infof("porterdavid: release inhibit lock done")

	return nil
}
