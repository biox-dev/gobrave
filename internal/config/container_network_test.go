package config

import (
	"runtime"
	"testing"
)

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

// TestDesktopDefaults 校验 Windows 桌面端的默认值：启用运行时预置自检，
// 并使用 host_port 访问容器端口；其他平台保持服务端默认值。
func TestDesktopDefaults(t *testing.T) {
	cfg := defaultConfig(LoadOptions{Desktop: true})

	wantMode := ContainerNetworkModeContainerIP
	wantEnabled := false
	if runtime.GOOS == "windows" {
		wantMode = ContainerNetworkModeHostPort
		wantEnabled = true
	}

	if got := ResolveContainerNetworkMode(cfg); got != wantMode {
		t.Errorf("desktop network_mode = %q, want %q", got, wantMode)
	}
	if got := RuntimeProvisionEnabled(cfg); got != wantEnabled {
		t.Errorf("desktop provision.enabled = %v, want %v", got, wantEnabled)
	}

	// 服务端形态不受影响。
	serverCfg := defaultConfig(LoadOptions{})
	if got := ResolveContainerNetworkMode(serverCfg); got != ContainerNetworkModeContainerIP {
		t.Errorf("server network_mode = %q, want %q", got, ContainerNetworkModeContainerIP)
	}
	if RuntimeProvisionEnabled(serverCfg) {
		t.Error("server provision.enabled = true, want false")
	}
}

// TestResolveContainerNetworkModeFallback 校验空值与非法值回退到形态默认值：
// 服务端回退 container_ip，Windows 桌面端回退 host_port。
func TestResolveContainerNetworkModeFallback(t *testing.T) {
	wantDesktop := ContainerNetworkModeContainerIP
	if runtime.GOOS == "windows" {
		wantDesktop = ContainerNetworkModeHostPort
	}

	for _, raw := range []string{"", "  ", "nonsense"} {
		c := &ContainerConfig{NetworkMode: raw}
		if got := resolveContainerNetworkMode(c, LoadOptions{}); got != ContainerNetworkModeContainerIP {
			t.Errorf("server fallback(%q) = %q, want %q", raw, got, ContainerNetworkModeContainerIP)
		}
		if got := resolveContainerNetworkMode(c, LoadOptions{Desktop: true}); got != wantDesktop {
			t.Errorf("desktop fallback(%q) = %q, want %q", raw, got, wantDesktop)
		}
	}
}

// TestDesktopDefaultsOverriddenByConfigFile 校验 config.yml 中的显式取值
// 会覆盖桌面端默认值。
func TestDesktopDefaultsOverriddenByConfigFile(t *testing.T) {
	cfg := defaultConfig(LoadOptions{Desktop: true})
	data := []byte("container:\n  network_mode: container_ip\n  docker:\n    provision:\n      enabled: false\n")
	if err := applyConfigData(cfg, data, LoadOptions{Desktop: true}); err != nil {
		t.Fatalf("applyConfigData: %v", err)
	}

	if got := ResolveContainerNetworkMode(cfg); got != ContainerNetworkModeContainerIP {
		t.Errorf("network_mode = %q, want %q", got, ContainerNetworkModeContainerIP)
	}
	if RuntimeProvisionEnabled(cfg) {
		t.Error("provision.enabled = true, want false")
	}
}
