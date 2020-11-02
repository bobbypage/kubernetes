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

// Package nodeshutdown can watch for node level shutdown events and trigger graceful termination of pods running on the node prior to a system shutdown.
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

// Manager has functions that can be used to interact with the Node Shutdown Manager.
type Manager struct {
	shutdownGracePeriodRequested    time.Duration
	shutdownGracePeriodCriticalPods time.Duration

	getPods eviction.ActivePodsFunc
	killPod eviction.KillPodFunc

	dbusCon             dbusInhibiter
	nodeShuttingDownNow bool
	inhibitLock         systemd.InhibitLock

	clock clock.Clock
}

func systemDbus() (dbusInhibiter, error) {
	bus, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	return &systemd.DBusCon{SystemBus: bus}, nil
}

// NewManager returns a new node shutdown manager.
func NewManager(getPodsFunc eviction.ActivePodsFunc, killPodFunc eviction.KillPodFunc, shutdownGracePeriodRequested, shutdownGracePeriodCriticalPods time.Duration) *Manager {
	return &Manager{
		getPods:                         getPodsFunc,
		killPod:                         killPodFunc,
		shutdownGracePeriodRequested:    shutdownGracePeriodRequested,
		shutdownGracePeriodCriticalPods: shutdownGracePeriodCriticalPods,
		clock:                           clock.RealClock{},
	}
}

// Start starts the node shutdown manager and will start watching the node for shutdown events.
func (m *Manager) Start() error {
	if !utilfeature.DefaultFeatureGate.Enabled(features.GracefulNodeShutdown) {
		return nil
	}
	if m.shutdownGracePeriodRequested == 0 {
		return nil
	}

	systemBus, err := getSystemDbus()
	if err != nil {
		return err
	}
	m.dbusCon = systemBus

	currentInhibitDelay, err := m.dbusCon.CurrentInhibitDelay()
	if err != nil {
		return err
	}

	// If the logind's InhibitDelayMaxUSec as configured in (logind.conf) is less than shutdownGracePeriodRequested, attempt to update the value to shutdownGracePeriodRequested.
	if m.shutdownGracePeriodRequested > currentInhibitDelay {
		err := m.dbusCon.OverrideInhibitDelay(m.shutdownGracePeriodRequested)
		if err != nil {
			return err
		}

		err = m.dbusCon.ReloadLogindConf()
		if err != nil {
			return err
		}

		// Read the current inhibitDelay again, if the override was successful, currentInhibitDelay will be equal to shutdownGracePeriodRequested.
		updatedInhibitDelay, err := m.dbusCon.CurrentInhibitDelay()
		if err != nil {
			return err
		}

		if updatedInhibitDelay != m.shutdownGracePeriodRequested {
			return fmt.Errorf("node shutdown manager was unable to update logind InhibitDelayMaxSec to %v (ShutdownGracePeriod), current value of InhibitDelayMaxSec (%v) is less than requested ShutdownGracePeriod", m.shutdownGracePeriodRequested, updatedInhibitDelay)
		}
	}

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
				klog.V(1).Infof("Shutdown manager detected new shutdown event, isNodeShuttingDownNow: %t", isShuttingDown)
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

func (m *Manager) aquireInhibitLock() error {
	lock, err := m.dbusCon.InhibitShutdown()
	if err != nil {
		return err
	}
	m.inhibitLock = lock
	return nil
}

func (m *Manager) ShutdownStatus() error {
	if !utilfeature.DefaultFeatureGate.Enabled(features.GracefulNodeShutdown) {
		return nil
	}

	if m.nodeShuttingDownNow {
		return fmt.Errorf("node is shutting down")
	}
	return nil
}

func (m *Manager) processShutdownEvent() error {
	klog.V(1).Infof("Shutdown manager processing shutdown event")
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

			klog.V(1).Infof("Shutdown manager killing pod %q with gracePeriod: %v", pod.Name, gracePeriodOverride)

			status := v1.PodStatus{
				Phase:   v1.PodFailed,
				Message: nodeShutdownMessage,
				Reason:  nodeShutdownReason,
			}

			err := m.killPod(pod, status, &gracePeriodOverride)
			if err != nil {
				klog.V(1).Infof("Shutdown manager failed killing pod %q: %v", pod.Name, err)
			} else {
				klog.V(1).Infof("Shutdown manager finished killing pod %q: %v", pod.Name, err)
			}
		}(pod)
		wg.Add(1)
	}
	wg.Wait()

	m.dbusCon.ReleaseInhibitLock(m.inhibitLock)
	klog.V(1).Infof("Shutdown manager completed processing shutdown event, node will shutdown shortly")

	return nil
}
