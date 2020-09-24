package kubelet

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/eviction"
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

	systemBusCon, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	conn := dBusCon{Conn: systemBusCon}

	maxInhibitDelay, err := conn.getMaxInhibitDelay()
	if err != nil {
		return err
	}

	if shutdownGracePeriodDuration > maxInhibitDelay {
		// write config file
		err := writeMaxInhibitDelayFile(shutdownGracePeriodDuration)
		if err != nil {
			return err
		}
		klog.V(0).Infof("porterdavid: writing inhibit file")

		err = conn.reloadLogind()
		if err != nil {
			return err
		}
		klog.V(0).Infof("porterdavid: reloaded logind")

		// read the maxInhibitDelay again, hopefuly updated now
		maxInhibitDelay, err = conn.getMaxInhibitDelay()
		if err != nil {
			return err
		}
	}

	klog.V(0).Infof("porterdavid: inhibitDelay: %v shutDownConfig: %v", maxInhibitDelay, shutdownGracePeriodDuration)

	minShutDownDelay := time.Duration(integer.Int64Min(maxInhibitDelay.Nanoseconds(), shutdownGracePeriodDuration.Nanoseconds()))
	klog.V(0).Infof("porterdavid: minShutDownDelay %v", minShutDownDelay)

	conn.takeInhibitLock()

	conn.monitorShutdown(getPodsFunc, killPodFunc, minShutDownDelay)

	return nil
}

func (info *dBusCon) monitorShutdown(getPodsFunc eviction.ActivePodsFunc, killPodFunc eviction.KillPodFunc, shutdownDuration time.Duration) error {
	err := info.Conn.AddMatchSignal(
		dbus.WithMatchInterface("org.freedesktop.login1.Manager"),
		dbus.WithMatchMember("PrepareForShutdown"),
		dbus.WithMatchObjectPath("/org/freedesktop/login1"),
	)
	if err != nil {
		return err
	}

	c := make(chan *dbus.Signal, 1)
	info.Conn.Signal(c)

	go func() {
		for {
			select {
			case event := <-c:
				active, ok := event.Body[0].(bool)
				if !ok {
					klog.V(0).Infof("unable to parse bool")
					return
				}
				klog.Infof("porterdavid: got event %+v, active: %v", event, active)
				if active {
					info.processShutdownEvent(getPodsFunc, killPodFunc, shutdownDuration)
				}
			}
		}
	}()
	return nil
}

func (info *dBusCon) processShutdownEvent(getPodsFunc eviction.ActivePodsFunc, killPodFunc eviction.KillPodFunc, shutdownDuration time.Duration) error {
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

func (info *dBusCon) takeInhibitLock() error {
	klog.Infof("porterdavid: started takeInhibitLock")

	obj := info.Conn.Object("org.freedesktop.login1", "/org/freedesktop/login1")
	what := "shutdown:sleep"
	who := "kubelet"
	why := "because"
	mode := "delay"

	call := obj.Call("org.freedesktop.login1.Manager.Inhibit", 0, what, who, why, mode)
	if call.Err != nil {
		return call.Err
	}

	var fd uint32
	err := call.Store(&fd)
	if err != nil {
		return err
	}
	//info.inhibitLock = os.NewFile(uintptr(fd), "inhibit fd")

	klog.Infof("porterdavid: done with inhibit lock got fd: %v", fd)

	return nil
}

func (info *dBusCon) releaseInhibitLock() error {
	if info.inhibitLock == nil {
		return errors.New("unable to releaseInhibitLock, lock is nil")
	}

	err := info.inhibitLock.Close()
	if err != nil {
		return fmt.Errorf("unable to releaseInhibitLock: %v", err)
	}

	return nil
}

func (info *dBusCon) getMaxInhibitDelay() (time.Duration, error) {
	dbusService := "org.freedesktop.login1"
	dbusObject := "/org/freedesktop/login1"
	dbusInterface := "org.freedesktop.login1.Manager"

	obj := info.Conn.Object(dbusService, dbus.ObjectPath(dbusObject))
	res, err := obj.GetProperty(dbusInterface + ".InhibitDelayMaxUSec")
	if err != nil {
		klog.Errorf("error getting dbus property: %v", err)
		return 0, err
	}

	delay := res.Value().(uint64)
	// Convert time from microseconds
	duration := time.Duration(delay) * time.Microsecond

	return duration, nil
}

func writeMaxInhibitDelayFile(inhibitDelayMax time.Duration) error {
	err := os.MkdirAll("/etc/systemd/logind.conf.d/", 0755)
	if err != nil {
		klog.Error(err)
	}

	f, err := os.Create("/etc/systemd/logind.conf.d/kubelet.conf")
	if err != nil {
		return err
	}
	defer f.Close()

	configFile := fmt.Sprintf(`# Kubelet logind override conf
[Login]
InhibitDelayMaxSec=%.0f
`, inhibitDelayMax.Seconds())

	_, err = f.WriteString(configFile)
	if err != nil {
		return err
	}

	return nil
}

func (info *dBusCon) reloadLogind() error {
	systemdService := "org.freedesktop.systemd1"
	systemdObject := "/org/freedesktop/systemd1"
	systemdInterface := "org.freedesktop.systemd1.Manager"

	obj := info.Conn.Object(systemdService, dbus.ObjectPath(systemdObject))
	unit := "systemd-logind.service"
	who := "all"
	var signal int32 = 1 // SIGHUP

	// TODO(bobbypage): Consider using CallWithContext here instead
	call := obj.Call(systemdInterface+".KillUnit", 0, unit, who, signal)
	if call.Err != nil {
		klog.Error(call.Err)
		return call.Err
	}
	return nil
}
