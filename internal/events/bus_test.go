package events

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBusFanOutAndDrop(t *testing.T) {
	b := NewBus[int]()
	a := b.Subscribe(10)
	slow := b.Subscribe(1)
	for i := 0; i < 5; i++ {
		b.Publish(i)
	}
	assert.Equal(t, 2, b.Len())
	assert.Len(t, a.C, 5)
	assert.Len(t, slow.C, 1)
	assert.Equal(t, uint64(4), slow.Dropped())
	slow.Close()
	slow.Close() // idempotent
	assert.Equal(t, 1, b.Len())
	<-slow.C // the buffered value is still delivered
	_, open := <-slow.C
	assert.False(t, open)
}

func TestBusConcurrent(t *testing.T) {
	b := NewBus[string]()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); s := b.Subscribe(4); s.Close() }()
		go func() { defer wg.Done(); b.Publish("x") }()
	}
	wg.Wait()
}

func TestRing(t *testing.T) {
	r := NewRing[int](3)
	assert.Empty(t, r.Values())
	r.Push(1)
	r.Push(2)
	assert.Equal(t, []int{1, 2}, r.Values())
	r.Push(3)
	r.Push(4)
	assert.Equal(t, []int{2, 3, 4}, r.Values())
	assert.Equal(t, []int{3, 4}, r.Last(2))
	assert.Equal(t, []int{2, 3, 4}, r.Last(0))
}
