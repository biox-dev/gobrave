package provision

import (
	"context"
	"errors"
	"testing"
)

type fakeProvisioner struct {
	endpoint string
	startErr error
}

func (f *fakeProvisioner) Status(context.Context) (*RuntimeStatus, error) { return &RuntimeStatus{}, nil }

func (f *fakeProvisioner) Download(context.Context, ProgressFunc) error { return ErrUnsupported }

func (f *fakeProvisioner) Install(context.Context, ProgressFunc) error { return ErrUnsupported }

func (f *fakeProvisioner) Uninstall(context.Context) error { return ErrUnsupported }

func (f *fakeProvisioner) Start(context.Context) (string, error) {
	if f.startErr != nil {
		return "", f.startErr
	}
	return f.endpoint, nil
}

func (f *fakeProvisioner) Endpoint() string { return f.endpoint }

func (f *fakeProvisioner) Stop(context.Context) error { return nil }

func (f *fakeProvisioner) HealthCheck(context.Context) error { return nil }

func TestManagerStartNotifiesOnReady(t *testing.T) {
	m := NewManager(&fakeProvisioner{endpoint: "tcp://127.0.0.1:2375"})

	var got []string
	m.SetOnReady(func(_ context.Context, endpoint string) {
		got = append(got, endpoint)
	})

	ep, err := m.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if ep != "tcp://127.0.0.1:2375" {
		t.Fatalf("Start() endpoint = %q", ep)
	}
	if len(got) != 1 || got[0] != ep {
		t.Fatalf("onReady calls = %v, want [%s]", got, ep)
	}
}

func TestManagerStartDoesNotNotifyOnFailure(t *testing.T) {
	m := NewManager(&fakeProvisioner{startErr: errors.New("dockerd is not reachable")})

	called := false
	m.SetOnReady(func(context.Context, string) { called = true })

	if _, err := m.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want failure")
	}
	if called {
		t.Fatal("onReady called on failed Start")
	}
}

func TestManagerSetOnReadyAfterStarted(t *testing.T) {
	m := NewManager(&fakeProvisioner{endpoint: "unix:///var/run/docker.sock"})
	if _, err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	var got []string
	m.SetOnReady(func(_ context.Context, endpoint string) {
		got = append(got, endpoint)
	})
	if len(got) != 1 || got[0] != "unix:///var/run/docker.sock" {
		t.Fatalf("onReady calls = %v, want the current endpoint once", got)
	}
}
