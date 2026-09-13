package repository

import (
	"os"
	"path/filepath"
	"strings"
)

func (r *Repository) graphLock() (func(), error) {
	return Lock(filepath.Join(r.StorePath, "locks", "graph.lock"))
}

// dependencyGraph returns the latest depends_on edges of every valid task.
func (r *Repository) dependencyGraph() (map[string][]string, error) {
	graph := map[string][]string{}
	tasksDirectory := filepath.Join(r.StorePath, "tasks")
	if _, err := SafePath(tasksDirectory); err != nil {
		return nil, blocked("unsafe task store path: %v", err)
	}
	entries, err := os.ReadDir(tasksDirectory)
	if os.IsNotExist(err) {
		return graph, nil
	}
	if err != nil {
		return nil, blocked("cannot enumerate tasks: %v", err)
	}
	for _, entry := range entries {
		if !isUUID(entry.Name()) {
			continue
		}
		chain, err := r.readChain(filepath.Join(tasksDirectory, entry.Name()))
		if err != nil {
			return nil, err
		}
		if len(chain) == 0 {
			continue
		}
		task, err := taskFromState(chain[len(chain)-1])
		if err != nil {
			continue
		}
		graph[task.ID] = task.DependsOn
	}
	return graph, nil
}

// validateGraph rejects a self-reference or a cycle introduced by the candidate
// edges. The caller must hold the repository graph lock.
func (r *Repository) validateGraph(candidate string, dependencies []string) error {
	graph, err := r.dependencyGraph()
	if err != nil {
		return err
	}
	for _, dependency := range dependencies {
		if dependency == candidate {
			return invalid("task must not depend on itself")
		}
	}
	graph[candidate] = dependencies
	return detectCycle(graph, candidate)
}

func detectCycle(graph map[string][]string, start string) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	state := map[string]int{}
	var visit func(node string, path []string) error
	visit = func(node string, path []string) error {
		state[node] = gray
		for _, next := range graph[node] {
			switch state[next] {
			case gray:
				return conflict("dependency cycle detected: %s", strings.Join(append(path, next), " -> "))
			case white:
				if err := visit(next, append(path, next)); err != nil {
					return err
				}
			}
		}
		state[node] = black
		return nil
	}
	return visit(start, []string{start})
}
