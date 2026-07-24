package presence

import (
	"errors"
	"testing"
)

func TestProbeWithConnectedClient(t *testing.T) {
	client := &probeClient{}
	if err := probeWith(client, "app-id"); err != nil {
		t.Fatal(err)
	}
	if client.loginAppID != "app-id" {
		t.Fatalf("login app ID = %q, want app-id", client.loginAppID)
	}
	if client.logoutCalls != 1 {
		t.Fatalf("logout calls = %d, want 1", client.logoutCalls)
	}
	if client.setCalls != 0 {
		t.Fatalf("set activity calls = %d, want 0", client.setCalls)
	}
}

func TestProbeWithLoginError(t *testing.T) {
	loginErr := errors.New("discord unavailable")
	client := &probeClient{loginErr: loginErr}
	if err := probeWith(client, "app-id"); !errors.Is(err, loginErr) {
		t.Fatalf("probeWith error = %v, want %v", err, loginErr)
	}
	if client.logoutCalls != 0 {
		t.Fatalf("logout calls = %d, want 0", client.logoutCalls)
	}
}

func TestProbeWithStatusStateErrors(t *testing.T) {
	tests := []error{
		ErrDiscordIPCNotFound,
		ErrDiscordIPCUnreachable,
		ErrDiscordIPCHandshakeTimeout,
	}
	for _, want := range tests {
		t.Run(want.Error(), func(t *testing.T) {
			client := &probeClient{loginErr: want}
			if err := probeWith(client, "app-id"); !errors.Is(err, want) {
				t.Fatalf("probeWith error = %v, want %v", err, want)
			}
			if client.logoutCalls != 0 {
				t.Fatalf("logout calls = %d, want 0", client.logoutCalls)
			}
		})
	}
}

func TestProbeWithEmptyAppIDUsesDefault(t *testing.T) {
	client := &probeClient{}
	if err := probeWith(client, ""); err != nil {
		t.Fatal(err)
	}
	if client.loginAppID != DefaultAppID {
		t.Fatalf("login app ID = %q, want default", client.loginAppID)
	}
}

type probeClient struct {
	loginErr    error
	loginAppID  string
	setCalls    int
	logoutCalls int
}

func (p *probeClient) Login(appID string) error {
	p.loginAppID = appID
	return p.loginErr
}

func (p *probeClient) SetActivity(Activity) error {
	p.setCalls++
	return nil
}

func (p *probeClient) Logout() error {
	p.logoutCalls++
	return nil
}
