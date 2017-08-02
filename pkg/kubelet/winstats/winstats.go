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

package winstats

import (
	"context"
	"encoding/json"
	"github.com/bobbypage/win"
	dockerapi "github.com/docker/engine-api/client"
	dockertypes "github.com/docker/engine-api/types"
	//"github.com/golang/glog"
	"errors"
	cadvisorapi "github.com/google/cadvisor/info/v1"
	cadvisorapiv2 "github.com/google/cadvisor/info/v2"
	"runtime"
	"sync"
	"time"
)

type Client struct {
	dockerClient                *dockerapi.Client
	cpuUsageCoreNanoSeconds     uint64
	memoryPrivWorkingSetBytes   uint64
	memoryCommitedBytes         uint64
	mu                          sync.Mutex
	memoryPhysicalCapacityBytes uint64
}

func NewClient() (*Client, error) {
	client := new(Client)

	dockerClient, _ := dockerapi.NewEnvClient()
	client.dockerClient = dockerClient

	// create physical memory
	var physicalMemoryKiloBytes uint64
	ok := win.GetPhysicallyInstalledSystemMemory(&physicalMemoryKiloBytes)

	if !ok {
		return nil, errors.New("Error reading physical memory")
	}

	memory, err := getPhysicallyInstalledSystemMemoryBytes()

	if err != nil {
		return nil, err
	}

	client.memoryPhysicalCapacityBytes = memory

	// start node monitoring (reading perf counters)
	errChan := make(chan error, 1)
	go client.startNodeMonitoring(errChan)

	err = <-errChan
	return client, err
}

func (c *Client) startNodeMonitoring(errChan chan error) {
	cpuChan, err := readPerformanceCounter(CPUQuery, 1)

	if err != nil {
		errChan <- err
		return
	}

	memWorkingSetChan, err := readPerformanceCounter(MemoryPrivWorkingSetQuery, 1)

	if err != nil {
		errChan <- err
		return
	}

	memCommittedBytesChan, err := readPerformanceCounter(MemoryCommittedBytesQuery, 1)

	if err != nil {
		errChan <- err
		return
	}

	// no error, send nil over channel
	errChan <- nil

	for {
		select {
		case cpu := <-cpuChan:
			c.mu.Lock()
			cpuCores := runtime.NumCPU()
			c.cpuUsageCoreNanoSeconds += uint64((cpu.Value / 100.0) * float64(cpuCores) * 1000000000)
			c.mu.Unlock()
		case mWorkingSet := <-memWorkingSetChan:
			c.mu.Lock()
			c.memoryPrivWorkingSetBytes = uint64(mWorkingSet.Value)
			c.mu.Unlock()
		case mCommitedBytes := <-memCommittedBytesChan:
			c.mu.Lock()
			c.memoryCommitedBytes = uint64(mCommitedBytes.Value)
			c.mu.Unlock()
		}
	}
}

func (c *Client) WinContainerInfos() (map[string]cadvisorapiv2.ContainerInfo, error) {
	infos := make(map[string]cadvisorapiv2.ContainerInfo)

	// root (node) container
	infos["/"] = *c.createRootContainerInfo()

	containers, err := c.dockerClient.ContainerList(context.Background(), dockertypes.ContainerListOptions{})

	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		containerInfo, err := c.createContainerInfo(&container)

		if err != nil {
			return nil, err
		}

		infos[container.ID] = *containerInfo
	}

	return infos, nil
}
func (c *Client) createRootContainerInfo() *cadvisorapiv2.ContainerInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	stats := make([]*cadvisorapiv2.ContainerStats, 1)
	stats[0] = &cadvisorapiv2.ContainerStats{
		Cpu: &cadvisorapi.CpuStats{
			Usage: cadvisorapi.CpuUsage{
				Total: c.cpuUsageCoreNanoSeconds,
			},
		},
		Memory: &cadvisorapi.MemoryStats{
			WorkingSet: c.memoryPrivWorkingSetBytes,
			Usage:      c.memoryCommitedBytes,
		},
	}

	rootInfo := cadvisorapiv2.ContainerInfo{
		Spec: cadvisorapiv2.ContainerSpec{
			HasCpu:    true,
			HasMemory: true,
			Memory: cadvisorapiv2.MemorySpec{
				Limit: c.memoryPhysicalCapacityBytes,
			},
		},
		Stats: stats,
	}

	//glog.Infof("created root container", spew.Sdump(rootInfo))
	return &rootInfo
}
func (c *Client) createContainerInfo(container *dockertypes.Container) (*cadvisorapiv2.ContainerInfo, error) {

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
	containerStats, err := c.createContainerStats(container)

	if err != nil {
		return nil, err
	}

	stats = append(stats, containerStats)
	return &cadvisorapiv2.ContainerInfo{Spec: spec, Stats: stats}, nil
}

func (c *Client) createContainerStats(container *dockertypes.Container) (*cadvisorapiv2.ContainerStats, error) {
	dockerStatsJson, err := c.getStatsForContainer(container.ID)

	if err != nil {
		return nil, err
	}

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
	return &stats, nil
}

func (c *Client) getStatsForContainer(containerId string) (*StatsJSON, error) {
	response, err := c.dockerClient.ContainerStats(context.Background(), containerId, false)
	defer response.Close()

	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(response)

	var stats StatsJSON
	err = dec.Decode(&stats)

	if err != nil {
		return nil, err
	}

	return &stats, nil
}
