package buildkit

import (
	"fmt"
	"strings"

	"github.com/moby/buildkit/client/llb"
	"github.com/railwayapp/railpack-go/core/plan"
)

type BuildGraph struct {
	Nodes     map[string]*Node
	BaseState *llb.State
}

type BuildGraphOutput struct {
	State    *llb.State
	PathList []string
	EnvVars  map[string]string
}

func NewBuildGraph(plan *plan.BuildPlan, baseState *llb.State) (*BuildGraph, error) {
	graph := &BuildGraph{
		Nodes:     make(map[string]*Node),
		BaseState: baseState,
	}

	// Create a node for each step
	for i := range plan.Steps {
		step := &plan.Steps[i]
		graph.Nodes[step.Name] = &Node{
			Step:           step,
			Parents:        make([]*Node, 0),
			Children:       make([]*Node, 0),
			Processed:      false,
			OutputEnvVars:  make(map[string]string),
			OutputPathList: make([]string, 0),
		}
	}

	// Add dependencies to each node
	for _, node := range graph.Nodes {
		for _, depName := range node.Step.DependsOn {
			if depNode, exists := graph.Nodes[depName]; exists {
				node.Parents = append(node.Parents, depNode)
				depNode.Children = append(depNode.Children, node)
			}
		}
	}

	return graph, nil
}

func (g *BuildGraph) GenerateLLB() (*BuildGraphOutput, error) {
	// Get processing order using topological sort
	order, err := g.getProcessingOrder()
	if err != nil {
		return nil, err
	}

	// Process all nodes in order
	for _, node := range order {
		if err := g.processNode(node); err != nil {
			return nil, err
		}
	}

	// Find all leaf nodes and get their states
	var leafStates []llb.State
	var leafStepNames []string

	outputPathList := make([]string, 0)
	outputEnvVars := make(map[string]string)

	for _, node := range g.Nodes {
		if len(node.Children) == 0 && node.State != nil {
			leafStates = append(leafStates, *node.State)
			leafStepNames = append(leafStepNames, node.Step.Name)

			// Add output path and env vars
			outputPathList = append(outputPathList, node.OutputPathList...)
			for k, v := range node.OutputEnvVars {
				outputEnvVars[k] = v
			}
		}

	}

	// If no leaf states, return base state
	if len(leafStates) == 0 {
		return &BuildGraphOutput{
			State:    g.BaseState,
			PathList: outputPathList,
			EnvVars:  outputEnvVars,
		}, nil
	}

	// If only one leaf state, return it
	if len(leafStates) == 1 {
		return &BuildGraphOutput{
			State:    &leafStates[0],
			PathList: outputPathList,
			EnvVars:  outputEnvVars,
		}, nil
	}

	// Merge all leaf states
	mergeName := fmt.Sprintf("merging steps: %s", strings.Join(leafStepNames, ", "))
	result := llb.Merge(leafStates, llb.WithCustomName(mergeName))

	return &BuildGraphOutput{
		State:    &result,
		PathList: outputPathList,
		EnvVars:  outputEnvVars,
	}, nil
}

func (g *BuildGraph) processNode(node *Node) error {
	// If already processed, we're done
	if node.Processed {
		return nil
	}

	// Check if all parents are processed
	for _, parent := range node.Parents {
		if !parent.Processed {
			// If this node is marked in-progress, we have a dependency violation
			if node.InProgress {
				return fmt.Errorf("Dependency violation: %s waiting for unprocessed parent %s",
					node.Step.Name, parent.Step.Name)
			}

			// Mark this node as in-progress and process the parent
			node.InProgress = true
			if err := g.processNode(parent); err != nil {
				node.InProgress = false
				return err
			}
			node.InProgress = false
		}
	}

	// Determine the state to build upon
	var currentState *llb.State
	currentEnvVars := make(map[string]string)
	currentPathList := make([]string, 0)

	if len(node.Parents) == 0 {
		currentState = g.BaseState
	} else if len(node.Parents) == 1 {
		// If only one parent, use its state directly
		currentState = node.Parents[0].State
		currentEnvVars = node.Parents[0].OutputEnvVars
		currentPathList = node.Parents[0].OutputPathList
	} else {
		// If multiple parents, merge their states
		parentStates := make([]llb.State, len(node.Parents))
		mergeStepNames := make([]string, len(node.Parents))

		for i, parent := range node.Parents {
			if parent.State == nil {
				return fmt.Errorf("Parent %s of %s has nil state",
					parent.Step.Name, node.Step.Name)
			}

			// Build up the current path and env vars
			currentPathList = append(currentPathList, parent.OutputPathList...)
			for k, v := range parent.OutputEnvVars {
				currentEnvVars[k] = v
			}

			parentStates[i] = *parent.State
			mergeStepNames[i] = parent.Step.Name
		}

		mergeName := fmt.Sprintf("merging steps: %s", strings.Join(mergeStepNames, ", "))
		merged := llb.Merge(parentStates, llb.WithCustomName(mergeName))
		currentState = &merged
	}

	node.InputPathList = currentPathList
	node.InputEnvVars = currentEnvVars

	// Convert this node's step to LLB
	stepState, err := node.convertStepToLLB(currentState)
	if err != nil {
		return err
	}

	node.State = stepState
	node.Processed = true

	return nil
}

// getProcessingOrder returns nodes in topological order
func (g *BuildGraph) getProcessingOrder() ([]*Node, error) {
	order := make([]*Node, 0, len(g.Nodes))
	visited := make(map[string]bool)
	temp := make(map[string]bool)

	var visit func(node *Node) error
	visit = func(node *Node) error {
		if temp[node.Step.Name] {
			return fmt.Errorf("cycle detected: %s", node.Step.Name)
		}
		if visited[node.Step.Name] {
			return nil
		}
		temp[node.Step.Name] = true

		for _, parent := range node.Parents {
			if err := visit(parent); err != nil {
				return err
			}
		}

		delete(temp, node.Step.Name)
		visited[node.Step.Name] = true
		order = append(order, node)
		return nil
	}

	// Start with leaf nodes (nodes with no children)
	for _, node := range g.Nodes {
		if len(node.Children) == 0 {
			if err := visit(node); err != nil {
				return nil, err
			}
		}
	}

	// Process any remaining nodes
	for _, node := range g.Nodes {
		if !visited[node.Step.Name] {
			if err := visit(node); err != nil {
				return nil, err
			}
		}
	}

	// Reverse the order since we want parents before children
	for i := 0; i < len(order)/2; i++ {
		j := len(order) - 1 - i
		order[i], order[j] = order[j], order[i]
	}

	return order, nil
}

func (g *BuildGraph) PrintGraph() {
	fmt.Println("\nBuild Graph Structure:")
	fmt.Println("=====================")

	for name, node := range g.Nodes {
		fmt.Printf("\nNode: %s\n", name)
		fmt.Printf("  Status:\n")
		fmt.Printf("    Processed: %v\n", node.Processed)
		fmt.Printf("    InProgress: %v\n", node.InProgress)

		fmt.Printf("  Parents (%d):\n", len(node.Parents))
		for _, parent := range node.Parents {
			fmt.Printf("    - %s\n", parent.Step.Name)
		}

		fmt.Printf("  Children (%d):\n", len(node.Children))
		for _, child := range node.Children {
			fmt.Printf("    - %s\n", child.Step.Name)
		}
	}
	fmt.Println("\n=====================")
}
