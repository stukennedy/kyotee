package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/muesli/reflow/wordwrap"
)

// PhaseProgress tracks the progress of a single phase
type PhaseProgress struct {
	ID        string
	Status    string // pending, running, passed, failed
	StartTime time.Time
	EndTime   time.Time
	Output    []string // Lines of output
	Expanded  bool     // Whether details are shown
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

// RenderExecuteView renders the execute mode view
func RenderExecuteView(state *ExecuteState, width, selectedIdx int, spinner string) string {
	var b strings.Builder

	// Header with project info
	header := fmt.Sprintf("Building: %s", state.ProjectName)
	if state.ProjectName == "" || state.ProjectName == "." {
		header = "Building in current directory"
	}
	b.WriteString(HeaderStyle.Width(width).Render(
		Logo() + "  " + TitleStyle.Render(header),
	))
	b.WriteString("\n\n")

	// Task summary (truncated)
	taskPreview := state.Task
	if len(taskPreview) > 80 {
		taskPreview = taskPreview[:77] + "..."
	}
	b.WriteString(HelpStyle.Render("Task: " + taskPreview))
	b.WriteString("\n\n")

	// Phases
	for i, phase := range state.Phases {
		isSelected := i == selectedIdx
		b.WriteString(renderPhase(&phase, width-4, isSelected, spinner))
		b.WriteString("\n")
	}

	// Error display
	if state.Error != nil {
		b.WriteString("\n")
		b.WriteString(PhaseFailedStyle.Render(fmt.Sprintf("Error: %v", state.Error)))
		b.WriteString("\n")
	}

	// Footer with controls
	b.WriteString("\n")
	if state.Paused {
		b.WriteString(HelpStyle.Render("PAUSED - r: resume • q: quit • ↑↓: navigate • enter: expand/collapse"))
	} else {
		b.WriteString(HelpStyle.Render("p: pause • q: quit • ↑↓: navigate • enter: expand/collapse"))
	}

	return b.String()
}

func renderPhase(phase *PhaseProgress, width int, selected bool, spinner string) string {
	var b strings.Builder

	// Phase header line
	var icon string
	var style = PhasePendingStyle
	switch phase.Status {
	case "pending":
		icon = "○"
		style = PhasePendingStyle
	case "running":
		icon = spinner
		style = PhaseActiveStyle
	case "passed":
		icon = "✓"
		style = PhasePassedStyle
	case "failed":
		icon = "✗"
		style = PhaseFailedStyle
	}

	// Selection indicator
	selector := "  "
	if selected {
		selector = "› "
	}

	// Expand/collapse indicator
	expandIcon := "▶"
	if phase.Expanded {
		expandIcon = "▼"
	}

	// Duration
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
	b.WriteString(style.Render(header))
	b.WriteString("\n")

	// Expanded content
	if phase.Expanded && len(phase.Output) > 0 {
		contentWidth := width - 6
		if contentWidth < 20 {
			contentWidth = 20
		}

		// Show last N lines of output
		maxLines := 10
		startIdx := 0
		if len(phase.Output) > maxLines {
			startIdx = len(phase.Output) - maxLines
			b.WriteString(OutputTextStyle.Render(fmt.Sprintf("      ... (%d more lines)\n", startIdx)))
		}

		for _, line := range phase.Output[startIdx:] {
			wrapped := wordwrap.String(line, contentWidth)
			for _, wline := range strings.Split(wrapped, "\n") {
				b.WriteString("      ")
				b.WriteString(OutputTextStyle.Render(wline))
				b.WriteString("\n")
			}
		}

		// File changes
		if len(phase.Files) > 0 {
			b.WriteString(OutputTextStyle.Render("      Files:\n"))
			for _, f := range phase.Files {
				icon := "+"
				if f.Action == "modify" {
					icon = "~"
				} else if f.Action == "delete" {
					icon = "-"
				}
				b.WriteString(OutputTextStyle.Render(fmt.Sprintf("        %s %s\n", icon, f.Path)))
			}
		}

		// Gates
		if len(phase.Gates) > 0 {
			b.WriteString(OutputTextStyle.Render("      Gates:\n"))
			for _, g := range phase.Gates {
				gIcon := "○"
				gStyle := PhasePendingStyle
				switch g.Status {
				case "running":
					gIcon = spinner
					gStyle = PhaseActiveStyle
				case "passed":
					gIcon = "✓"
					gStyle = PhasePassedStyle
				case "failed":
					gIcon = "✗"
					gStyle = PhaseFailedStyle
				}
				b.WriteString(gStyle.Render(fmt.Sprintf("        %s %s\n", gIcon, g.Name)))
			}
		}
	}

	return b.String()
}
