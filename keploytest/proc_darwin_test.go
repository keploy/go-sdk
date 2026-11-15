package keploytest

import (
	"os"
	"os/exec"
	"testing"
)

// macOS's process table answers each process's parent pid and start time:
// this process's parent is the process that started it, a process it starts
// is its child and started no earlier, and a pid that no process holds any
// more has no answer, so no parent.
func TestMacOSProcessTableAnswersParentAndStart(t *testing.T) {
	ppid, started, ok := kinfoProc(os.Getpid())
	if !ok || ppid != os.Getppid() || started <= 0 {
		t.Fatalf("this process: parent %d, start %d, %v; want parent %d, a start", ppid, started, ok, os.Getppid())
	}
	if p, ok := systemParent(os.Getpid()); !ok || p != os.Getppid() {
		t.Fatalf("systemParent(self) = %d, %v; want %d", p, ok, os.Getppid())
	}
	c := startIdle(t)
	cp, cs, ok := kinfoProc(c.Process.Pid)
	if !ok || cp != os.Getpid() || cs < started {
		t.Fatalf("a child: parent %d, start %d, %v; want parent %d, a start no earlier than %d", cp, cs, ok, os.Getpid(), started)
	}
	ended := exec.Command("/usr/bin/true")
	if err := ended.Run(); err != nil {
		t.Fatal(err)
	}
	if p, s, ok := kinfoProc(ended.Process.Pid); ok {
		t.Fatalf("an ended, reaped process %d answers parent %d, start %d", ended.Process.Pid, p, s)
	}
	if p, ok := systemParent(ended.Process.Pid); ok {
		t.Fatalf("an ended, reaped process %d has parent %d", ended.Process.Pid, p)
	}
}

// The table is read one process at a time: a parent pid read in one answer
// that has passed to a later process (started after the child) is not the
// child's parent, and the child is judged by the start read in the same
// answer as its parent pid, not by a later answer for its pid, which may be
// another process's by then.
func TestMacOSParentIsJudgedByOneAnswer(t *testing.T) {
	type answer struct {
		ppid    int
		started int64
	}
	reads := map[int]int{}
	table := map[int][]answer{
		7:  {{3, 100}, {1, 900}}, // 7 is read once, then passes to a process started at 900
		3:  {{1, 300}},           // 3 started after 7's first process: a reused pid
		8:  {{4, 500}},
		4:  {{1, 400}}, // 4 started before 8: its parent
		1:  {{0, 10}},
		12: {{5, 600}}, // 5 is answered by no process
	}
	was := procInfo
	procInfo = func(pid int) (int, int64, bool) {
		a, ok := table[pid]
		if !ok {
			return 0, 0, false
		}
		i := reads[pid]
		if i >= len(a) {
			i = len(a) - 1
		}
		reads[pid]++
		return a[i].ppid, a[i].started, true
	}
	t.Cleanup(func() { procInfo = was })
	for _, tc := range []struct {
		pid, want int
		ok        bool
	}{
		{7, 0, false},  // its parent pid is a later process's
		{8, 4, true},   // its parent started before it
		{1, 0, true},   // launchd: the parent of none
		{12, 0, false}, // its parent is gone
		{99, 0, false}, // no answer
	} {
		for k := range reads {
			delete(reads, k)
		}
		if got, ok := systemParent(tc.pid); got != tc.want || ok != tc.ok {
			t.Errorf("systemParent(%d) = %d, %v; want %d, %v", tc.pid, got, ok, tc.want, tc.ok)
		}
	}
}
