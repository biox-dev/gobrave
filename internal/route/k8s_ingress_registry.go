package route

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type K8sIngressRegistryConfig struct {
	Namespace        string
	Kubeconfig       string
	InCluster        bool
	IngressClassName string
	Host             string
	PathType         string
	Annotations      map[string]string
}

type K8sIngressRegistry struct {
	clientset               kubernetes.Interface
	dynamicClient           dynamic.Interface
	namespace               string
	ingressClassName        string
	host                    string
	pathType                networkingv1.PathType
	defaultAnnotations      map[string]string
	middlewareResource      schema.GroupVersionResource
	hasTraefikMiddlewareCRD bool
}

const (
	traefikRouterMiddlewaresAnnotation = "traefik.ingress.kubernetes.io/router.middlewares"
)

func NewK8sIngressRegistry(cfg K8sIngressRegistryConfig) (*K8sIngressRegistry, error) {
	namespace := strings.TrimSpace(cfg.Namespace)
	if namespace == "" {
		namespace = "default"
	}

	restCfg, err := buildK8sIngressRESTConfig(cfg)
	if err != nil {
		return nil, err
	}

	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("init kubernetes client: %w", err)
	}

	dc, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("init kubernetes dynamic client: %w", err)
	}

	middlewareGVR, hasMiddlewareCRD := detectTraefikMiddlewareGVR(cs)

	pathType := parseIngressPathType(cfg.PathType)
	annotations := map[string]string{}
	for key, value := range cfg.Annotations {
		k := strings.TrimSpace(key)
		v := strings.TrimSpace(value)
		if k == "" || v == "" {
			continue
		}
		annotations[k] = v
	}

	return &K8sIngressRegistry{
		clientset:               cs,
		dynamicClient:           dc,
		namespace:               namespace,
		ingressClassName:        strings.TrimSpace(cfg.IngressClassName),
		host:                    strings.TrimSpace(cfg.Host),
		pathType:                pathType,
		defaultAnnotations:      annotations,
		middlewareResource:      middlewareGVR,
		hasTraefikMiddlewareCRD: hasMiddlewareCRD,
	}, nil
}

func (r *K8sIngressRegistry) UpsertRoute(ctx context.Context, route Registration) error {
	cleaned, err := sanitizeRegistration(route)
	if err != nil {
		return err
	}

	svcName, svcNS, err := parseServiceRef(cleaned.Backend.Host, r.namespace)
	if err != nil {
		return err
	}

	middlewareRefs, err := r.reconcileProfileMiddlewares(ctx, svcNS, cleaned)
	if err != nil {
		return err
	}

	name := ingressNameFromRouteKey(cleaned.RouteKey)
	pathType := r.pathType
	annotations := mergeIngressAnnotations(r.defaultAnnotations, cleaned.Metadata)
	if len(middlewareRefs) > 0 {
		annotations[traefikRouterMiddlewaresAnnotation] = strings.Join(middlewareRefs, ",")
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   svcNS,
			Labels:      map[string]string{"gobrave-route-key": cleaned.RouteKey},
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: r.host,
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     cleaned.PathPrefix,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: svcName,
									Port: networkingv1.ServiceBackendPort{Number: int32(cleaned.Backend.Port)},
								},
							},
						}},
					},
				},
			}},
		},
	}
	if strings.TrimSpace(r.ingressClassName) != "" {
		ing.Spec.IngressClassName = stringPtr(r.ingressClassName)
	}

	ingresses := r.clientset.NetworkingV1().Ingresses(svcNS)
	existing, err := ingresses.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get ingress %s: %w", name, err)
		}
		if _, err := ingresses.Create(ctx, ing, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create ingress %s: %w", name, err)
		}
		return nil
	}

	existing.Labels = ing.Labels
	existing.Annotations = ing.Annotations
	existing.Spec = ing.Spec
	if _, err := ingresses.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update ingress %s: %w", name, err)
	}

	return nil
}

func (r *K8sIngressRegistry) DeleteRoute(ctx context.Context, routeKey string) error {
	routeKey = strings.TrimSpace(routeKey)
	if routeKey == "" {
		return fmt.Errorf("route key is required")
	}

	ingresses := r.clientset.NetworkingV1().Ingresses(metav1.NamespaceAll)
	list, err := ingresses.List(ctx, metav1.ListOptions{LabelSelector: "gobrave-route-key=" + routeKey})
	if err != nil {
		return fmt.Errorf("list ingress by route key %s: %w", routeKey, err)
	}

	if len(list.Items) == 0 {
		name := ingressNameFromRouteKey(routeKey)
		if err := r.clientset.NetworkingV1().Ingresses(r.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete ingress %s/%s: %w", r.namespace, name, err)
		}
	}

	for _, item := range list.Items {
		if err := r.clientset.NetworkingV1().Ingresses(item.Namespace).Delete(ctx, item.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete ingress %s/%s: %w", item.Namespace, item.Name, err)
		}
		if err := r.deleteProfileMiddlewares(ctx, item.Namespace, routeKey); err != nil {
			return err
		}
	}

	if len(list.Items) == 0 {
		if err := r.deleteProfileMiddlewares(ctx, r.namespace, routeKey); err != nil {
			return err
		}
	}

	return nil
}

func (r *K8sIngressRegistry) reconcileProfileMiddlewares(ctx context.Context, namespace string, route Registration) ([]string, error) {
	middlewareNames, middlewareDefs := buildProfileMiddlewares(route)
	if len(middlewareNames) == 0 {
		return nil, nil
	}

	if !r.hasTraefikMiddlewareCRD || r.dynamicClient == nil {
		return nil, fmt.Errorf("traefik middleware CRD is required for profile %q but was not found in cluster", resolveTraefikProfile(route))
	}

	refs := make([]string, 0, len(middlewareNames))
	resource := r.dynamicClient.Resource(r.middlewareResource).Namespace(namespace)

	for _, middlewareName := range middlewareNames {
		spec, ok := middlewareDefs[middlewareName]
		if !ok {
			continue
		}

		obj := &metav1unstructured.Unstructured{Object: map[string]any{
			"apiVersion": r.middlewareResource.Group + "/" + r.middlewareResource.Version,
			"kind":       "Middleware",
			"metadata": map[string]any{
				"name":      middlewareName,
				"namespace": namespace,
				"labels": map[string]any{
					"gobrave-route-key": route.RouteKey,
				},
			},
			"spec": traefikMiddlewareSpecToUnstructured(spec),
		}}

		existing, err := resource.Get(ctx, middlewareName, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("get middleware %s/%s: %w", namespace, middlewareName, err)
			}
			if _, err := resource.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
				return nil, fmt.Errorf("create middleware %s/%s: %w", namespace, middlewareName, err)
			}
		} else {
			existing.SetLabels(map[string]string{"gobrave-route-key": route.RouteKey})
			if err := metav1unstructured.SetNestedField(existing.Object, obj.Object["spec"], "spec"); err != nil {
				return nil, fmt.Errorf("set middleware spec %s/%s: %w", namespace, middlewareName, err)
			}
			if _, err := resource.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
				return nil, fmt.Errorf("update middleware %s/%s: %w", namespace, middlewareName, err)
			}
		}

		refs = append(refs, namespace+"-"+middlewareName+"@kubernetescrd")
	}

	return refs, nil
}

func (r *K8sIngressRegistry) deleteProfileMiddlewares(ctx context.Context, namespace string, routeKey string) error {
	if !r.hasTraefikMiddlewareCRD || r.dynamicClient == nil {
		return nil
	}

	resource := r.dynamicClient.Resource(r.middlewareResource).Namespace(namespace)
	list, err := resource.List(ctx, metav1.ListOptions{LabelSelector: "gobrave-route-key=" + routeKey})
	if err != nil {
		return fmt.Errorf("list middleware by route key %s in %s: %w", routeKey, namespace, err)
	}
	for _, item := range list.Items {
		if err := resource.Delete(ctx, item.GetName(), metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete middleware %s/%s: %w", namespace, item.GetName(), err)
		}
	}

	return nil
}

func detectTraefikMiddlewareGVR(clientset kubernetes.Interface) (schema.GroupVersionResource, bool) {
	if clientset == nil {
		return schema.GroupVersionResource{}, false
	}

	candidates := []schema.GroupVersionResource{
		{Group: "traefik.io", Version: "v1alpha1", Resource: "middlewares"},
		{Group: "traefik.containo.us", Version: "v1alpha1", Resource: "middlewares"},
	}

	for _, gvr := range candidates {
		if _, err := clientset.Discovery().ServerResourcesForGroupVersion(gvr.Group + "/" + gvr.Version); err == nil {
			return gvr, true
		}
	}

	return schema.GroupVersionResource{}, false
}

func traefikMiddlewareSpecToUnstructured(spec traefikMiddlewareSpec) map[string]any {
	out := map[string]any{}
	if spec.StripPrefix != nil {
		prefixes := make([]any, 0, len(spec.StripPrefix.Prefixes))
		for _, prefix := range spec.StripPrefix.Prefixes {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" {
				continue
			}
			prefixes = append(prefixes, prefix)
		}
		out["stripPrefix"] = map[string]any{
			"prefixes":   prefixes,
			"forceSlash": spec.StripPrefix.ForceSlash,
		}
	}
	if spec.Headers != nil && len(spec.Headers.CustomRequestHeaders) > 0 {
		headers := map[string]any{}
		for key, value := range spec.Headers.CustomRequestHeaders {
			k := strings.TrimSpace(key)
			v := strings.TrimSpace(value)
			if k == "" || v == "" {
				continue
			}
			headers[k] = v
		}
		if len(headers) > 0 {
			out["headers"] = map[string]any{"customRequestHeaders": headers}
		}
	}
	return out
}

func buildK8sIngressRESTConfig(cfg K8sIngressRegistryConfig) (*rest.Config, error) {
	if cfg.InCluster {
		restCfg, err := rest.InClusterConfig()
		if err == nil {
			return restCfg, nil
		}
		return nil, fmt.Errorf("init in-cluster kubernetes config: %w", err)
	}

	kubeconfig := strings.TrimSpace(cfg.Kubeconfig)
	if kubeconfig != "" {
		restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %s: %w", kubeconfig, err)
		}
		return restCfg, nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	restCfg, err := clientCfg.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load default kubeconfig: %w", err)
	}
	return restCfg, nil
}

func parseServiceRef(rawHost string, fallbackNamespace string) (string, string, error) {
	host := strings.TrimSpace(strings.ToLower(rawHost))
	if host == "" {
		return "", "", fmt.Errorf("backend host is required")
	}
	if ip := net.ParseIP(host); ip != nil {
		return "", "", fmt.Errorf("k8s ingress backend requires service host, got ip: %s", rawHost)
	}

	parts := strings.Split(host, ".")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}

	svcName := parts[0]
	if svcName == "" {
		return "", "", fmt.Errorf("invalid service host: %s", rawHost)
	}

	namespace := strings.TrimSpace(fallbackNamespace)
	if len(parts) >= 2 && parts[1] != "" {
		namespace = parts[1]
	}
	if namespace == "" {
		namespace = "default"
	}

	return svcName, namespace, nil
}

func parseIngressPathType(raw string) networkingv1.PathType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "exact":
		return networkingv1.PathTypeExact
	case "implementationspecific", "implementation-specific":
		return networkingv1.PathTypeImplementationSpecific
	default:
		return networkingv1.PathTypePrefix
	}
}

func mergeIngressAnnotations(base map[string]string, metadata map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range base {
		k := strings.TrimSpace(key)
		v := strings.TrimSpace(value)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}

	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if value == "" {
			continue
		}
		out["gobrave.io/"+strings.ToLower(strings.TrimSpace(key))] = value
	}
	return out
}

func ingressNameFromRouteKey(routeKey string) string {
	cleaned := sanitizeK8sName(routeKey)
	if cleaned == "" {
		cleaned = "route"
	}
	name := "gobrave-" + cleaned
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

func sanitizeK8sName(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}

	builder := strings.Builder{}
	builder.Grow(len(raw))
	lastDash := false
	for _, r := range raw {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}

	return strings.Trim(builder.String(), "-")
}

func stringPtr(v string) *string {
	return &v
}
