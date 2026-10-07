// Package plugin 依赖图解析. docs/plugin/manager.md §2.
package plugin

import (
	"errors"
	"fmt"
	"sort"
)

// resolveDependencies 返回稳定拓扑排序和检测到的错误.
// docs/plugin/manager.md §2:
//   - ID 唯一性 (Discover 已保证)
//   - 缺失依赖检测 (optional 缺失只 WARN → diagnostics, 不阻断)
//   - SemVer range 校验
//   - 循环依赖检测
//   - 稳定拓扑排序 (依赖在依赖项之前)
//
// 返回 order 是依赖在前, 被依赖在后的排列.
func (m *Manager) resolveDependencies() (order []string, errs []error) {
	// 构建邻接表: plugin -> 它依赖的 plugin IDs (只含已 installed 的)
	// 也记录缺失 dep 用于 diagnostic
	allIDs := make([]string, 0, len(m.entries))
	for id := range m.entries {
		allIDs = append(allIDs, id)
	}
	sort.Strings(allIDs)

	// depGraph: id -> 依赖的 id 列表 (已 installed 的 non-optional deps)
	depGraph := make(map[string][]string)
	// 排序后的 ID 集合
	installedIDs := make(map[string]bool, len(allIDs))
	for _, id := range allIDs {
		installedIDs[id] = true
	}
	for _, id := range allIDs {
		e := m.entries[id]
		var deps []string
		for _, dep := range e.Descriptor.Manifest.Dependencies {
			if !installedIDs[dep.ID] {
				if !dep.Optional {
					errs = append(errs, fmt.Errorf("%w: %s required by %s",
						ErrPluginDependencyMissing, dep.ID, id))
					m.fail(e, fmt.Errorf("%w: %s",
						ErrPluginDependencyMissing, dep.ID))
				}
				continue
			}
			// version range 校验
			depVersion := m.entries[dep.ID].Descriptor.Manifest.Version
			constraints, perr := parseVersionRange(dep.Version)
			if perr != nil {
				errs = append(errs, fmt.Errorf("%s: dependency %s range %q invalid: %v",
					id, dep.ID, dep.Version, perr))
				continue
			}
			if !versionInRange(depVersion, constraints) {
				errs = append(errs, fmt.Errorf("%s: dependency %s version %s does not satisfy %q",
					id, dep.ID, depVersion, dep.Version))
				continue
			}
			deps = append(deps, dep.ID)
		}
		depGraph[id] = deps
	}

	// 循环依赖检测 + 拓扑排序 (Kahn's algorithm, 稳定排序 tiebreak).
	// depGraph[id] = deps 是 id 依赖的人; 启动顺序应先启动 deps 再启动 id.
	// 拓扑序中, dep 在前 id 在后: edge dep -> id.
	// inDegree[id] = len(deps) 表示 id 等待多少个 dep 启动完毕.
	inDegree := make(map[string]int)
	for _, id := range allIDs {
		inDegree[id] = len(depGraph[id])
	}

	// 可用节点: inDegree == 0 且 state 不是 error.
	startNodes := make([]string, 0)
	for _, id := range allIDs {
		if inDegree[id] == 0 {
			startNodes = append(startNodes, id)
		}
	}
	sort.Strings(startNodes)

	// 稳定 Kahn: 每次取排序后的最小 ID
	queue := append([]string(nil), startNodes...)
	for len(queue) > 0 {
		// sort queue for stable processing (已 sorted, 但后续 insert 可能乱序)
		sort.Strings(queue)
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		// 找所有依赖 id 的节点, 减少它们的 inDegree
		for _, other := range allIDs {
			for _, dep := range depGraph[other] {
				if dep == id {
					inDegree[other]--
					if inDegree[other] == 0 {
						queue = append(queue, other)
					}
				}
			}
		}
	}

	// 如果 order 不含所有节点, 说明有循环
	if len(order) < len(allIDs) {
		// 找到不在 order 中的节点 → 循环依赖
		inOrder := make(map[string]bool)
		for _, id := range order {
			inOrder[id] = true
		}
		for _, id := range allIDs {
			if !inOrder[id] {
				errs = append(errs, fmt.Errorf("%w: %s", ErrPluginCircularDependency, id))
				m.fail(m.entries[id], ErrPluginCircularDependency)
			}
		}
	}
	return order, errs
}

// fail 在 mu 下更新 Entry 的 State 和 LastError. docs/plugin/manager.md §3.
func (m *Manager) fail(e *Entry, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.State = StateError
	e.LastError = errors.Join(e.LastError, err) // errors.Join 在 Go 1.20+ 可用
}

// effectiveEnabled 返回 enabled 的最终值. docs/plugin/manager.md §3.
// 显式 entries[].enabled 优先; 否则用 Manifest default_enabled.
func effectiveEnabled(e *Entry) bool {
	if e.Enabled != nil {
		return *e.Enabled
	}
	return e.Descriptor.Manifest.DefaultEnabled
}

// requireReadyDependencies 检查所有 non-optional dependencies 是否处于 ready.
// docs/plugin/manager.md §2: 启动某 Entry 前, non-optional deps 必须是 ready.
// ponytail: 当前无 ready (RPC 未启动), 所有 dep 都会 fail. RPC 实现后再补.
func (m *Manager) requireReadyDependencies(e *Entry) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, dep := range e.Descriptor.Manifest.Dependencies {
		if dep.Optional {
			continue
		}
		depEntry := m.entries[dep.ID]
		if depEntry == nil || depEntry.State != StateReady {
			return fmt.Errorf("plugin %s: required dependency %s not ready",
				e.Descriptor.Manifest.ID, dep.ID)
		}
	}
	return nil
}
