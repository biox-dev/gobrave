
panic: could not build arguments for function "github.com/biox-dev/gobrave/internal/container".BuildContainer.func9 (/ssd1/wy/workspace3/go-project/gobrave/internal/container/container.go:300): could not build value group event.Handler[group="event_handlers"]: could not build arguments for function "github.com/biox-dev/gobrave/internal/route".NewRouteRegistryHandler (/ssd1/wy/workspace3/go-project/gobrave/internal/route/handler.go:24): failed to build route.RouteRegistry: received non-nil error from function "github.com/biox-dev/gobrave/internal/container".BuildContainer.func3 (/ssd1/wy/workspace3/go-project/gobrave/internal/container/container.go:120): load kubeconfig /home/admin/.kube/config: stat /home/admin/.kube/config: no such file or directory

goroutine 1 [running]:
github.com/biox-dev/gobrave/internal/container.must({0x44e5ca0, 0xc001599a40})
	/ssd1/wy/workspace3/go-project/gobrave/internal/container/container.go:51 +0x55
github.com/biox-dev/gobrave/internal/container.BuildContainer(0xc0002961d8)
	/ssd1/wy/workspace3/go-project/gobrave/internal/container/container.go:300 +0x12ba
main.main()
	/ssd1/wy/workspace3/go-project/gobrave/cmd/server/main.go:45 +0x132