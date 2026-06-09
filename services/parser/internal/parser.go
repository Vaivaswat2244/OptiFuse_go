// Package parser implements the parser service — takes raw serverless.yml
// bytes and emits a Graph proto. No AWS credentials required.
//
// Direct translation of simulation/core/builder.py ApplicationBuilder.create_from_yaml_content()
package parser

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// ServerlessYAML represents the relevant fields of a serverless.yml file.
// We only unmarshal what we need; unknown fields are ignored.
type ServerlessYAML struct {
	Service   string                  `yaml:"service"`
	Provider  ProviderSpec            `yaml:"provider"`
	Functions map[string]FunctionSpec `yaml:"functions"`
	Custom    CustomSpec              `yaml:"custom"`
}

type ProviderSpec struct {
	Name       string `yaml:"name"`
	Runtime    string `yaml:"runtime"`
	Region     string `yaml:"region"`
	MemorySize int    `yaml:"memorySize"`
	Timeout    int    `yaml:"timeout"`
}

type FunctionSpec struct {
	Handler     string            `yaml:"handler"`
	Runtime     string            `yaml:"runtime"`
	MemorySize  int               `yaml:"memorySize"`
	Timeout     int               `yaml:"timeout"`
	Environment map[string]string `yaml:"environment"`
	Events      []interface{}     `yaml:"events"` // kept raw; not used in optimization
}

// CustomSpec contains the optifuse-specific configuration block.
// Python: custom_spec.get('optifuse', {})
type CustomSpec struct {
	OptiFuse OptiFuseConfig `yaml:"optifuse"`
}

type OptiFuseConfig struct {
	// Topology defines the call graph.
	// Python: topology: { upload: { children: { resize: 5242880 } } }
	Topology     map[string]TopologyNode `yaml:"topology"`
	CriticalPath []string                `yaml:"criticalPath"`
	Constraints  ConstraintSpec          `yaml:"constraints"`
}

type TopologyNode struct {
	// Children maps child function ID → bytes transferred on that edge.
	// Python: details['children'] = {'resize': 5242880}
	Children map[string]int64 `yaml:"children"`
}

type ConstraintSpec struct {
	MaxMemoryMB  int `yaml:"maxMemoryMB"`
	MaxLatencyMS int `yaml:"maxLatencyMS"`
	NetworkHopMS int `yaml:"networkHopMS"`
}

// ParsedFunction is an intermediate representation before proto conversion.
// It mirrors LambdaFunction from structures.py but is independent of the
// domain package (which lives in the optimizer service).
type ParsedFunction struct {
	ID          string
	Name        string
	MemoryMB    int
	TimeoutSec  int
	Handler     string
	Runtime     string
	Environment map[string]string
	// DataOutBytes: child function ID → bytes transferred
	DataOutBytes map[string]int64
}

// ParsedGraph is the output of the parser — ready to be converted to the Graph proto.
type ParsedGraph struct {
	Name         string
	Functions    []*ParsedFunction
	CriticalPath []string
	MaxMemoryMB  int
	MaxLatencyMS int
	NetworkHopMS int
	Warnings     []string
}

// Parse takes raw serverless.yml bytes and a repo name, and returns a ParsedGraph.
// Python: ApplicationBuilder.create_from_yaml_content(repo_name, yaml_content)
func Parse(repoName string, yamlContent []byte) (*ParsedGraph, error) {
	var spec ServerlessYAML
	if err := yaml.Unmarshal(yamlContent, &spec); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	if len(spec.Functions) == 0 {
		return nil, fmt.Errorf("no 'functions' block found in serverless.yml")
	}

	var warnings []string

	// ── Provider defaults ─────────────────────────────────────────────────────
	// Python: memory = props.get('memorySize', provider_spec.get('memorySize', 512))
	defaultMemory := spec.Provider.MemorySize
	if defaultMemory == 0 {
		defaultMemory = 512
	}
	defaultTimeout := spec.Provider.Timeout
	if defaultTimeout == 0 {
		defaultTimeout = 30
	}
	defaultRuntime := spec.Provider.Runtime

	// ── First pass: create function nodes ─────────────────────────────────────
	// Python: for func_id, props in functions_spec.items():
	funcsMap := make(map[string]*ParsedFunction, len(spec.Functions))
	for id, props := range spec.Functions {
		mem := props.MemorySize
		if mem == 0 {
			mem = defaultMemory
		}
		timeout := props.Timeout
		if timeout == 0 {
			timeout = defaultTimeout
		}
		rt := props.Runtime
		if rt == "" {
			rt = defaultRuntime
		}

		funcsMap[id] = &ParsedFunction{
			ID:           id,
			Name:         id,
			MemoryMB:     mem,
			TimeoutSec:   timeout,
			Handler:      props.Handler,
			Runtime:      rt,
			Environment:  props.Environment,
			DataOutBytes: make(map[string]int64),
		}
	}

	// ── Second pass: build edges from topology block ───────────────────────────
	// Python: topology = optifuse_config.get('topology', {})
	//         for parent_id, details in topology.items():
	//             for child_id, data_bytes in details['children'].items():
	//                 functions[parent_id].add_child(functions[child_id], data_bytes)
	if len(spec.Custom.OptiFuse.Topology) == 0 {
		warnings = append(warnings, "no 'custom.optifuse.topology' block found — graph has no edges; optimization will be trivial")
	}

	for parentID, node := range spec.Custom.OptiFuse.Topology {
		parent, ok := funcsMap[parentID]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("topology references unknown function '%s' — skipping", parentID))
			continue
		}
		for childID, dataBytes := range node.Children {
			if _, ok := funcsMap[childID]; !ok {
				warnings = append(warnings, fmt.Sprintf("topology references unknown child '%s' for parent '%s' — skipping", childID, parentID))
				continue
			}
			parent.DataOutBytes[childID] = dataBytes
		}
	}

	// ── Constraints ───────────────────────────────────────────────────────────
	maxMem := spec.Custom.OptiFuse.Constraints.MaxMemoryMB
	if maxMem == 0 {
		maxMem = 1024
	}
	maxLat := spec.Custom.OptiFuse.Constraints.MaxLatencyMS
	if maxLat == 0 {
		maxLat = 30000
	}
	netHop := spec.Custom.OptiFuse.Constraints.NetworkHopMS
	if netHop == 0 {
		netHop = 20
	}

	if len(spec.Custom.OptiFuse.CriticalPath) == 0 {
		warnings = append(warnings, "no 'custom.optifuse.criticalPath' specified — latency constraints will not be enforced")
	}

	// Convert map to slice (deterministic order: critical path first, then rest).
	cpSet := make(map[string]bool, len(spec.Custom.OptiFuse.CriticalPath))
	for _, id := range spec.Custom.OptiFuse.CriticalPath {
		cpSet[id] = true
	}
	funcs := make([]*ParsedFunction, 0, len(funcsMap))
	for _, id := range spec.Custom.OptiFuse.CriticalPath {
		if f, ok := funcsMap[id]; ok {
			funcs = append(funcs, f)
		}
	}
	for id, f := range funcsMap {
		if !cpSet[id] {
			funcs = append(funcs, f)
		}
	}

	graph := &ParsedGraph{
		Name:         repoName,
		Functions:    funcs,
		CriticalPath: spec.Custom.OptiFuse.CriticalPath,
		MaxMemoryMB:  maxMem,
		MaxLatencyMS: maxLat,
		NetworkHopMS: netHop,
		Warnings:     warnings,
	}

	return graph, nil
}
