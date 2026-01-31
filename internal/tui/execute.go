package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/stukennedy/tooey/node"
)

// PhaseProgress tracks the progress of a single phase
type PhaseProgress struct {
	ID        string
	Status    string // pending, running, passed, failed
	StartTime time.Time
	EndTime   time.Time
	Output    []string
	Expanded  bool
	Files     []FileChange
	Gates     []GateProgress
}

// FileChange represents a file operation
type FileChange struct {
	Path   string
	Action string // create, modify, delete
}

// GateProgress tracks a verification gate
type GateProgress struct {
	Name   string
	Status string // pending, running, passed, failed
	Output string
}

// CheckpointState holds checkpoint UI state
type CheckpointState struct {
	Active      bool
	Type        string   // human-verify, decision, human-action
	Message     string
	Options     []string
	SelectedOpt int
}

// ExecuteState holds the full execution state
type ExecuteState struct {
	ProjectName string
	Task        string
	Phases      []PhaseProgress
	CurrentIdx  int
	StartTime   time.Time
	RunDir      string
	Paused      bool
	Error       error
	Checkpoint  *CheckpointState
}

// NewExecuteState creates initial execution state
func NewExecuteState(projectName, task string) *ExecuteState {
	return &ExecuteState{
		ProjectName: projectName,
		Task:        task,
		StartTime:   time.Now(),
		Phases: []PhaseProgress{
			{ID: "context", Status: "pending"},
			{ID: "plan", Status: "pending"},
			{ID: "implement", Status: "pending"},
			{ID: "verify", Status: "pending"},
			{ID: "deliver", Status: "pending"},
		},
	}
}

// RenderPhaseBar renders the phase indicator row
func RenderPhaseBar(phases []PhaseProgress, width int) node.Node {
	var items []node.Node
	for i, p := range phases {
		var icon string
		var fg node.Color
		switch p.Status {
		case "pending":
			icon = "○"
			fg = colMuted
		case "running":
			icon = "●"
			fg = colWarning
		case "passed":
			icon = "✓"
			fg = colPrimary
		case "failed":
			icon = "✗"
			fg = colError
		}
		label := fmt.Sprintf(" %s %s ", icon, p.ID)
		items = append(items, node.TextStyled(label, fg, 0, node.Bold))
		if i < len(phases)-1 {
			items = append(items, node.TextStyled("→", colDim, 0, 0))
		}
	}
	return node.Row(items...)
}

// RenderPhaseDetail renders expanded phase output
func RenderPhaseDetail(phase *PhaseProgress, width int, selected bool, spinnerFrame string) []node.Node {
	var nodes []node.Node

	// Phase header
	var icon string
	var fg node.Color
	var style node.StyleFlags
	switch phase.Status {
	case "pending":
		icon = "○"
		fg = colMuted
	case "running":
		icon = spinnerFrame
		fg = colWarning
		style = node.Bold
	case "passed":
		icon = "✓"
		fg = colPrimary
		style = node.Bold
	case "failed":
		icon = "✗"
		fg = colError
		style = node.Bold
	}

	selector := "  "
	if selected {
		selector = "› "
	}

	expandIcon := "▶"
	if phase.Expanded {
		expandIcon = "▼"
	}

	duration := ""
	if !phase.StartTime.IsZero() {
		end := phase.EndTime
		if end.IsZero() {
			end = time.Now()
		}
		d := end.Sub(phase.StartTime)
		if d > time.Second {
			duration = fmt.Sprintf(" (%s)", d.Round(time.Second))
		}
	}

	header := fmt.Sprintf("%s%s %s %s%s", selector, expandIcon, icon, phase.ID, duration)
	selectorFG := colSecondary
	if !selected {
		selectorFG = fg
	}
	nodes = append(nodes, node.TextStyled(header, selectorFG, 0, style))

	// Expanded content
	if phase.Expanded && len(phase.Output) > 0 {
		maxLines := 10
		startIdx := 0
		if len(phase.Output) > maxLines {
			startIdx = len(phase.Output) - maxLines
			nodes = append(nodes, node.TextStyled(
				fmt.Sprintf("      ... (%d more lines)", startIdx), colMuted, 0, node.Dim))
		}
		for _, line := range phase.Output[startIdx:] {
			nodes = append(nodes, node.TextStyled("      "+line, colText, 0, 0))
		}

		// File changes
		if len(phase.Files) > 0 {
			nodes = append(nodes, node.TextStyled("      Files:", colMuted, 0, 0))
			for _, f := range phase.Files {
				var fc node.Color
				var prefix string
				switch f.Action {
				case "create":
					fc = colFileAdd
					prefix = "+"
				case "modify":
					fc = colFileMod
					prefix = "~"
				case "delete":
					fc = colFileDel
					prefix = "-"
				default:
					fc = colText
					prefix = "•"
				}
				nodes = append(nodes, node.TextStyled(
					fmt.Sprintf("        %s %s", prefix, f.Path), fc, 0, 0))
			}
		}

		// Gates
		if len(phase.Gates) > 0 {
			nodes = append(nodes, node.TextStyled("      Gates:", colMuted, 0, 0))
			for _, g := range phase.Gates {
				gIcon := "○"
				gFG := colMuted
				switch g.Status {
				case "running":
					gIcon = "●"
					gFG = colWarning
				case "passed":
					gIcon = "✓"
					gFG = colPrimary
				case "failed":
					gIcon = "✗"
					gFG = colError
				}
				nodes = append(nodes, node.TextStyled(
					fmt.Sprintf("        %s %s", gIcon, g.Name), gFG, 0, 0))
			}
		}
	}

	return nodes
}

// RenderCheckpoint renders the checkpoint UI
func RenderCheckpoint(cp *CheckpointState) []node.Node {
	if cp == nil || !cp.Active {
		return nil
	}

	var nodes []node.Node
	var icon string
	switch cp.Type {
	case "human-verify":
		icon = "🔍"
	case "decision":
		icon = "🤔"
	case "human-action":
		icon = "👤"
	default:
		icon = "⏸"
	}

	nodes = append(nodes,
		node.Text(""),
		node.TextStyled(fmt.Sprintf("%s CHECKPOINT [%s]", icon, cp.Type), colWarning, 0, node.Bold),
		node.TextStyled("  "+cp.Message, colText, 0, 0),
	)

	if len(cp.Options) > 0 {
		for i, opt := range cp.Options {
			prefix := "  "
			fg := colText
			if i == cp.SelectedOpt {
				prefix = "› "
				fg = colSecondary
			}
			nodes = append(nodes, node.TextStyled(
				fmt.Sprintf("  %s%d) %s", prefix, i+1, opt), fg, 0, 0))
		}
	}

	return nodes
}

// RenderExecuteView renders the full execute mode as a node tree
func RenderExecuteView(state *ExecuteState, width, selectedIdx int, spinnerFrame string) node.Node {
	// Header
	headerText := fmt.Sprintf(" %s  Building: %s", Logo(), state.ProjectName)
	if state.ProjectName == "" || state.ProjectName == "." {
		headerText = fmt.Sprintf(" %s  Building in current directory", Logo())
	}
	pad := width - len([]rune(headerText))
	if pad < 0 {
		pad = 0
	}
	header := node.TextStyled(headerText+strings.Repeat(" ", pad), colPrimary, colDarkBg, node.Bold)

	// Task summary
	taskPreview := state.Task
	if len(taskPreview) > 80 {
		taskPreview = taskPreview[:77] + "..."
	}
	taskLine := node.TextStyled(" Task: "+taskPreview, colMuted, 0, 0)

	// Phase bar
	phaseBar := RenderPhaseBar(state.Phases, width)

	// Phase details (scrollable)
	var detailNodes []node.Node
	for i := range state.Phases {
		nodes := RenderPhaseDetail(&state.Phases[i], width-4, i == selectedIdx, spinnerFrame)
		detailNodes = append(detailNodes, nodes...)
	}

	// Error
	if state.Error != nil {
		detailNodes = append(detailNodes,
			node.Text(""),
			node.TextStyled(fmt.Sprintf("Error: %v", state.Error), colError, 0, node.Bold),
		)
	}

	// Checkpoint
	if cpNodes := RenderCheckpoint(state.Checkpoint); cpNodes != nil {
		detailNodes = append(detailNodes, cpNodes...)
	}

	details := node.Column(detailNodes...).WithFlex(1).WithScrollToBottom()

	// Footer
	var helpText string
	if state.Checkpoint != nil && state.Checkpoint.Active {
		if len(state.Checkpoint.Options) > 0 {
			helpText = " ⏸ CHECKPOINT • ↑↓: select • enter: confirm • q: quit"
		} else {
			helpText = " ⏸ CHECKPOINT • y: approve • n: reject • q: quit"
		}
	} else if state.Paused {
		helpText = " ⏸ PAUSED • r: resume • q: quit • ↑↓: select • enter: expand"
	} else {
		helpText = " p: pause • q: quit • ↑↓: select • enter: expand"
	}
	helpPad := width - len([]rune(helpText))
	if helpPad < 0 {
		helpPad = 0
	}
	footer := node.TextStyled(helpText+strings.Repeat(" ", helpPad), colMuted, colDarkBg, 0)

	return node.Column(
		header,
		node.TextStyled(strings.Repeat("─", width), colDim, 0, 0),
		taskLine,
		node.Text(""),
		phaseBar,
		node.Text(""),
		details,
		node.TextStyled(strings.Repeat("─", width), colDim, 0, 0),
		footer,
	)
}
