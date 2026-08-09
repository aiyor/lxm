package lxd

import (
	"context"
	"errors"
	"testing"
	"time"

	lxd_client "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"
	"github.com/gorilla/websocket"
)

type mockLXDOperation struct {
	cancelCalled bool
	waitBlock    chan struct{}
}

func (m *mockLXDOperation) AddHandler(handler func(api.Operation)) (*lxd_client.EventTarget, error) {
	return nil, nil
}
func (m *mockLXDOperation) Cancel() error {
	m.cancelCalled = true
	if m.waitBlock != nil {
		select {
		case <-m.waitBlock:
		default:
			close(m.waitBlock)
		}
	}
	return nil
}
func (m *mockLXDOperation) CancelTarget() error                                { return nil }
func (m *mockLXDOperation) Get() api.Operation                                 { return api.Operation{} }
func (m *mockLXDOperation) GetWebsocket(path string) (*websocket.Conn, error)  { return nil, nil }
func (m *mockLXDOperation) RemoveHandler(target *lxd_client.EventTarget) error { return nil }
func (m *mockLXDOperation) Wait() error {
	if m.waitBlock != nil {
		<-m.waitBlock
	}
	if m.cancelCalled {
		return errors.New("operation cancelled")
	}
	return nil
}
func (m *mockLXDOperation) WaitContext(ctx context.Context) error { return m.Wait() }
func (m *mockLXDOperation) Refresh() error                        { return nil }

func TestWaitOpContext_CancellationCallsCancel(t *testing.T) {
	mockOp := &mockLXDOperation{
		waitBlock: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := waitOpContext(ctx, mockOp)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
	if !mockOp.cancelCalled {
		t.Errorf("expected op.Cancel() to be called on ctx.Done(), but it was not")
	}
}

func TestWaitOpContext_Success(t *testing.T) {
	mockOp := &mockLXDOperation{}
	err := waitOpContext(context.Background(), mockOp)
	if err != nil {
		t.Errorf("expected nil error on success, got %v", err)
	}
}
