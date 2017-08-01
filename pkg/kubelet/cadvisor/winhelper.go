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
	"github.com/bobbypage/win"
	"github.com/davecgh/go-spew/spew"
	dockerapi "github.com/docker/engine-api/client"
	dockertypes "github.com/docker/engine-api/types"
	"github.com/golang/glog"
	cadvisorapi "github.com/google/cadvisor/info/v1"
	cadvisorapiv2 "github.com/google/cadvisor/info/v2"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"
)

//CPUQuery for perf counter
const CPUQuery = "\\Processor(_Total)\\% Processor Time"
const MemoryPrivWorkingSetQuery = "\\Process(_Total)\\Working Set - Private"
const MemoryCommittedBytesQuery = "\\Memory\\Committed Bytes"

type winhelper struct {
	dockerClient                *dockerapi.Client
	DockerStats                 map[string]Stats
	cpuUsageCoreNanoSeconds     uint64
	memoryPrivWorkingSetBytes   uint64
	memoryCommitedBytes         uint64
	mu                          sync.Mutex
	memoryPhysicalCapacityBytes uint64
}

func NewWinHelper() *winhelper {
	glog.Infof("Creating NewWinHelper")

	helper := new(winhelper)
	helper.DockerStats = make(map[string]Stats)

	dockerClient, _ := dockerapi.NewEnvClient()
	helper.dockerClient = dockerClient

	// create physical memory
	var physicalMemoryKiloBytes uint64
	ok := win.GetPhysicallyInstalledSystemMemory(&physicalMemoryKiloBytes)
	glog.Infof("done making windows syscall, ret=%v, res=%v", physicalMemoryKiloBytes, ok)

	if !ok {
		glog.Infof("Error reading physical memory")
	}

	helper.memoryPhysicalCapacityBytes = physicalMemoryKiloBytes * 1000 // convert kilobytes to bytes

	go helper.startNodeMonitoring()
	return helper
}

func (ws *winhelper) startNodeMonitoring() {

	//var physicalMemoryKiloBytes uint64
	//ok := win.GetPhysicallyInstalledSystemMemory(&physicalMemoryKiloBytes)
	//glog.Infof("done making windows syscall, ret=%v, res=%v", physicalMemoryKiloBytes, ok)

	//if !ok {
	//glog.Infof("Error reading physical memory")
	//}

	//ws.memoryPhysicalCapacityBytes = physicalMemoryKiloBytes * 1000 // convert kilobytes to bytes
	glog.Infof("now memoryPhysicalBytes is %v", ws.memoryPhysicalCapacityBytes)

	//ok := win.GetPhysicallyInstalledSystemMemory(&ws.memoryPhysicalCapacityKiloBytes)
	//if !ok {
	//glog.Infof("Error reading physical memory")
	//}

	glog.Info("startNodeCPUMonitoring()")

	cpuChan, err := readPerformanceCounter(CPUQuery, 1)

	if err != nil {
		glog.Infof("Unable to read perf counter, cpu")
	}

	memWorkingSetChan, err := readPerformanceCounter(MemoryPrivWorkingSetQuery, 1)

	if err != nil {
		glog.Infof("Unable to read perf counter memory")
	}

	memCommittedBytesChan, err := readPerformanceCounter(MemoryCommittedBytesQuery, 1)

	if err != nil {
		glog.Infof("Unable to read perf counter memoryCommited")
	}

	for {
		select {
		case c := <-cpuChan:
			ws.mu.Lock()
			cpuCores := runtime.NumCPU()
			ws.cpuUsageCoreNanoSeconds += uint64((c.ValueFloat / 100.0) * float64(cpuCores) * 1000000000)
			glog.Infof("number of cpuCores %v ; UsageCoreNanoSeconds %v", cpuCores, ws.cpuUsageCoreNanoSeconds)
			ws.mu.Unlock()
		case mWorkingSet := <-memWorkingSetChan:
			ws.mu.Lock()
			ws.memoryPrivWorkingSetBytes = uint64(mWorkingSet.ValueFloat)
			ws.mu.Unlock()
		case mCommitedBytes := <-memCommittedBytesChan:
			ws.mu.Lock()
			ws.memoryCommitedBytes = uint64(mCommitedBytes.ValueFloat)
			ws.mu.Unlock()
		}
	}

	glog.Info("Exit startNodeCPUMonitoring()")
}

func (ws *winhelper) WinContainerInfos() map[string]cadvisorapiv2.ContainerInfo {
	m := make(map[string]cadvisorapiv2.ContainerInfo)

	// root (node) container
	m["/"] = ws.createRootContainerInfo()

	containers, err := ws.dockerClient.ContainerList(context.Background(), dockertypes.ContainerListOptions{})

	if err != nil {
		panic(err)
	}

	glog.Info("looping through containers")
	for _, container := range containers {
		m[container.ID] = ws.createContainerInfo(&container)
		glog.Info("added container info for container", container)
	}

	glog.Info("end winContainerInfos", m)
	return m
}
func (ws *winhelper) createRootContainerInfo() cadvisorapiv2.ContainerInfo {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	stats := make([]*cadvisorapiv2.ContainerStats, 1)
	stats[0] = &cadvisorapiv2.ContainerStats{
		Cpu: &cadvisorapi.CpuStats{
			Usage: cadvisorapi.CpuUsage{
				Total: ws.cpuUsageCoreNanoSeconds,
			},
		},
		Memory: &cadvisorapi.MemoryStats{
			WorkingSet: ws.memoryPrivWorkingSetBytes,
			Usage:      ws.memoryCommitedBytes,
		},
		//"memory": {
		//"time": "2017-07-31T23:17:57Z",
		//"usageBytes": 162115584,
		//"workingSetBytes": 86888448,
		//"rssBytes": 60649472,
		//"pageFaults": 1919023,
		//"majorPageFaults": 726

		//Memory: &cadvisorapi.MemoryStats{
		//WorkingSet: 86888448,
		//Usage:      162115584,
		//RSS:        60649472,
		//},
	}
	//var res uint64
	//ret := win.GetPhysicallyInstalledSystemMemory(&res)
	//glog.Infof("made windows syscall, ret=%v, res=%v", ret, res)

	rootInfo := cadvisorapiv2.ContainerInfo{
		Spec: cadvisorapiv2.ContainerSpec{
			Namespace: "testNameSpace",
			Image:     "davidImage",
			HasCpu:    true,
			HasMemory: true,
			Memory: cadvisorapiv2.MemorySpec{
				//Limit: ws.memoryPhysicalCapacityKiloBytes * 1000, // convert to kilobytes to bytes
				//Limit: 3.2e+10,
				Limit: ws.memoryPhysicalCapacityBytes,
			},
		},
		Stats: stats,
	}

	glog.Infof("created root container", spew.Sdump(rootInfo))
	return rootInfo
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
		HasFilesystem:    false,
		HasDiskIo:        false,
		Image:            container.Image,
	}
	stats := make([]*cadvisorapiv2.ContainerStats, 1)

	stats = append(stats, ws.createContainerStats(container))

	//stats.append(
	//	&cadvisorapiv2.ContainerStats{Cpu: &cadvisorapi.CpuStats{Usage: cadvisorapi.CpuUsage{Total: GetWinHelper().GetUsageCoreNanoSeconds()}}})

	return cadvisorapiv2.ContainerInfo{Spec: spec, Stats: stats}
}

func (ws *winhelper) createContainerStats(container *dockertypes.Container) *cadvisorapiv2.ContainerStats {
	dockerStatsJson := ws.getStatsForContainer(container.ID)

	dockerStats := dockerStatsJson.Stats
	// create network stats

	networkInterfaces := make([]cadvisorapi.InterfaceStats, len(dockerStatsJson.Networks))
	for networkName, networkStats := range dockerStatsJson.Networks {

		networkInterfaces = append(networkInterfaces, cadvisorapi.InterfaceStats{
			Name:      networkName,
			RxBytes:   networkStats.RxBytes,
			RxPackets: networkStats.RxPackets,
			RxErrors:  networkStats.RxErrors,
			RxDropped: networkStats.RxDropped,
			TxBytes:   networkStats.TxBytes,
			TxPackets: networkStats.TxPackets,
			TxErrors:  networkStats.TxErrors,
			TxDropped: networkStats.TxDropped,
		})
	}

	stats := cadvisorapiv2.ContainerStats{
		Timestamp: time.Unix(container.Created, 0),
		Cpu:       &cadvisorapi.CpuStats{Usage: cadvisorapi.CpuUsage{Total: dockerStats.CPUStats.CPUUsage.TotalUsage}},
		CpuInst:   &cadvisorapiv2.CpuInstStats{},
		Memory:    &cadvisorapi.MemoryStats{WorkingSet: dockerStats.MemoryStats.PrivateWorkingSet, Usage: dockerStats.MemoryStats.Commit},
		Network:   &cadvisorapiv2.NetworkStats{Interfaces: networkInterfaces},
		// TODO: ... diskio, memory, network, etc...
	}
	return &stats
}

func (ws *winhelper) getStatsForContainer(containerId string) StatsJSON {
	response, err := ws.dockerClient.ContainerStats(context.Background(), containerId, false)
	defer response.Close()

	if err != nil {
		panic(err)
	}
	dec := json.NewDecoder(response)

	var stats StatsJSON
	err = dec.Decode(&stats)

	if err != nil {
		panic("cant parse json")
	}

	return stats
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
