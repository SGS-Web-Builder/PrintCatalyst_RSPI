package dispatch

import "testing"

func TestWakeCoalescesWithoutBlocking(t *testing.T) {
	d := New(nil, nil, nil, nil, DispatcherConfig{})
	for i := 0; i < 1000; i++ {
		d.Wake()
	}
	select {
	case <-d.wake:
	default:
		t.Fatal("release wake lost")
	}
	select {
	case <-d.wake:
		t.Fatal("wake requests were not coalesced")
	default:
	}
	d.Wake()
	select {
	case <-d.wake:
	default:
		t.Fatal("next release wake lost")
	}
}
