// +build windows
package winstats

import (
	"errors"
	"github.com/bobbypage/win"
)

func getPhysicallyInstalledSystemMemoryBytes() (uint64, error) {
	var physicalMemoryKiloBytes uint64
	ok := win.GetPhysicallyInstalledSystemMemory(&physicalMemoryKiloBytes)

	if !ok {
		return 0, errors.New("Unable to read physical memory")
	}
	return physicalMemoryKiloBytes * 1000, nil // convert kilobytes to bytes
}
