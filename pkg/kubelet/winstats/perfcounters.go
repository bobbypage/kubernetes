// +build windows
package winstats

import (
	"errors"
	"fmt"
	"github.com/lxn/win"
	"time"
	"unsafe"
)

const (
	CPUQuery                  = "\\Processor(_Total)\\% Processor Time"
	MemoryPrivWorkingSetQuery = "\\Process(_Total)\\Working Set - Private"
	MemoryCommittedBytesQuery = "\\Memory\\Committed Bytes"
)

// Metric collected from a counter
type Metric struct {
	Name  string
	Value float64
}

// NewMetric construct a Metric struct
func NewMetric(name string, value float64) Metric {
	return Metric{
		Name:  name,
		Value: value,
	}
}

func (metric Metric) String() string {
	return fmt.Sprintf(
		"Name: %s | Value: %s",
		metric.Name,
		metric.Value,
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

							metric = Metric{
								counter,
								c.FmtValue.DoubleValue,
							}
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
