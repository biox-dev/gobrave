package docker

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
)

func TestToPortBindingsDisabled(t *testing.T) {
	// 默认模式（container_ip）不发布端口：既不占用宿主机端口，也不会与
	// 同类容器（如两个 RStudio）冲突。
	if got := toPortBindings(false, 8787); got != nil {
		t.Errorf("toPortBindings(publish=false) = %v, want nil", got)
	}
	// publish=true 但端口非法时同样不发布。
	if got := toPortBindings(true, 0); got != nil {
		t.Errorf("toPortBindings(port=0) = %v, want nil", got)
	}
}

func TestToPortBindingsUsesRandomHostPort(t *testing.T) {
	got := toPortBindings(true, 8787)
	bindings, ok := got["8787/tcp"]
	if !ok {
		t.Fatalf("missing 8787/tcp binding, got %v", got)
	}
	if len(bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(bindings))
	}
	if bindings[0].HostPort != "" {
		t.Errorf("HostPort = %q, want empty (daemon-assigned)", bindings[0].HostPort)
	}
	if bindings[0].HostIP != "0.0.0.0" {
		t.Errorf("HostIP = %q, want 0.0.0.0", bindings[0].HostIP)
	}
}

func TestPublishedAddress(t *testing.T) {
	info := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			NetworkSettingsBase: container.NetworkSettingsBase{
				Ports: nat.PortMap{
					"8787/tcp": []nat.PortBinding{
						{HostIP: "0.0.0.0", HostPort: "49812"},
					},
				},
			},
		},
	}
	host, port := publishedAddress(info)
	if host != "127.0.0.1" || port != 49812 {
		t.Errorf("publishedAddress = (%q, %d), want (127.0.0.1, 49812)", host, port)
	}
}

func TestPublishedAddressWhenNothingPublished(t *testing.T) {
	if host, port := publishedAddress(container.InspectResponse{}); host != "" || port != 0 {
		t.Errorf("publishedAddress(empty) = (%q, %d), want (\"\", 0)", host, port)
	}
	// 有端口表但没有实际绑定（host_port 模式关闭时）同样视为未发布。
	info := container.InspectResponse{
		NetworkSettings: &container.NetworkSettings{
			NetworkSettingsBase: container.NetworkSettingsBase{
				Ports: nat.PortMap{"8787/tcp": nil},
			},
		},
	}
	if host, port := publishedAddress(info); host != "" || port != 0 {
		t.Errorf("publishedAddress(unbound) = (%q, %d), want (\"\", 0)", host, port)
	}
}
