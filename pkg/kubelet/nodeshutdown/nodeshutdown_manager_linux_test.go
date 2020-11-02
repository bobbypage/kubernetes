package nodeshutdown

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/clock"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/kubernetes/pkg/apis/scheduling"
	pkgfeatures "k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/kubelet/nodeshutdown/systemd"
)

type fakeDbus struct {
	currentInhibitDelay        time.Duration
	overrideSystemInhibitDelay time.Duration
	shutdownChan               chan bool

	didInhibitShutdown      bool
	didOverrideInhibitDelay bool
}

func (f *fakeDbus) CurrentInhibitDelay() (time.Duration, error) {
	if f.didOverrideInhibitDelay {
		return f.overrideSystemInhibitDelay, nil
	}
	return f.currentInhibitDelay, nil
}

func (f *fakeDbus) InhibitShutdown() (systemd.InhibitLock, error) {
	f.didInhibitShutdown = true
	return systemd.InhibitLock(0), nil
}

func (f *fakeDbus) ReleaseInhibitLock(lock systemd.InhibitLock) error {
	return nil
}

func (f *fakeDbus) ReloadLogindConf() error {
	return nil
}

func (f *fakeDbus) MonitorShutdown() (<-chan bool, error) {
	return f.shutdownChan, nil
}

func (f *fakeDbus) OverrideInhibitDelay(inhibitDelayMax time.Duration) error {
	f.didOverrideInhibitDelay = true
	return nil
}

func makePod(name string, criticalPod bool) *v1.Pod {
	var priority int32
	if criticalPod {
		priority = scheduling.SystemCriticalPriority
	}

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			UID:  types.UID(name),
		},
		Spec: v1.PodSpec{
			Priority: &priority,
		},
	}
}

func TestManager(t *testing.T) {
	normalPod := makePod("normal-pod", false /* criticalPod */)
	criticalPod := makePod("critical-pod", true /* criticalPod */)

	var tests = []struct {
		desc                             string
		activePods                       []*v1.Pod
		shutdownGracePeriodRequested     time.Duration
		shutdownGracePeriodCriticalPods  time.Duration
		systemInhibitDelay               time.Duration
		overrideSystemInhibitDelay       time.Duration
		expectedDidOverrideInhibitDelay  bool
		expectedPodToGracePeriodOverride map[string]int64
		expectedError                    error
	}{
		{
			desc:                             "no override (total=30s, critical=10s)",
			activePods:                       []*v1.Pod{normalPod, criticalPod},
			shutdownGracePeriodRequested:     time.Duration(30 * time.Second),
			shutdownGracePeriodCriticalPods:  time.Duration(10 * time.Second),
			systemInhibitDelay:               time.Duration(40 * time.Second),
			overrideSystemInhibitDelay:       time.Duration(40 * time.Second),
			expectedDidOverrideInhibitDelay:  false,
			expectedPodToGracePeriodOverride: map[string]int64{"normal-pod": 20, "critical-pod": 10},
		},
		{
			desc:                             "no override (total=30, critical=0)",
			activePods:                       []*v1.Pod{normalPod, criticalPod},
			shutdownGracePeriodRequested:     time.Duration(30 * time.Second),
			shutdownGracePeriodCriticalPods:  time.Duration(0 * time.Second),
			systemInhibitDelay:               time.Duration(40 * time.Second),
			overrideSystemInhibitDelay:       time.Duration(40 * time.Second),
			expectedDidOverrideInhibitDelay:  false,
			expectedPodToGracePeriodOverride: map[string]int64{"normal-pod": 30, "critical-pod": 0},
		},
		{
			desc:                             "override succesful (total=30, critical=10)",
			activePods:                       []*v1.Pod{normalPod, criticalPod},
			shutdownGracePeriodRequested:     time.Duration(30 * time.Second),
			shutdownGracePeriodCriticalPods:  time.Duration(10 * time.Second),
			systemInhibitDelay:               time.Duration(5 * time.Second),
			overrideSystemInhibitDelay:       time.Duration(30 * time.Second),
			expectedDidOverrideInhibitDelay:  true,
			expectedPodToGracePeriodOverride: map[string]int64{"normal-pod": 20, "critical-pod": 10},
		},
		{
			desc:                             "override unsuccesful",
			activePods:                       []*v1.Pod{normalPod, criticalPod},
			shutdownGracePeriodRequested:     time.Duration(30 * time.Second),
			shutdownGracePeriodCriticalPods:  time.Duration(10 * time.Second),
			systemInhibitDelay:               time.Duration(5 * time.Second),
			overrideSystemInhibitDelay:       time.Duration(5 * time.Second),
			expectedDidOverrideInhibitDelay:  true,
			expectedPodToGracePeriodOverride: map[string]int64{"normal-pod": 5, "critical-pod": 0},
			expectedError:                    fmt.Errorf("unable to update logind InhibitDelayMaxSec to 30s (ShutdownGracePeriod), current value of InhibitDelayMaxSec (5s) is less than requested ShutdownGracePeriod"),
		},
		{
			desc:                            "override unsuccesful, zero time",
			activePods:                      []*v1.Pod{normalPod, criticalPod},
			shutdownGracePeriodRequested:    time.Duration(5 * time.Second),
			shutdownGracePeriodCriticalPods: time.Duration(5 * time.Second),
			systemInhibitDelay:              time.Duration(0 * time.Second),
			overrideSystemInhibitDelay:      time.Duration(0 * time.Second),
			expectedError:                   fmt.Errorf("unable to update logind InhibitDelayMaxSec to 5s (ShutdownGracePeriod), current value of InhibitDelayMaxSec (0s) is less than requested ShutdownGracePeriod"),
		},
		{
			desc:                             "no override, all time to critical pods",
			activePods:                       []*v1.Pod{normalPod, criticalPod},
			shutdownGracePeriodRequested:     time.Duration(5 * time.Second),
			shutdownGracePeriodCriticalPods:  time.Duration(5 * time.Second),
			systemInhibitDelay:               time.Duration(5 * time.Second),
			overrideSystemInhibitDelay:       time.Duration(5 * time.Second),
			expectedDidOverrideInhibitDelay:  false,
			expectedPodToGracePeriodOverride: map[string]int64{"normal-pod": 0, "critical-pod": 5},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			activePodsFunc := func() []*v1.Pod {
				return tc.activePods
			}

			killedPodsToGracePeriods := map[string]int64{}
			podKillChan := make(chan struct{})

			killPodsFunc := func(pod *v1.Pod, status v1.PodStatus, gracePeriodOverride *int64) error {
				var gracePeriod int64
				if gracePeriodOverride != nil {
					gracePeriod = *gracePeriodOverride
				}
				killedPodsToGracePeriods[pod.Name] = gracePeriod
				podKillChan <- struct{}{}
				return nil
			}

			fakeShutdownChan := make(chan bool)
			fakeDbus := &fakeDbus{currentInhibitDelay: tc.systemInhibitDelay, shutdownChan: fakeShutdownChan, overrideSystemInhibitDelay: tc.overrideSystemInhibitDelay}
			getSystemDbus = func() (dbusInhibiter, error) {
				return fakeDbus, nil
			}
			defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, pkgfeatures.GracefulNodeShutdown, true)()

			manager := NewManager(activePodsFunc, killPodsFunc, tc.shutdownGracePeriodRequested, tc.shutdownGracePeriodCriticalPods)
			manager.clock = clock.NewFakeClock(time.Now())

			err := manager.Start()
			if tc.expectedError != nil {
				if !strings.Contains(err.Error(), tc.expectedError.Error()) {
					t.Errorf("unexpected error message. Got: %s want %s", err.Error(), tc.expectedError.Error())
				}
			} else {
				assert.NoError(t, err, "expected manager.Start() to not return error")
				assert.True(t, fakeDbus.didInhibitShutdown, "expected that manager inhibited shutdown")
				assert.NoError(t, manager.ShutdownStatus(), "expected that manager does not return error since shutdown is not active")

				// Send fake shutdown event
				fakeShutdownChan <- true
				assert.Error(t, manager.ShutdownStatus(), "expected that manager returns error since shutdown is active")

				// Wait for all the pods to be killed
				for i := 0; i < len(tc.activePods); i++ {
					select {
					case <-podKillChan:
						continue
					case <-time.After(1 * time.Second):
						t.Fatal()
					}
				}

				assert.Equal(t, tc.expectedPodToGracePeriodOverride, killedPodsToGracePeriods)
				assert.Equal(t, tc.expectedDidOverrideInhibitDelay, fakeDbus.didOverrideInhibitDelay, "override system inhibit delay differs")
			}
		})
	}
}
