/*
Copyright 2017 The Kubernetes Authors.

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

// Package winstats provides a client to get node and pod level stats on windows
package winstats

import (
	"time"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	cadvisorapiv2 "github.com/google/cadvisor/info/v2"
)

// Client is an object that is used to get stats information.
type Client struct {
	winNodeStatsClient
}

type winNodeStatsClient interface {
	startMonitoring() error
	getNodeStats() (*nodeStats, error)
	getMachineInfo() (*cadvisorapi.MachineInfo, error)
	getVersionInfo() (*cadvisorapi.VersionInfo, error)
}

type metric struct {
	Name      string
	Value     uint64
	Timestamp time.Time
}

type nodeStats struct {
	cpuUsageCoreNanoSeconds     metric
	memoryPrivWorkingSetBytes   metric
	memoryCommittedBytes        metric
	memoryPhysicalCapacityBytes uint64
	kernelVersion               string
}

// NewClient constructs a Client.
func NewClient(statsClient winNodeStatsClient) (*Client, error) {
	client := new(Client)
	client.winNodeStatsClient = statsClient

	err := client.startMonitoring()

	if err != nil {
		return nil, err
	}

	return client, nil
}

// WinContainerInfos returns a map of container infos. The map contains node and
// pod level stats. Analogous to cadvisor GetContainerInfoV2 method.
func (c *Client) WinContainerInfos() (map[string]cadvisorapiv2.ContainerInfo, error) {
	infos := make(map[string]cadvisorapiv2.ContainerInfo)
	rootContainerInfo, err := c.createRootContainerInfo()

	if err != nil {
		return nil, err
	}

	infos["/"] = *rootContainerInfo

	return infos, nil
}

// WinMachineInfo returns a cadvisorapi.MachineInfo with details about the
// node machine. Analogous to cadvisor MachineInfo method.
func (c *Client) WinMachineInfo() (*cadvisorapi.MachineInfo, error) {
	return c.getMachineInfo()
}

// WinVersionInfo returns a  cadvisorapi.VersionInfo with version info of
// the kernel and docker runtime. Analogous to cadvisor VersionInfo method.
func (c *Client) WinVersionInfo() (*cadvisorapi.VersionInfo, error) {
	return c.getVersionInfo()
}

func (c *Client) createRootContainerInfo() (*cadvisorapiv2.ContainerInfo, error) {
	nodeStats, err := c.getNodeStats()

	if err != nil {
		return nil, err
	}
	var stats []*cadvisorapiv2.ContainerStats

	stats = append(stats, &cadvisorapiv2.ContainerStats{
		Timestamp: nodeStats.cpuUsageCoreNanoSeconds.Timestamp,
		Cpu: &cadvisorapi.CpuStats{
			Usage: cadvisorapi.CpuUsage{
				Total: nodeStats.cpuUsageCoreNanoSeconds.Value,
			},
		},
		Memory: &cadvisorapi.MemoryStats{
			WorkingSet: nodeStats.memoryPrivWorkingSetBytes.Value,
			Usage:      nodeStats.memoryCommittedBytes.Value,
		},
	})

	rootInfo := cadvisorapiv2.ContainerInfo{
		Spec: cadvisorapiv2.ContainerSpec{
			HasCpu:    true,
			HasMemory: true,
			Memory: cadvisorapiv2.MemorySpec{
				Limit: nodeStats.memoryPhysicalCapacityBytes,
			},
		},
		Stats: stats,
	}

	return &rootInfo, nil
}
