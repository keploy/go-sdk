//go:build windows

package keploytest

import (
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// namesFoldCase: Windows matches file names without case (GO.EXE is go.exe).
const namesFoldCase = true

// readsParents: the package reads Windows' process table.
func readsParents() bool { return true }

const (
	processQueryLimitedInformation = 0x1000
	// processCommandLineInformation is NtQueryInformationProcess's class for
	// a process's command line (Windows 8.1 and later).
	processCommandLineInformation = 60
	statusInfoLengthMismatch      = 0xC0000004
)

var ntQueryInformationProcess = syscall.NewLazyDLL("ntdll.dll").NewProc("NtQueryInformationProcess")

// systemParent is process pid's parent, from the system's process snapshot,
// while that parent is the process that started pid (trueParent).
func systemParent(pid int) (int, bool) {
	recorded, ok := snapshotParent(uint32(pid))
	if !ok {
		return 0, false
	}
	return trueParent(pid, recorded, creationTime)
}

// snapshotParent is the parent pid the system recorded for process pid.
func snapshotParent(pid uint32) (int, bool) {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(snap)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		if e.ProcessID == pid {
			return int(e.ParentProcessID), true
		}
	}
	return 0, false
}

// creationTime is when process pid was created, in 100 ns ticks.
func creationTime(pid int) (int64, bool) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer syscall.CloseHandle(h)
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, false
	}
	return created.Nanoseconds() / 100, true
}

// systemCmdline is process pid's command line, as the process's own
// arguments (splitCommandLine).
func systemCmdline(pid int) ([]string, bool) {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return nil, false
	}
	defer syscall.CloseHandle(h)
	// The answer is a UNICODE_STRING whose text follows it in the buffer. A
	// command line longer than the buffer is answered with its size.
	buf := make([]byte, 512)
	for tries := 0; tries < 4; tries++ {
		var need uint32
		st, _, _ := ntQueryInformationProcess.Call(uintptr(h), processCommandLineInformation,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&need)))
		if st == statusInfoLengthMismatch && int(need) > len(buf) {
			buf = make([]byte, need)
			continue
		}
		if st != 0 {
			return nil, false
		}
		line, ok := unicodeStringIn(buf)
		if !ok {
			return nil, false
		}
		return splitCommandLine(line), true
	}
	return nil, false
}

// unicodeString is Windows' UNICODE_STRING: Length bytes of UTF-16 text at
// Buffer.
type unicodeString struct {
	Length, MaximumLength uint16
	Buffer                uintptr
}

// unicodeStringIn is the text of the UNICODE_STRING at the start of buf,
// whose characters lie in buf itself.
func unicodeStringIn(buf []byte) (string, bool) {
	if len(buf) < int(unsafe.Sizeof(unicodeString{})) {
		return "", false
	}
	us := (*unicodeString)(unsafe.Pointer(&buf[0]))
	start := us.Buffer - uintptr(unsafe.Pointer(&buf[0]))
	n := uintptr(us.Length)
	if us.Buffer < uintptr(unsafe.Pointer(&buf[0])) || start+n > uintptr(len(buf)) || n%2 != 0 {
		return "", false
	}
	u := make([]uint16, n/2)
	for i := range u {
		u[i] = uint16(buf[start+2*uintptr(i)]) | uint16(buf[start+2*uintptr(i)+1])<<8
	}
	return string(utf16.Decode(u)), true
}
