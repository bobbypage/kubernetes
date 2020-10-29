package systemd

import (
	"fmt"
	"io/ioutil"
	"os"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
	"k8s.io/klog/v2"
)

const (
	LogindService   = "org.freedesktop.login1"
	LogindObject    = dbus.ObjectPath("/org/freedesktop/login1")
	LogindInterface = "org.freedesktop.login1.Manager"
)

type DBusConnector interface {
	Object(dest string, path dbus.ObjectPath) dbus.BusObject
	AddMatchSignal(options ...dbus.MatchOption) error
	Signal(ch chan<- *dbus.Signal)
}

type DBusCon struct {
	DBusConnector
}

type InhibitLock uint32

// CurrentInhibitDelay returns the current delay inhibitor timeout value as configured in logind.conf(5).
// see https://www.freedesktop.org/software/systemd/man/logind.conf.html for more details.
func (bus *DBusCon) CurrentInhibitDelay() (time.Duration, error) {
	obj := bus.Object(LogindService, LogindObject)
	res, err := obj.GetProperty(LogindInterface + ".InhibitDelayMaxUSec")
	if err != nil {
		return 0, fmt.Errorf("failed reading InhibitDelayMaxUSec property from logind: %v", err)
	}

	delay, ok := res.Value().(uint64)
	if !ok {
		return 0, fmt.Errorf("InhibitDelayMaxUSec from logind is not a uint64 as expected")
	}

	// InhibitDelayMaxUSec is in microseconds
	duration := time.Duration(delay) * time.Microsecond
	return duration, nil
}

// InhibitShutdown creates an systemd inhibitor by calling logind's Inhibt() and returns the inhibitor lock
// see https://www.freedesktop.org/wiki/Software/systemd/inhibit/ for more details.
func (bus *DBusCon) InhibitShutdown() (InhibitLock, error) {
	obj := bus.Object(LogindService, LogindObject)
	// TODO(bobbypage): we probably don't need sleep here...
	what := "shutdown:sleep"
	who := "kubelet"
	why := "Kubelet needs time to handle node shutdown"
	mode := "delay"

	call := obj.Call("org.freedesktop.login1.Manager.Inhibit", 0, what, who, why, mode)
	if call.Err != nil {
		return InhibitLock(0), fmt.Errorf("failed creating systemd inhibitor: %v", call.Err)
	}

	var fd uint32
	err := call.Store(&fd)
	if err != nil {
		return InhibitLock(0), fmt.Errorf("failed storing inhibit lock file descriptor: %v", err)
	}

	return InhibitLock(fd), nil
}

func (bus *DBusCon) ReleaseInhibitLock(lock InhibitLock) error {
	err := syscall.Close(int(lock))

	if err != nil {
		return fmt.Errorf("unable to close systemd inhibitor lock: %v", err)
	}

	return nil
}

// ReloadLogindConf uses dbus to send a SIGHUP to the systemd-logind service causing logind to reload it's configuration.
func (bus *DBusCon) ReloadLogindConf() error {
	systemdService := "org.freedesktop.systemd1"
	systemdObject := "/org/freedesktop/systemd1"
	systemdInterface := "org.freedesktop.systemd1.Manager"

	obj := bus.Object(systemdService, dbus.ObjectPath(systemdObject))
	unit := "systemd-logind.service"
	who := "all"
	var signal int32 = 1 // SIGHUP

	// TODO(bobbypage): Consider using CallWithContext here instead
	call := obj.Call(systemdInterface+".KillUnit", 0, unit, who, signal)
	if call.Err != nil {
		return fmt.Errorf("unable to reload logind conf: %v", call.Err)
	}

	return nil
}

// MonitorShutdown detects the a node shutdown by watching for "PrepareForShutdown" logind events.
// see https://www.freedesktop.org/wiki/Software/systemd/inhibit/ for more details.
func (bus *DBusCon) MonitorShutdown() (<-chan bool, error) {
	err := bus.AddMatchSignal(dbus.WithMatchInterface(LogindInterface), dbus.WithMatchMember("PrepareForShutdown"), dbus.WithMatchObjectPath("/org/freedesktop/login1"))

	if err != nil {
		return nil, err
	}

	busChan := make(chan *dbus.Signal, 1)
	bus.Signal(busChan)

	shutdownChan := make(chan bool, 1)

	go func() {
		for {
			select {
			case event := <-busChan:
				if len(event.Body) == 0 {
					klog.Errorf("Failed obtaining shutdown event, PrepareForShutdown event was empty")
				}
				shutdownActive, ok := event.Body[0].(bool)
				if !ok {
					klog.Errorf("Failed obtaining shutdown event, PrepareForShutdown event was not bool type as expected")
					return
				}
				klog.V(1).Infof("Recieved node shutdown event; shutdown active: %t", shutdownActive)
				shutdownChan <- shutdownActive
			}
		}
	}()

	return shutdownChan, nil
}

// OverrideSystemdInhibitDelay writes a config file to logind overriding InhibitDelayMaxSec to the value desired.
func OverrideSystemdInhibitDelay(inhibitDelayMax time.Duration) error {
	err := os.MkdirAll("/etc/systemd/logind.conf.d/", 0755)
	if err != nil {
		klog.Error(err)
	}

	inhibitOverride := fmt.Sprintf(`# Kubelet logind override
    [Login]
    InhibitDelayMaxSec=%.0f
    `, inhibitDelayMax.Seconds())

	if err := ioutil.WriteFile("/etc/systemd/logind.conf.d/kubelet.conf", []byte(inhibitOverride), 0755); err != nil {
		return err
	}

	return nil
}
