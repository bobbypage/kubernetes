// +build windows

/*
Copyright 2015 The Kubernetes Authors.

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

package cadvisor

import (
	//"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	dockerapi "github.com/docker/engine-api/client"
	dockertypes "github.com/docker/engine-api/types"
	"github.com/golang/glog"
	cadvisorapi "github.com/google/cadvisor/info/v1"
	cadvisorapiv2 "github.com/google/cadvisor/info/v2"
	"github.com/lxn/win"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

//CPUQuery for perf counter
const CPUQuery = "\\Processor(_Total)\\% Processor Time"

type winhelper struct {
	DockerStats          map[string]Stats
	usageCoreNanoSeconds uint64
	mu                   sync.Mutex
}

var instance *winhelper
var once sync.Once

func (ws *winhelper) Start() {
	glog.Info("winhelper start() ")
}

func GetWinHelper() *winhelper {
	glog.Info("getwinhelper() ")
	once.Do(func() {
		glog.Info("make winhelper singleton")
		instance = &winhelper{DockerStats: make(map[string]Stats)}
		instance.startNodeCPUMonitoring()
		instance.startDockerStatsMonitoring()
	})
	glog.Info("returning instance %v", instance)
	return instance
}

func (ws *winhelper) startNodeCPUMonitoring() {
	glog.Info("startNodeCPUMonitoring()")

	go func() {
		c, err := readPerformanceCounter(CPUQuery, 1)

		if err != nil {
			glog.Infof("Unable to read perf counter")
		}

		for m := range c {
			ws.mu.Lock()
			glog.Infof("CPU perf data %v", m)

			cpuCores := runtime.NumCPU()

			ws.usageCoreNanoSeconds += uint64((m.ValueFloat / 100.0) * float64(cpuCores) * 1000000000)
			glog.Infof("cpuCores %v ; UsageCoreNanoSeconds %v", cpuCores, ws.usageCoreNanoSeconds)

			ws.mu.Unlock()
		}
	}()
	glog.Info("Exit startNodeCPUMonitoring()")
}

func (ws *winhelper) GetUsageCoreNanoSeconds() uint64 {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return ws.usageCoreNanoSeconds
}

func (ws *winhelper) createContainerInfo(container *dockertypes.Container) cadvisorapiv2.ContainerInfo {

	spec := cadvisorapiv2.ContainerSpec{
		CreationTime:     time.Unix(container.Created, 0),
		Aliases:          []string{},
		Namespace:        "docker",
		Labels:           container.Labels,
		Envs:             map[string]string{},
		HasCpu:           true,
		Cpu:              cadvisorapiv2.CpuSpec{},
		HasMemory:        true,
		Memory:           cadvisorapiv2.MemorySpec{},
		HasCustomMetrics: false,
		CustomMetrics:    []cadvisorapi.MetricSpec{},
		HasNetwork:       true,
		HasFilesystem:    true,
		HasDiskIo:        true,
		Image:            container.Image,
	}

	stats := make([]*cadvisorapiv2.ContainerStats, 1)
	stats.append(
		&cadvisorapiv2.ContainerStats{Cpu: &cadvisorapi.CpuStats{Usage: cadvisorapi.CpuUsage{Total: GetWinHelper().GetUsageCoreNanoSeconds()}}})

	return cadvisorapiv2.ContainerInfo{Spec: spec}
}

func (ws *winhelper) startDockerStatsMonitoring() {
	//https://github.com/robertojrojas/cross-platform-communication-using-gRPC/blob/cd12ab4ec2614475f6803e75cc6d604f0adf3035/examples/nodejs-to-go/docker/service/server.go

	go func() {
		for {
			glog.Infof("starting docker stats monitoring")
			cli, err := dockerapi.NewEnvClient()
			if err != nil {
				panic(err)

			}
			containers, err := cli.ContainerList(context.Background(), dockertypes.ContainerListOptions{})

			glog.Info("looping through containers")
			for _, container := range containers {

				glog.Infof("container: %+v", container)
				response, err := cli.ContainerStats(context.Background(), container.ID, false)

				if err != nil {
					panic("can't call docker stats")
				}

				dec := json.NewDecoder(response)

				var stats Stats
				err = dec.Decode(&stats)

				if err != nil {
					panic("cant parse json")
				}

				ws.DockerStats[container.ID] = stats
				glog.Infof("Got stats for container %v", stats)
				time.Sleep(5 * time.Second)
			}
		}
	}()
}

// Metric collected from a counter
type Metric struct {
	Name       string
	Value      string
	ValueFloat float64
	Timestamp  int64
}

// NewMetric construct a Metric struct
func NewMetric(name, value string, valueFloat float64, timestamp int64) Metric {
	return Metric{
		Name:       name,
		Value:      value,
		ValueFloat: valueFloat,
		Timestamp:  timestamp,
	}
}

func (metric Metric) String() string {
	return fmt.Sprintf(
		"%s | %s | %s",
		metric.Name,
		metric.Value,
		time.Unix(metric.Timestamp, 0).Format("2006-01-02 15:04:05"),
	)
}

func readPerformanceCounter(counter string, sleepInterval int) (chan Metric, error) {

	var queryHandle win.PDH_HQUERY
	var counterHandle win.PDH_HCOUNTER

	ret := win.PdhOpenQuery(0, 0, &queryHandle)
	if ret != win.ERROR_SUCCESS {
		return nil, errors.New("Unable to open query through DLL call")
	}

	// test path
	ret = win.PdhValidatePath(counter)
	if ret == win.PDH_CSTATUS_BAD_COUNTERNAME {
		return nil, errors.New("Unable to fetch counter (this is unexpected)")
	}

	ret = win.PdhAddEnglishCounter(queryHandle, counter, 0, &counterHandle)
	if ret != win.ERROR_SUCCESS {
		return nil, errors.New(fmt.Sprintf("Unable to add process counter. Error code is %x\n", ret))
	}

	ret = win.PdhCollectQueryData(queryHandle)
	if ret != win.ERROR_SUCCESS {
		return nil, errors.New(fmt.Sprintf("Got an error: 0x%x\n", ret))
	}

	out := make(chan Metric)

	go func() {
		for {
			ret = win.PdhCollectQueryData(queryHandle)
			if ret == win.ERROR_SUCCESS {

				//var metric []Metric
				var metric Metric

				var bufSize uint32
				var bufCount uint32
				var size = uint32(unsafe.Sizeof(win.PDH_FMT_COUNTERVALUE_ITEM_DOUBLE{}))
				var emptyBuf [1]win.PDH_FMT_COUNTERVALUE_ITEM_DOUBLE // need at least 1 addressable null ptr.

				ret = win.PdhGetFormattedCounterArrayDouble(counterHandle, &bufSize, &bufCount, &emptyBuf[0])
				if ret == win.PDH_MORE_DATA {
					filledBuf := make([]win.PDH_FMT_COUNTERVALUE_ITEM_DOUBLE, bufCount*size)
					ret = win.PdhGetFormattedCounterArrayDouble(counterHandle, &bufSize, &bufCount, &filledBuf[0])
					if ret == win.ERROR_SUCCESS {
						for i := 0; i < int(bufCount); i++ {
							c := filledBuf[i]
							s := win.UTF16PtrToString(c.SzName)

							metricName := normalizePerfCounterMetricName(counter)
							if len(s) > 0 {
								metricName = fmt.Sprintf("%s.%s", normalizePerfCounterMetricName(counter), normalizePerfCounterMetricName(s))
							}

							//metric = append(metric, Metric{
							//metricName,
							//fmt.Sprintf("%v", c.FmtValue.DoubleValue),
							//c.FmtValue.DoubleValue,
							//time.Now().Unix()})

							metric = Metric{
								metricName,
								fmt.Sprintf("%v", c.FmtValue.DoubleValue),
								c.FmtValue.DoubleValue,
								time.Now().Unix()}

						}
					}
				}
				out <- metric
			}

			time.Sleep(time.Duration(sleepInterval) * time.Second)
		}
	}()

	return out, nil
}

func normalizePerfCounterMetricName(rawName string) (normalizedName string) {

	normalizedName = rawName

	// thanks to Microsoft Windows,
	// we have performance counter metric like `\\Processor(_Total)\\% Processor Time`
	// which we need to convert to `processor_total.processor_time` see perfcounter_test.go for more beautiful examples
	r := strings.NewReplacer(
		".", "",
		"\\", ".",
		" ", "_",
	)
	normalizedName = r.Replace(normalizedName)

	normalizedName = normalizeMetricName(normalizedName)
	return
}

func normalizeMetricName(rawName string) (normalizedName string) {

	normalizedName = strings.ToLower(rawName)

	// remove trailing and leading non alphanumeric characters
	re1 := regexp.MustCompile(`(^[^a-z0-9]+)|([^a-z0-9]+$)`)
	normalizedName = re1.ReplaceAllString(normalizedName, "")

	// replace whitespaces with underscore
	re2 := regexp.MustCompile(`\s`)
	normalizedName = re2.ReplaceAllString(normalizedName, "_")

	// remove non alphanumeric characters except underscore and dot
	re3 := regexp.MustCompile(`[^a-z0-9._]`)
	normalizedName = re3.ReplaceAllString(normalizedName, "")

	return
}
