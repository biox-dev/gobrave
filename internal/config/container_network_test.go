package config

import "testing"

func TestNormalizeContainerNetworkMode(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", ContainerNetworkModeContainerIP, false},
		{"  ", ContainerNetworkModeContainerIP, false},
		{"container_ip", ContainerNetworkModeContainerIP, false},
		{" CONTAINER_IP ", ContainerNetworkModeContainerIP, false},
		{"host_port", ContainerNetworkModeHostPort, false},
		{"Host_Port", ContainerNetworkModeHostPort, false},
		{"bridge", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeContainerNetworkMode(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("NormalizeContainerNetworkMode(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeContainerNetworkMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveContainerNetworkModeDefaults(t *testing.T) {
	// 默认（未配置）必须是 container_ip：不发布宿主机端口，行为与历史一致。
	if got := ResolveContainerNetworkMode(nil); got != ContainerNetworkModeContainerIP {
		t.Errorf("nil config = %q, want %q", got, ContainerNetworkModeContainerIP)
	}
	if got := ResolveContainerNetworkMode(&Config{}); got != ContainerNetworkModeContainerIP {
		t.Errorf("empty config = %q, want %q", got, ContainerNetworkModeContainerIP)
	}
	// 非法值同样回退默认，不阻断启动。
	cfg := &Config{Container: &ContainerConfig{NetworkMode: "nonsense"}}
	if got := ResolveContainerNetworkMode(cfg); got != ContainerNetworkModeContainerIP {
		t.Errorf("invalid mode = %q, want %q", got, ContainerNetworkModeContainerIP)
	}
	cfg.Container.NetworkMode = "host_port"
	if got := ResolveContainerNetworkMode(cfg); got != ContainerNetworkModeHostPort {
		t.Errorf("host_port = %q, want %q", got, ContainerNetworkModeHostPort)
	}
}
