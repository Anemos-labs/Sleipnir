package widget

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const workUnit = "CPU cycles"
const workResolution = uint64(1)

var queryProcessCycleTime = windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryProcessCycleTime")

// processWork reads CPU cycles across all process threads. Comparing cycles
// avoids scheduler wait time and the coarse accounting ticks of GetProcessTimes.
// Cycles are not converted to a duration: their rate depends on the processor.
func processWork() (uint64, error) {
	var cycles uint64
	ok, _, err := queryProcessCycleTime.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&cycles)))
	if ok == 0 {
		return 0, err
	}
	return cycles, nil
}
