package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/stukennedy/tooey/component"
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

func badgeStyle(status string) component.BadgeStyle {
	switch status {
	case "passed":
		return component.BadgeSuccess
	case "failed":
		return component.BadgeError
	case "running":
		return component.BadgeWarning
	case "pending":
		return component.BadgePending
	default:
		return component.BadgeInfo
	}
}

func stepStatus(status string) component.StepStatus {
	switch status {
	case "passed":
		return component.StepDone
	case "failed":
		return component.StepFailed
	case "running":
		return component.StepActive
	default:
		return component.StepPending
	}
}

// RenderPhaseBar renders the phase indicator using component.Steps
func RenderPhaseBar(phases []PhaseProgress) node.Node {
	steps := make([]component.Step, len(phases))
	for i, p := range phases {
		steps[i] = component.Step{Label: p.ID, Status: stepStatus(p.Status)}
	}
	return component.Steps(steps)
}

// RenderPhaseDetail renders expanded phase output
func RenderPhaseDetail(phase *PhaseProgress, width int, selected bool, spinnerFrame string) []node.Node {
	var nodes []node.Node

	// Duration suffix
	duration := ""
	if !phase.StartTime.IsZero() {
		end := phase.EndTime
		if end.IsZero() {
			end = time.Now()
		}
		if d := end.Sub(phase.StartTime); d > time.Second {
			duration = fmt.Sprintf(" (%s)", d.Round(time.Second))
		}
	}

	// Header line: selector + expand icon + badge + id + duration
	selector := "  "
	if selected {
		selector = "› "
	}
	expandIcon := "▶"
	if phase.Expanded {
		expandIcon = "▼"
	}

	label := fmt.Sprintf("%s %s%s", phase.ID, phase.Status, duration)
	headerRow := node.Row(
		node.TextStyled(selector+expandIcon+" ", func() node.Color {
			if selected {
				return colSecondary
			}
			return colMuted
		}(), 0, 0),
		component.Badge(label, badgeStyle(phase.Status)),
	)
	nodes = append(nodes, headerRow)

	// Expanded content via Collapsible-style rendering
	if phase.Expanded {
		var children []node.Node

		// Output lines (last 10)
		if len(phase.Output) > 0 {
			maxLines := 10
			startIdx := 0
			if len(phase.Output) > maxLines {
				startIdx = len(phase.Output) - maxLines
				children = append(children, node.TextStyled(
					fmt.Sprintf("... (%d more lines)", startIdx), colMuted, 0, node.Dim))
			}
			for _, line := range phase.Output[startIdx:] {
				children = append(children, node.TextStyled(line, colText, 0, 0))
			}
		}

		// File changes
		if len(phase.Files) > 0 {
			children = append(children, node.TextStyled("Files:", colMuted, 0, 0))
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
				children = append(children, node.TextStyled(
					fmt.Sprintf("  %s %s", prefix, f.Path), fc, 0, 0))
			}
		}

		// Gates
		if len(phase.Gates) > 0 {
			children = append(children, node.TextStyled("Gates:", colMuted, 0, 0))
			for _, g := range phase.Gates {
				children = append(children, component.Badge(g.Name, badgeStyle(g.Status)))
			}
		}

		if len(children) > 0 {
			nodes = append(nodes, node.Indent(4, node.Column(children...)))
		}
	}

	return nodes
}

// RenderCheckpoint renders the checkpoint UI
func RenderCheckpoint(cp *CheckpointState) []node.Node {
	if cp == nil || !cp.Active {
		return nil
	}

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

	var nodes []node.Node
	nodes = append(nodes,
		node.Text(""),
		node.TextStyled(fmt.Sprintf("%s CHECKPOINT [%s]", icon, cp.Type), colWarning, 0, node.Bold),
	)

	nodes = append(nodes, node.Indent(2, node.Paragraph(cp.Message, colText, 0, 0)))

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
	// Header bar
	headerText := fmt.Sprintf(" %s  Building: %s", Logo(), state.ProjectName)
	if state.ProjectName == "" || state.ProjectName == "." {
		headerText = fmt.Sprintf(" %s  Building in current directory", Logo())
	}
	header := node.Bar(headerText, colPrimary, colDarkBg, node.Bold)

	// Task summary (truncated)
	taskLine := node.TextStyled(" Task: "+node.Truncate(state.Task, 80), colMuted, 0, 0)

	// Phase steps bar
	phaseBar := RenderPhaseBar(state.Phases)

	// Phase details (scrollable)
	var detailNodes []node.Node
	for i := range state.Phases {
		detailNodes = append(detailNodes, RenderPhaseDetail(&state.Phases[i], width-4, i == selectedIdx, spinnerFrame)...)
	}

	if state.Error != nil {
		detailNodes = append(detailNodes,
			node.Text(""),
			node.TextStyled(fmt.Sprintf("Error: %v", state.Error), colError, 0, node.Bold),
		)
	}

	if cpNodes := RenderCheckpoint(state.Checkpoint); cpNodes != nil {
		detailNodes = append(detailNodes, cpNodes...)
	}

	details := node.Column(detailNodes...).WithFlex(1).WithScrollToBottom()

	// Footer bar
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
	footer := node.Bar(helpText, colMuted, colDarkBg, 0)

	return node.Column(
		header,
		node.Separator(width),
		taskLine,
		node.Text(""),
		phaseBar,
		node.Text(""),
		details,
		node.Separator(width),
		footer,
	)
}

// elapsed returns a human-readable duration string
func elapsed(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// formatLines joins output lines with a prefix
func formatLines(lines []string, prefix string) string {
	return prefix + strings.Join(lines, "\n"+prefix)
}
