package webapp

import "testing"

func TestDualKernelDistributesLoad(t *testing.T) {
	k := newDualKernel(t.TempDir(), 2)
	l1, ok := k.acquire()
	if !ok {
		t.Fatal("第一个 run 应获得 lane")
	}
	l2, ok := k.acquire()
	if !ok {
		t.Fatal("第二个 run 应获得 lane")
	}
	if l1 == l2 {
		t.Fatalf("两个 run 应分到不同 lane, got %s", l1.role)
	}
	k.release(l1)
	k.release(l2)
}

func TestDualKernelCapacity(t *testing.T) {
	k := newDualKernel(t.TempDir(), 1)
	l1, _ := k.acquire()
	l2, _ := k.acquire()
	if _, ok := k.acquire(); ok {
		t.Fatal("两个 lane 都满时应拒绝")
	}
	k.release(l1)
	k.release(l2)
}
