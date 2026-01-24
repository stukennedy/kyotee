package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stukennedy/kyotee/internal/orchestrator"
	"github.com/stukennedy/kyotee/internal/types"
)

// Messages
type (
	PhaseUpdateMsg struct {
		PhaseIdx int
		Status   types.PhaseStatus
	}
	OutputMsg    string
	NarrationMsg string
	ErrorMsg     error
	DoneMsg      struct{}
)

// Model is the TUI state
type Model struct {
	engine    *orchestrator.Engine
	phases    []phaseItem
	output    viewport.Model
	spinner   spinner.Model
	narration string
	width     int
	height    int
	err       error
	done      bool
	ready     bool
}

type phaseItem struct {
	name   string
	status types.PhaseStatus
}

// New creates a new TUI model
func New(engine *orchestrator.Engine) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = SpinnerStyle

	phases := make([]phaseItem, len(engine.State.Phases))
	for i, p := range engine.State.Phases {
		phases[i] = phaseItem{
			name:   p.Phase.ID,
			status: types.PhasePending,
		}
	}

	return Model{
		engine:  engine,
		phases:  phases,
		spinner: s,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.runOrchestrator(),
	)
}

func (m Model) runOrchestrator() tea.Cmd {
	return func() tea.Msg {
		// Wire up callbacks
		m.engine.OnPhase = func(idx int, status types.PhaseStatus) {
			// This runs in goroutine, we'll handle via messages
		}
		m.engine.OnOutput = func(phase, text string) {
			// This runs in goroutine
		}
		m.engine.OnNarrate = func(text string) {
			// This runs in goroutine
		}

		if err := m.engine.Run(); err != nil {
			return ErrorMsg(err)
		}
		return DoneMsg{}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Initialize viewport if not ready
		if !m.ready {
			m.output = viewport.New(msg.Width-20, msg.Height-12)
			m.output.Style = OutputTextStyle
			m.ready = true
		} else {
			m.output.Width = msg.Width - 20
			m.output.Height = msg.Height - 12
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case PhaseUpdateMsg:
		if msg.PhaseIdx < len(m.phases) {
			m.phases[msg.PhaseIdx].status = msg.Status
		}

	case OutputMsg:
		content := m.output.View() + string(msg)
		m.output.SetContent(content)
		m.output.GotoBottom()

	case NarrationMsg:
		m.narration = string(msg)

	case ErrorMsg:
		m.err = msg
		m.done = true

	case DoneMsg:
		m.done = true
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	if !m.ready {
		return "Loading..."
	}

	var b strings.Builder

	// Header
	header := lipgloss.JoinHorizontal(
		lipgloss.Left,
		Logo(),
		strings.Repeat(" ", max(0, m.width-40)),
		IterCountStyle.Render(fmt.Sprintf("iter %d/%d",
			m.engine.State.TotalIterations,
			m.engine.Spec.Limits.MaxTotalIterations)),
	)
	b.WriteString(HeaderStyle.Width(m.width).Render(header))
	b.WriteString("\n")

	// Main content: phases panel + output
	phasesPanel := m.renderPhases()
	outputPanel := m.renderOutput()

	mainContent := lipgloss.JoinHorizontal(
		lipgloss.Top,
		phasesPanel,
		outputPanel,
	)
	b.WriteString(mainContent)
	b.WriteString("\n")

	// Narration bar
	if m.narration != "" {
		narr := fmt.Sprintf("💭 Ralph: \"%s\"", m.narration)
		b.WriteString(NarrationStyle.Width(m.width - 4).Render(narr))
		b.WriteString("\n")
	}

	// Status bar
	status := "Press q to quit"
	if m.done {
		if m.err != nil {
			status = StatusBarStyle.Foreground(Error).Render(fmt.Sprintf("Error: %v", m.err))
		} else {
			status = StatusBarStyle.Foreground(Primary).Render(fmt.Sprintf("Done! Artifacts in: %s", m.engine.RunDir))
		}
	}
	b.WriteString(StatusBarStyle.Render(status))

	return b.String()
}

func (m Model) renderPhases() string {
	var lines []string
	lines = append(lines, TitleStyle.Render("PHASES"))
	lines = append(lines, "")

	for i, p := range m.phases {
		var symbol string
		var style lipgloss.Style

		switch p.status {
		case types.PhasePending:
			symbol = SymbolPending
			style = PhasePendingStyle
		case types.PhaseRunning:
			symbol = m.spinner.View()
			style = PhaseActiveStyle
		case types.PhasePassed:
			symbol = SymbolPassed
			style = PhasePassedStyle
		case types.PhaseFailed:
			symbol = SymbolFailed
			style = PhaseFailedStyle
		}

		// Highlight current phase
		name := p.name
		if i == m.engine.State.CurrentPhase && !m.done {
			name = style.Render(name)
		} else {
			name = style.Render(name)
		}

		lines = append(lines, fmt.Sprintf("%s %s", symbol, name))
	}

	return PhasePanelStyle.Render(strings.Join(lines, "\n"))
}

func (m Model) renderOutput() string {
	title := TitleStyle.Render("OUTPUT")
	content := m.output.View()

	return OutputPanelStyle.
		Width(m.width - 22).
		Height(m.height - 10).
		Render(title + "\n\n" + content)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
