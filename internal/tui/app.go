package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
	"github.com/stukennedy/kyotee/internal/orchestrator"
)

// AppMode represents the current mode
type AppMode int

const (
	ModeDiscovery AppMode = iota
	ModeExecute
)

// Messages
type (
	ResponseMsg struct {
		Content string
		Err     error
	}
	SpecReadyMsg struct {
		Spec map[string]any
	}
	ExecuteStartMsg struct{}
	ExecuteDoneMsg  struct {
		Err    error
		RunDir string
	}
)

// App is the main TUI application
type App struct {
	mode      AppMode
	discovery *orchestrator.Discovery
	agentDir  string
	repoRoot  string

	// Discovery mode
	messages  []chatMessage
	input     textarea.Model
	chatVP    viewport.Model
	spec      map[string]any
	specReady bool
	waiting   bool

	// Execute mode
	engine  *orchestrator.Engine
	phases  []phaseItem
	execVP  viewport.Model
	spinner spinner.Model

	// Shared
	width     int
	height    int
	ready     bool
	done      bool
	err       error
	runDir    string
	narration string
}

type chatMessage struct {
	role    string
	content string
}

type phaseItem struct {
	name   string
	status string
}

// NewApp creates a new application in discovery mode
func NewApp(agentDir, repoRoot string) App {
	ti := textarea.New()
	ti.Placeholder = "What would you like to build?"
	ti.Focus()
	ti.CharLimit = 2000
	ti.SetHeight(3)
	ti.ShowLineNumbers = false

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = SpinnerStyle

	discovery := orchestrator.NewDiscovery(agentDir, repoRoot)

	// Add initial greeting
	messages := []chatMessage{
		{role: "assistant", content: "Hey! I'm Kyotee. What would you like to build today?\n\nTell me about your project and I'll help figure out the details."},
	}

	return App{
		mode:      ModeDiscovery,
		discovery: discovery,
		agentDir:  agentDir,
		repoRoot:  repoRoot,
		messages:  messages,
		input:     ti,
		spinner:   s,
	}
}

func (a App) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		a.spinner.Tick,
	)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		return a.handleKey(msg)

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		a.updateLayout()

	case spinner.TickMsg:
		var cmd tea.Cmd
		a.spinner, cmd = a.spinner.Update(msg)
		cmds = append(cmds, cmd)

	case ResponseMsg:
		a.waiting = false
		if msg.Err != nil {
			a.messages = append(a.messages, chatMessage{
				role:    "assistant",
				content: fmt.Sprintf("Error: %v", msg.Err),
			})
		} else {
			a.messages = append(a.messages, chatMessage{
				role:    "assistant",
				content: msg.Content,
			})
		}
		a.updateChatViewport()

		// Check if spec is ready
		if a.discovery.IsSpecReady() {
			a.spec = a.discovery.GetSpec()
			a.specReady = true
		}

	case ExecuteStartMsg:
		a.mode = ModeExecute
		cmds = append(cmds, a.runExecute())

	case ExecuteDoneMsg:
		a.done = true
		a.err = msg.Err
		a.runDir = msg.RunDir
	}

	// Update textarea when not waiting
	if a.mode == ModeDiscovery && !a.waiting {
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return a, tea.Batch(cmds...)
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		return a, tea.Quit

	case tea.KeyEnter:
		if a.mode == ModeDiscovery {
			if msg.Alt {
				// Alt+Enter for newline in input - pass to textarea
				var cmd tea.Cmd
				a.input, cmd = a.input.Update(msg)
				return a, cmd
			}

			// Check for confirmation
			input := strings.TrimSpace(strings.ToLower(a.input.Value()))
			if a.specReady && (input == "yes" || input == "y") {
				a.input.Reset()
				return a, func() tea.Msg { return ExecuteStartMsg{} }
			}

			// Send message
			if !a.waiting && strings.TrimSpace(a.input.Value()) != "" {
				userMsg := strings.TrimSpace(a.input.Value())
				a.messages = append(a.messages, chatMessage{
					role:    "user",
					content: userMsg,
				})
				a.input.Reset()
				a.waiting = true
				a.updateChatViewport()

				return a, a.sendMessage(userMsg)
			}
			return a, nil
		}

	default:
		// Pass all other keys to textarea when in discovery mode
		if a.mode == ModeDiscovery && !a.waiting {
			var cmd tea.Cmd
			a.input, cmd = a.input.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	return a, tea.Batch(cmds...)
}

func (a *App) sendMessage(msg string) tea.Cmd {
	return func() tea.Msg {
		response, err := a.discovery.SendMessage(msg)
		return ResponseMsg{Content: response, Err: err}
	}
}

func (a *App) runExecute() tea.Cmd {
	return func() tea.Msg {
		// Build task from spec
		task := a.buildTaskFromSpec()

		// Determine project directory
		projectRoot := a.repoRoot
		if projectName, ok := a.spec["project_name"].(string); ok && projectName != "" && projectName != "." {
			projectRoot = filepath.Join(a.repoRoot, projectName)
			// Create the project directory
			if err := os.MkdirAll(projectRoot, 0755); err != nil {
				return ExecuteDoneMsg{Err: fmt.Errorf("failed to create project directory: %w", err)}
			}
		}

		// Load spec config
		spec, err := orchestrator.LoadSpecWithOverrides(a.agentDir, a.spec)
		if err != nil {
			return ExecuteDoneMsg{Err: err}
		}

		// Create engine with the project root
		engine, err := orchestrator.NewEngine(spec, task, projectRoot, a.agentDir)
		if err != nil {
			return ExecuteDoneMsg{Err: err}
		}

		// Run
		if err := engine.Run(); err != nil {
			return ExecuteDoneMsg{Err: err, RunDir: engine.RunDir}
		}

		return ExecuteDoneMsg{RunDir: engine.RunDir}
	}
}

func (a *App) buildTaskFromSpec() string {
	if a.spec == nil {
		return "Build the project"
	}

	var parts []string
	if goal, ok := a.spec["goal"].(string); ok {
		parts = append(parts, goal)
	}
	if features, ok := a.spec["features"].([]any); ok {
		for _, f := range features {
			if fs, ok := f.(string); ok {
				parts = append(parts, "- "+fs)
			}
		}
	}

	return strings.Join(parts, "\n")
}

func (a *App) updateLayout() {
	if a.width == 0 || a.height == 0 {
		return
	}

	inputHeight := 5
	headerHeight := 3
	specHeight := 0
	if a.specReady {
		specHeight = 8
	}

	vpHeight := a.height - inputHeight - headerHeight - specHeight - 4

	if !a.ready {
		a.chatVP = viewport.New(a.width-4, vpHeight)
		a.execVP = viewport.New(a.width-4, a.height-10)
		a.ready = true
	} else {
		a.chatVP.Width = a.width - 4
		a.chatVP.Height = vpHeight
		a.execVP.Width = a.width - 4
		a.execVP.Height = a.height - 10
	}

	a.input.SetWidth(a.width - 4)
	a.updateChatViewport()
}

func (a *App) updateChatViewport() {
	if !a.ready {
		return
	}

	// Calculate available width for content (viewport - padding - prefix space)
	// ChatBox has padding of 2 on each side, plus border, and prefix like "You: " or "🐺 "
	contentWidth := a.chatVP.Width - 6 // Leave room for prefix
	if contentWidth < 20 {
		contentWidth = 20
	}

	var b strings.Builder
	for _, msg := range a.messages {
		if msg.role == "user" {
			prefix := UserMsgStyle.Render("You: ")
			b.WriteString(prefix)
			// Word-wrap the content and indent continuation lines
			wrapped := wordwrap.String(msg.content, contentWidth)
			lines := strings.Split(wrapped, "\n")
			for i, line := range lines {
				if i > 0 {
					b.WriteString("      ") // Indent to align with first line
				}
				b.WriteString(UserContentStyle.Render(line))
				if i < len(lines)-1 {
					b.WriteString("\n")
				}
			}
		} else {
			prefix := AssistantMsgStyle.Render("🐺 ")
			b.WriteString(prefix)
			// Word-wrap the content and indent continuation lines
			wrapped := wordwrap.String(msg.content, contentWidth)
			lines := strings.Split(wrapped, "\n")
			for i, line := range lines {
				if i > 0 {
					b.WriteString("   ") // Indent to align with first line (emoji is ~2 chars)
				}
				b.WriteString(AssistantContentStyle.Render(line))
				if i < len(lines)-1 {
					b.WriteString("\n")
				}
			}
		}
		b.WriteString("\n\n")
	}

	if a.waiting {
		b.WriteString(AssistantMsgStyle.Render("🐺 "))
		b.WriteString(ThinkingStyle.Render(a.spinner.View() + " thinking..."))
	}

	a.chatVP.SetContent(b.String())
	a.chatVP.GotoBottom()
}

func (a App) View() string {
	if !a.ready {
		return "Loading..."
	}

	if a.mode == ModeDiscovery {
		return a.viewDiscovery()
	}
	return a.viewExecute()
}

func (a App) viewDiscovery() string {
	var b strings.Builder

	// Header
	header := HeaderStyle.Width(a.width).Render(
		lipgloss.JoinHorizontal(lipgloss.Left,
			Logo(),
			"  ",
			TitleStyle.Render("Discovery"),
		),
	)
	b.WriteString(header)
	b.WriteString("\n")

	// Chat viewport
	chatBox := ChatBoxStyle.Width(a.width - 2).Render(a.chatVP.View())
	b.WriteString(chatBox)
	b.WriteString("\n")

	// Spec panel (if ready)
	if a.specReady {
		specView := a.renderSpec()
		b.WriteString(specView)
		b.WriteString("\n")
	}

	// Input
	inputBox := InputBoxStyle.Width(a.width - 2).Render(a.input.View())
	b.WriteString(inputBox)
	b.WriteString("\n")

	// Help
	help := "Enter: send • Cmd/Alt+Enter: newline • Esc: quit"
	if a.specReady {
		help = "Type 'yes' to start building • " + help
	}
	b.WriteString(HelpStyle.Render(help))

	return b.String()
}

func (a App) renderSpec() string {
	if a.spec == nil {
		return ""
	}

	var lines []string
	lines = append(lines, SpecTitleStyle.Render("📋 READY TO BUILD"))

	if goal, ok := a.spec["goal"].(string); ok {
		lines = append(lines, fmt.Sprintf("  %s", goal))
	}
	if projectName, ok := a.spec["project_name"].(string); ok && projectName != "" && projectName != "." {
		lines = append(lines, fmt.Sprintf("  Project: %s/", projectName))
	}
	if lang, ok := a.spec["language"].(string); ok {
		lines = append(lines, fmt.Sprintf("  Language: %s", lang))
	}
	if features, ok := a.spec["features"].([]any); ok && len(features) > 0 {
		featStrs := make([]string, 0, len(features))
		for _, f := range features {
			if fs, ok := f.(string); ok {
				featStrs = append(featStrs, fs)
			}
		}
		if len(featStrs) > 0 {
			lines = append(lines, fmt.Sprintf("  Features: %s", strings.Join(featStrs, ", ")))
		}
	}

	return SpecBoxStyle.Width(a.width - 2).Render(strings.Join(lines, "\n"))
}

func (a App) viewExecute() string {
	var b strings.Builder

	// Header
	header := HeaderStyle.Width(a.width).Render(
		lipgloss.JoinHorizontal(lipgloss.Left,
			Logo(),
			"  ",
			TitleStyle.Render("Building..."),
			"  ",
			a.spinner.View(),
		),
	)
	b.WriteString(header)
	b.WriteString("\n")

	// Status
	if a.done {
		if a.err != nil {
			b.WriteString(PhaseFailedStyle.Render(fmt.Sprintf("Error: %v", a.err)))
		} else {
			b.WriteString(PhasePassedStyle.Render(fmt.Sprintf("✓ Done! Artifacts in: %s", a.runDir)))
		}
		b.WriteString("\n\n")
		b.WriteString(HelpStyle.Render("Press Esc to exit"))
	} else {
		b.WriteString(ThinkingStyle.Render("Running phases: context → plan → implement → verify → deliver"))
		b.WriteString("\n")
	}

	return b.String()
}
