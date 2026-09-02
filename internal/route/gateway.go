package route

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Gateway keeps route-to-backend mapping for the in-process gateway.
// It is database-backed so mappings survive process restarts.
type Gateway struct {
	mu          sync.RWMutex
	db          *gorm.DB
	routesByKey map[string]Registration
	keyByPrefix map[string]string
	prefixes    []string
}

func NewGateway(db *gorm.DB) (*Gateway, error) {
	if db == nil {
		return nil, fmt.Errorf("db is required")
	}

	r := &Gateway{
		db:          db,
		routesByKey: make(map[string]Registration),
		keyByPrefix: make(map[string]string),
		prefixes:    make([]string, 0),
	}

	if err := r.db.AutoMigrate(&types.GatewayRoute{}); err != nil {
		return nil, err
	}

	if err := r.loadFromDB(); err != nil {
		return nil, err
	}

	return r, nil
}

func (r *Gateway) ResolveByPath(path string) (Registration, string, bool) {
	normalizedPath := normalizePath(path)

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, prefix := range r.prefixes {
		if pathHasPrefix(normalizedPath, prefix) {
			key := r.keyByPrefix[prefix]
			reg, ok := r.routesByKey[key]
			if ok {
				return reg, prefix, true
			}
		}
	}

	return Registration{}, "", false
}

func (r *Gateway) UpsertRoute(ctx context.Context, route Registration) error {
	cleaned, err := sanitizeRegistration(route)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existingKey, exists := r.keyByPrefix[cleaned.PathPrefix]; exists && existingKey != cleaned.RouteKey {
		return fmt.Errorf("path prefix already registered: %s (owned by %s)", cleaned.PathPrefix, existingKey)
	}

	if old, exists := r.routesByKey[cleaned.RouteKey]; exists && old.PathPrefix != cleaned.PathPrefix {
		delete(r.keyByPrefix, old.PathPrefix)
	}

	r.routesByKey[cleaned.RouteKey] = cleaned
	r.keyByPrefix[cleaned.PathPrefix] = cleaned.RouteKey
	r.rebuildPrefixIndex()

	if err := r.upsertRouteLocked(ctx, cleaned); err != nil {
		// rollback in-memory mutation to keep cache consistent with DB.
		r.rebuildFromDBLocked(ctx)
		return err
	}

	logger.Infof(ctx, "[Gateway] upsert route key=%s prefix=%s backend=%s:%d", cleaned.RouteKey, cleaned.PathPrefix, cleaned.Backend.Host, cleaned.Backend.Port)
	return nil
}

func (r *Gateway) DeleteRoute(ctx context.Context, routeKey string) error {
	routeKey = strings.TrimSpace(routeKey)
	if routeKey == "" {
		return fmt.Errorf("route key is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if old, exists := r.routesByKey[routeKey]; exists {
		delete(r.routesByKey, routeKey)
		delete(r.keyByPrefix, old.PathPrefix)
		r.rebuildPrefixIndex()
	}

	if err := r.deleteRouteLocked(ctx, routeKey); err != nil {
		r.rebuildFromDBLocked(ctx)
		return err
	}

	logger.Infof(ctx, "[Gateway] delete route key=%s", routeKey)
	return nil
}

func (r *Gateway) DeleteRouteByContainerInstanceID(ctx context.Context, containerInstanceID int64) error {
	if containerInstanceID <= 0 {
		return fmt.Errorf("container instance id is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for key, reg := range r.routesByKey {
		if reg.ContainerInstanceID == containerInstanceID {
			delete(r.routesByKey, key)
			delete(r.keyByPrefix, reg.PathPrefix)
		}
	}
	r.rebuildPrefixIndex()

	if err := r.deleteRouteLockedByContainerInstanceID(ctx, containerInstanceID); err != nil {
		r.rebuildFromDBLocked(ctx)
		return err
	}

	logger.Infof(ctx, "[Gateway] delete route container_instance_id=%d", containerInstanceID)
	return nil
}

func (r *Gateway) loadFromDB() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.rebuildFromDBLocked(context.Background())
}

func (r *Gateway) rebuildFromDBLocked(ctx context.Context) error {
	var rows []types.GatewayRoute
	if err := r.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return err
	}

	r.routesByKey = make(map[string]Registration, len(rows))
	r.keyByPrefix = make(map[string]string, len(rows))
	r.prefixes = r.prefixes[:0]

	for _, row := range rows {
		reg := Registration{
			RouteKey:            row.RouteKey,
			ContainerInstanceID: row.ContainerInstanceID,
			PathPrefix:          row.PathPrefix,
			IsTrimPrefix:        row.IsTrimPrefix,

			Backend: Backend{
				Host: row.BackendHost,
				Port: row.BackendPort,
			},
		}
		if len(row.Metadata) > 0 {
			_ = json.Unmarshal(row.Metadata, &reg.Metadata)
		}

		cleaned, err := sanitizeRegistration(reg)
		if err != nil {
			logger.Warnf(ctx, "[Gateway] skip invalid db route route_key=%s err=%v", row.RouteKey, err)
			continue
		}

		r.routesByKey[cleaned.RouteKey] = cleaned
		r.keyByPrefix[cleaned.PathPrefix] = cleaned.RouteKey
	}
	r.rebuildPrefixIndex()

	return nil
}

func (r *Gateway) upsertRouteLocked(ctx context.Context, route Registration) error {
	if owner, exists := r.keyByPrefix[route.PathPrefix]; exists && owner != route.RouteKey {
		return fmt.Errorf("path prefix already registered: %s (owned by %s)", route.PathPrefix, owner)
	}

	metadata := datatypes.JSON([]byte("{}"))
	if len(route.Metadata) > 0 {
		data, err := json.Marshal(route.Metadata)
		if err != nil {
			return err
		}
		metadata = datatypes.JSON(data)
	}

	entity := &types.GatewayRoute{
		RouteKey:            route.RouteKey,
		ContainerInstanceID: route.ContainerInstanceID,
		PathPrefix:          route.PathPrefix,
		BackendHost:         route.Backend.Host,
		BackendPort:         route.Backend.Port,
		IsTrimPrefix:        route.IsTrimPrefix,
		Metadata:            metadata,
	}

	// 先查询是否存在，存在则更新，否则插入。
	var count int64
	if err := r.db.WithContext(ctx).Model(&types.GatewayRoute{}).
		Where("route_key = ?", route.RouteKey).
		Count(&count).Error; err != nil {
		return err
	}

	if count > 0 {
		if err := r.db.WithContext(ctx).Model(&types.GatewayRoute{}).
			Where("route_key = ?", route.RouteKey).
			Updates(map[string]interface{}{
				"container_instance_id": entity.ContainerInstanceID,
				"path_prefix":           entity.PathPrefix,
				"backend_host":          entity.BackendHost,
				"backend_port":          entity.BackendPort,
				"is_trim_prefix":        entity.IsTrimPrefix,
				"metadata":              entity.Metadata,
			}).Error; err != nil {
			return err
		}
	} else {
		if err := r.db.Debug().WithContext(ctx).Create(entity).Error; err != nil {
			return err
		}
	}

	return nil
}

func (r *Gateway) deleteRouteLocked(ctx context.Context, routeKey string) error {
	return r.db.WithContext(ctx).Where("route_key = ?", routeKey).Delete(&types.GatewayRoute{}).Error
}

func (r *Gateway) deleteRouteLockedByContainerInstanceID(ctx context.Context, containerInstanceID int64) error {
	return r.db.WithContext(ctx).Where("container_instance_id = ?", containerInstanceID).Delete(&types.GatewayRoute{}).Error
}

func (r *Gateway) rebuildPrefixIndex() {
	r.prefixes = r.prefixes[:0]
	for prefix := range r.keyByPrefix {
		r.prefixes = append(r.prefixes, prefix)
	}

	sort.Slice(r.prefixes, func(i, j int) bool {
		if len(r.prefixes[i]) == len(r.prefixes[j]) {
			return r.prefixes[i] < r.prefixes[j]
		}
		return len(r.prefixes[i]) > len(r.prefixes[j])
	})
}

func sanitizeRegistration(route Registration) (Registration, error) {
	route.RouteKey = strings.TrimSpace(route.RouteKey)
	route.PathPrefix = normalizePath(strings.TrimSpace(route.PathPrefix))
	route.Backend.Host = strings.TrimSpace(route.Backend.Host)

	if err := validateRegistration(route); err != nil {
		return Registration{}, err
	}

	return route, nil
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path != "/" {
		path = strings.TrimRight(path, "/")
		if path == "" {
			path = "/"
		}
	}
	return path
}

func pathHasPrefix(path string, prefix string) bool {
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}
