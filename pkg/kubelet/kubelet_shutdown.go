package kubelet

import (
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/eviction"
	"k8s.io/kubernetes/pkg/kubelet/systemd"
	kubelettypes "k8s.io/kubernetes/pkg/kubelet/types"
	"k8s.io/utils/integer"
)

type dBusCon struct {
	*dbus.Conn
	inhibitLock *os.File
}

func (kl *Kubelet) startShutdownInhibitor(getPodsFunc eviction.ActivePodsFunc, killPodFunc eviction.KillPodFunc) error {
	shutdownGracePeriodDuration := kl.kubeletConfiguration.ShutdownGracePeriod.Duration
	klog.Infof("porterdavid shutdownGracePeriodDuration: %v", shutdownGracePeriodDuration)

	if shutdownGracePeriodDuration == 0 {
		klog.V(0).Info("shutdown duration was zero, skipping shutdown inhibit setup")
		return nil
	}

	systemBus, err := dbus.SystemBus()
	if err != nil {
		return err
	}

	dbusCon := &systemd.DBusCon{DBusConnector: systemBus}

	maxInhibitDelay, err := dbusCon.CurrentInhibitDelay()
	if err != nil {
		return err
	}

	if shutdownGracePeriodDuration > maxInhibitDelay {
		// write config file
		err := systemd.OverrideSystemdInhibitDelay(shutdownGracePeriodDuration)
		if err != nil {
			return err
		}
		klog.V(0).Infof("porterdavid: writing inhibit file")

		err = dbusCon.ReloadLogindConf()
		if err != nil {
			return err
		}
		klog.V(0).Infof("porterdavid: reloaded logind")

		// read the maxInhibitDelay again, hopefuly updated now
		maxInhibitDelay, err = dbusCon.CurrentInhibitDelay()
		if err != nil {
			return err
		}
	}

	klog.V(0).Infof("porterdavid: inhibitDelay: %v shutDownConfig: %v", maxInhibitDelay, shutdownGracePeriodDuration)

	minShutDownDelay := time.Duration(integer.Int64Min(maxInhibitDelay.Nanoseconds(), shutdownGracePeriodDuration.Nanoseconds()))
	klog.V(0).Infof("porterdavid: minShutDownDelay %v", minShutDownDelay)

	// TODO(bobbypage): handle the lock fd returned
	_, err = dbusCon.InhibitShutdown()
	if err != nil {
		return err
	}

	events, err := dbusCon.MonitorShutdown()
	if err != nil {
		return err
	}

	go func() {
		for {
			select {
			case event := <-events:
				if event {
					processShutdownEvent(getPodsFunc, killPodFunc, minShutDownDelay)
				}
				klog.Infof("porterdavid: got active: %t", event)
			}
		}
	}()

	return nil
}

func processShutdownEvent(getPodsFunc eviction.ActivePodsFunc, killPodFunc eviction.KillPodFunc, shutdownDuration time.Duration) error {
	klog.Infof("porterdavid: processShutdownEvent")

	activePods := getPodsFunc()

	criticalPodGracePeriod := time.Duration(2) * time.Second
	nonCriticalPodGracePeriod := shutdownDuration - criticalPodGracePeriod

	klog.Infof("porterdavid: processShutdownEvent systemGracePeriod: %v ; nonSystemGracePeriod: %v", criticalPodGracePeriod, nonCriticalPodGracePeriod)

	var wg sync.WaitGroup
	for _, pod := range activePods {
		go func(pod *v1.Pod) {
			defer wg.Done()

			var gracePeriodOverride int64
			if kubelettypes.IsCriticalPod(pod) {
				gracePeriodOverride = int64(criticalPodGracePeriod.Seconds())
				time.Sleep(nonCriticalPodGracePeriod)
			} else {
				gracePeriodOverride = int64(nonCriticalPodGracePeriod.Seconds())
			}

			klog.Infof("porterdavid: killing pod %v gracePeriod: %v", pod.Name, gracePeriodOverride)

			status := v1.PodStatus{
				Phase:   v1.PodFailed,
				Message: "Node was shutdown, shutting down pod",
				Reason:  "Shutdown",
			}

			err := killPodFunc(pod, status, &gracePeriodOverride)
			if err != nil {
				klog.Infof("error killing pod %q: %v", pod.Name, err)
			}
			klog.Infof("porterdavid: done killing pod %v", pod.Name)
		}(pod)
		wg.Add(1)
	}
	wg.Wait()

	return nil
}
