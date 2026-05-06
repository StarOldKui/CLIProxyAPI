package usage

import (
	"context"
	"testing"
	"time"
)

func TestManagerStopWaitsForDrain(t *testing.T) {
	m := NewManager(1)
	plugin := &blockingPlugin{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	m.Register(plugin)
	m.Publish(context.Background(), Record{Model: "gpt-5.4"})

	<-plugin.started
	stopped := make(chan struct{})
	go func() {
		m.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop returned before queued usage was dispatched")
	case <-time.After(20 * time.Millisecond):
	}

	close(plugin.release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return after queue drained")
	}
}

type blockingPlugin struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingPlugin) HandleUsage(context.Context, Record) {
	close(p.started)
	<-p.release
}
