package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wordwrap"
	"github.com/stukennedy/kyotee/internal/orchestrator"
	"github.com/stukennedy/kyotee/internal/project"
	"github.com/stukennedy/kyotee/internal/types"
)

// AppMode represents the current mode
type AppMode int

const (
	ModeDiscovery AppMode = iota
	ModeExecute
	ModeHistory
)

// Messages for TUI updates
type (
	ResponseMsg struct {
		Content string
		Err     error
	}
	ExecuteStartMsg   struct{}
	ExecuteDoneMsg    struct{ Err error }
	PhaseStartMsg     struct{ PhaseID string }
	PhaseEndMsg       struct{ PhaseID string; Passed bool }
	PhaseOutputMsg    struct{ PhaseID string; Line string }
	FileChangeMsg     struct{ PhaseID string; Path string; Action string }
	GateStartMsg      struct{ PhaseID string; GateName string }
	GateEndMsg        struct{ PhaseID string; GateName string; Passed bool }
	NarrationMsg      struct{ Text string }
	PauseMsg          struct{}
	ResumeMsg         struct{}
	AutonomousOutput  struct{ Text string }
	AutonomousToolMsg struct{ Name string; Input any }
	FolderSelectedMsg struct{ Path string; IsNew bool }
)

// App is the main TUI application
type App struct {
	mode      AppMode
	discovery *orchestrator.Discovery
	agentDir  string
	repoRoot  string

	// Discovery mode
	messages     []chatMessage
	input        textarea.Model
	chatVP       viewport.Model
	spec         map[string]any
	specReady    bool
	askingFolder bool     // Asking user about project folder location
	projectName  string   // Derived from spec for folder name suggestion
	waiting      bool

	// Execute mode
	execState   *ExecuteState
	selectedIdx int
	execVP      viewport.Model
	spinner     spinner.Model
	cancelExec  func() // Function to cancel execution
	autonomous  bool   // Use autonomous execution with direct API calls

	// Autonomous mode output
	autonomousOutput []string

	// State persistence
	proj    *project.Project // Project state in .kyotee/
	program *tea.Program

	// Shared
	width  int
	height int
	ready  bool
}

type chatMessage struct {
	role    string
	content string
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

// NewAppForProject creates an application with state persistence in local .kyotee/
// - Opens or creates .kyotee/ in current directory
// - Loads existing conversation and spec
// - Auto-resumes most recent unfinished job
func NewAppForProject(agentDir, repoRoot string) App {
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

	// Open or create .kyotee/ in current directory
	proj, err := project.Open(repoRoot)
	if err != nil {
		// Fall back to basic app without persistence
		return App{
			mode:      ModeDiscovery,
			discovery: discovery,
			agentDir:  agentDir,
			repoRoot:  repoRoot,
			messages: []chatMessage{
				{role: "assistant", content: "Hey! I'm Kyotee. What would you like to build today?\n\nTell me about your project and I'll help figure out the details."},
			},
			input:   ti,
			spinner: s,
		}
	}

	// Load existing messages from project
	messages := []chatMessage{
		{role: "assistant", content: "Hey! I'm Kyotee. What would you like to build today?\n\nTell me about your project and I'll help figure out the details."},
	}

	if proj.HasConversation() {
		messages = []chatMessage{}
		// Also load into discovery's history so Claude has context
		discoveryHistory := []orchestrator.DiscoveryMessage{}
		for _, m := range proj.Conversation.Messages {
			messages = append(messages, chatMessage{
				role:    m.Role,
				content: m.Content,
			})
			discoveryHistory = append(discoveryHistory, orchestrator.DiscoveryMessage{
				Role:    m.Role,
				Content: m.Content,
			})
		}
		// Sync to discovery so Claude has the conversation context
		discovery.LoadHistory(discoveryHistory)
	}

	// Load existing spec
	var spec map[string]any
	specReady := false
	if proj.HasSpec() {
		spec = proj.Spec.Raw
		specReady = true
	}

	app := App{
		mode:      ModeDiscovery,
		discovery: discovery,
		agentDir:  agentDir,
		repoRoot:  repoRoot,
		messages:  messages,
		input:     ti,
		spinner:   s,
		proj:      proj,
		spec:      spec,
		specReady: specReady,
	}

	// Auto-resume if there's an unfinished job
	if proj.CanResume() {
		app.mode = ModeExecute
		app.execState = &ExecuteState{
			ProjectName: proj.Spec.ProjectName,
			Task:        proj.Spec.Goal,
			StartTime:   proj.Job.StartedAt,
			Paused:      proj.Job.Status == project.JobStatusPaused,
		}
		for _, p := range proj.Job.Phases {
			app.execState.Phases = append(app.execState.Phases, PhaseProgress{
				ID:     p.ID,
				Status: p.Status,
			})
		}
	}

	return app
}

// SetProgram sets the tea.Program reference for sending messages
func (a *App) SetProgram(p *tea.Program) {
	a.program = p
}

// NewAppWithJob creates an application to resume an existing job
func NewAppWithJob(agentDir, repoRoot string, jobState *orchestrator.JobState) App {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = SpinnerStyle

	// Convert job state to execute state
	execState := &ExecuteState{
		ProjectName: jobState.ProjectName,
		Task:        jobState.Task,
		StartTime:   jobState.StartTime,
		RunDir:      filepath.Join(agentDir, "runs", jobState.ID),
		Paused:      jobState.Status == "paused",
	}

	// Convert phases
	for _, jp := range jobState.Phases {
		execState.Phases = append(execState.Phases, PhaseProgress{
			ID:        jp.ID,
			Status:    jp.Status,
			StartTime: jp.StartTime,
			EndTime:   jp.EndTime,
			Output:    jp.Output,
			Expanded:  jp.Status == "running" || jp.Status == "failed",
		})
	}
	execState.CurrentIdx = jobState.CurrentPhase

	return App{
		mode:      ModeExecute,
		agentDir:  agentDir,
		repoRoot:  repoRoot,
		spec:      jobState.Spec,
		execState: execState,
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

	// Discovery messages
	case ResponseMsg:
		a.waiting = false
		var content string
		if msg.Err != nil {
			content = fmt.Sprintf("Error: %v", msg.Err)
		} else {
			content = msg.Content
		}
		a.messages = append(a.messages, chatMessage{
			role:    "assistant",
			content: content,
		})
		// Save to project
		if a.proj != nil {
			a.proj.AddMessage("assistant", content)
		}
		a.updateChatViewport()

		if a.discovery.IsSpecReady() {
			a.spec = a.discovery.GetSpec()
			a.specReady = true
			// Save spec to project
			if a.proj != nil {
				a.proj.SetSpec(a.spec)
			}
		}

	// Folder selection
	case FolderSelectedMsg:
		a.askingFolder = false
		if msg.IsNew {
			// Create new folder and move .kyotee there
			if err := setupProjectFolder(&a, msg.Path); err != nil {
				a.messages = append(a.messages, chatMessage{
					role:    "assistant",
					content: fmt.Sprintf("Error creating folder: %v", err),
				})
				a.updateChatViewport()
				return a, nil
			}
			a.repoRoot = msg.Path
			a.messages = append(a.messages, chatMessage{
				role:    "assistant",
				content: fmt.Sprintf("Created %s/\nStarting build...", a.projectName),
			})
		} else {
			// Use current folder, just create CLAUDE.md
			createClaudeMD(msg.Path)
			a.messages = append(a.messages, chatMessage{
				role:    "assistant",
				content: "Starting build in current folder...",
			})
		}
		a.updateChatViewport()
		return a, func() tea.Msg { return ExecuteStartMsg{} }

	// Execute messages
	case ExecuteStartMsg:
		a.mode = ModeExecute
		a.autonomous = true // Always use autonomous mode after discovery
		if a.autonomous {
			a.autonomousOutput = []string{"🚀 Starting autonomous execution...\n\n"}
			cmds = append(cmds, a.spinner.Tick, a.runAutonomous())
		} else {
			a.initExecuteState()
			cmds = append(cmds, a.runExecute())
		}

	case PhaseStartMsg:
		if a.execState != nil {
			for i := range a.execState.Phases {
				if a.execState.Phases[i].ID == msg.PhaseID {
					a.execState.Phases[i].Status = "running"
					a.execState.Phases[i].StartTime = time.Now()
					a.execState.CurrentIdx = i
					// Auto-expand running phase
					a.execState.Phases[i].Expanded = true
					a.selectedIdx = i
					break
				}
			}
		}

	case PhaseEndMsg:
		if a.execState != nil {
			for i := range a.execState.Phases {
				if a.execState.Phases[i].ID == msg.PhaseID {
					if msg.Passed {
						a.execState.Phases[i].Status = "passed"
					} else {
						a.execState.Phases[i].Status = "failed"
					}
					a.execState.Phases[i].EndTime = time.Now()
					break
				}
			}
		}

	case PhaseOutputMsg:
		if a.execState != nil {
			for i := range a.execState.Phases {
				if a.execState.Phases[i].ID == msg.PhaseID {
					a.execState.Phases[i].Output = append(a.execState.Phases[i].Output, msg.Line)
					break
				}
			}
		}

	case FileChangeMsg:
		if a.execState != nil {
			for i := range a.execState.Phases {
				if a.execState.Phases[i].ID == msg.PhaseID {
					a.execState.Phases[i].Files = append(a.execState.Phases[i].Files, FileChange{
						Path:   msg.Path,
						Action: msg.Action,
					})
					break
				}
			}
		}

	case GateStartMsg:
		if a.execState != nil {
			for i := range a.execState.Phases {
				if a.execState.Phases[i].ID == msg.PhaseID {
					a.execState.Phases[i].Gates = append(a.execState.Phases[i].Gates, GateProgress{
						Name:   msg.GateName,
						Status: "running",
					})
					break
				}
			}
		}

	case GateEndMsg:
		if a.execState != nil {
			for i := range a.execState.Phases {
				if a.execState.Phases[i].ID == msg.PhaseID {
					for j := range a.execState.Phases[i].Gates {
						if a.execState.Phases[i].Gates[j].Name == msg.GateName {
							if msg.Passed {
								a.execState.Phases[i].Gates[j].Status = "passed"
							} else {
								a.execState.Phases[i].Gates[j].Status = "failed"
							}
							break
						}
					}
					break
				}
			}
		}

	case NarrationMsg:
		if a.execState != nil {
			// Add narration to current phase output
			idx := a.execState.CurrentIdx
			if idx >= 0 && idx < len(a.execState.Phases) {
				a.execState.Phases[idx].Output = append(
					a.execState.Phases[idx].Output,
					"💭 "+msg.Text,
				)
			}
		}

	case ExecuteDoneMsg:
		if a.execState != nil {
			a.execState.Error = msg.Err
		}

	case PauseMsg:
		if a.execState != nil && !a.execState.Paused {
			a.execState.Paused = true
			if a.cancelExec != nil {
				a.cancelExec()
			}
		}

	case ResumeMsg:
		if a.execState != nil && a.execState.Paused {
			a.execState.Paused = false
			cmds = append(cmds, a.runExecute())
		}

	case AutonomousOutput:
		a.autonomousOutput = append(a.autonomousOutput, msg.Text)
		// Auto-scroll to bottom
		if a.ready {
			content := strings.Join(a.autonomousOutput, "")
			wrapped := wordwrap.String(content, a.execVP.Width-2)
			a.execVP.SetContent(wrapped)
			a.execVP.GotoBottom()
		}

	case AutonomousToolMsg:
		// Log tool call to output
		a.autonomousOutput = append(a.autonomousOutput, fmt.Sprintf("🔧 %s\n", msg.Name))
	}

	// Update textarea when in discovery mode
	if a.mode == ModeDiscovery && !a.waiting {
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return a, tea.Batch(cmds...)
}

func (a *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Global keys
	switch msg.Type {
	case tea.KeyCtrlC:
		return a, tea.Quit

	case tea.KeyEsc:
		if a.mode == ModeExecute {
			// In autonomous mode, cancel and quit
			if a.autonomous {
				if a.cancelExec != nil {
					a.cancelExec()
				}
				return a, tea.Quit
			}
			// In legacy execute mode, Esc pauses or quits if done
			if a.execState != nil && (a.execState.Error != nil || a.allPhasesDone()) {
				return a, tea.Quit
			}
			// Otherwise pause
			return a, func() tea.Msg { return PauseMsg{} }
		}
		return a, tea.Quit
	}

	// Mode-specific keys
	switch a.mode {
	case ModeDiscovery:
		return a.handleDiscoveryKey(msg)
	case ModeExecute:
		return a.handleExecuteKey(msg)
	}

	return a, tea.Batch(cmds...)
}

func (a *App) handleDiscoveryKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlJ:
		// Ctrl+J inserts a newline (works in most terminals)
		if !a.waiting {
			a.input.InsertString("\n")
		}
		return a, nil

	case tea.KeyPgUp:
		// Scroll chat viewport up
		a.chatVP, _ = a.chatVP.Update(msg)
		return a, nil

	case tea.KeyPgDown:
		// Scroll chat viewport down
		a.chatVP, _ = a.chatVP.Update(msg)
		return a, nil

	case tea.KeyUp:
		// Scroll up with arrow key when waiting (can't type)
		if a.waiting {
			a.chatVP, _ = a.chatVP.Update(msg)
			return a, nil
		}

	case tea.KeyDown:
		// Scroll down with arrow key when waiting
		if a.waiting {
			a.chatVP, _ = a.chatVP.Update(msg)
			return a, nil
		}

	case tea.KeyEnter:
		// Enter sends the message
		input := strings.TrimSpace(strings.ToLower(a.input.Value()))

		// Handle folder selection
		if a.askingFolder {
			a.input.Reset()
			switch input {
			case "1", "here", "current":
				// Use current folder
				return a, func() tea.Msg {
					return FolderSelectedMsg{Path: a.repoRoot, IsNew: false}
				}
			case "2", "new", "child":
				// Create new child folder
				newPath := filepath.Join(a.repoRoot, a.projectName)
				return a, func() tea.Msg {
					return FolderSelectedMsg{Path: newPath, IsNew: true}
				}
			default:
				// Invalid input, show options again
				a.messages = append(a.messages, chatMessage{
					role:    "assistant",
					content: "Please enter 1 (current folder) or 2 (new folder):",
				})
				a.updateChatViewport()
				return a, nil
			}
		}

		// Spec approved - ask about folder location
		if a.specReady && (input == "yes" || input == "y") {
			a.input.Reset()
			a.askingFolder = true
			// Extract project name from spec for folder suggestion
			a.projectName = a.getProjectNameFromSpec()
			a.messages = append(a.messages, chatMessage{
				role:    "assistant",
				content: fmt.Sprintf("Where should I create the project?\n\n  1) Here (current folder: %s)\n  2) New folder: %s/\n\nEnter 1 or 2:", filepath.Base(a.repoRoot), a.projectName),
			})
			a.updateChatViewport()
			return a, nil
		}

		if !a.waiting && strings.TrimSpace(a.input.Value()) != "" {
			userMsg := strings.TrimSpace(a.input.Value())
			a.messages = append(a.messages, chatMessage{
				role:    "user",
				content: userMsg,
			})
			// Save to project
			if a.proj != nil {
				a.proj.AddMessage("user", userMsg)
			}
			a.input.Reset()
			a.waiting = true
			a.updateChatViewport()
			return a, a.sendMessage(userMsg)
		}

	default:
		if !a.waiting {
			var cmd tea.Cmd
			a.input, cmd = a.input.Update(msg)
			return a, cmd
		}
	}

	return a, nil
}

func (a *App) handleExecuteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if a.selectedIdx > 0 {
			a.selectedIdx--
		}
	case tea.KeyDown:
		if a.execState != nil && a.selectedIdx < len(a.execState.Phases)-1 {
			a.selectedIdx++
		}
	case tea.KeyPgUp:
		// Scroll viewport up
		a.execVP, _ = a.execVP.Update(msg)
	case tea.KeyPgDown:
		// Scroll viewport down
		a.execVP, _ = a.execVP.Update(msg)
	case tea.KeyEnter:
		// Toggle expand/collapse
		if a.execState != nil && a.selectedIdx < len(a.execState.Phases) {
			a.execState.Phases[a.selectedIdx].Expanded = !a.execState.Phases[a.selectedIdx].Expanded
		}
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "j":
			// Scroll down
			for i := 0; i < 3; i++ {
				a.execVP, _ = a.execVP.Update(tea.KeyMsg{Type: tea.KeyDown})
			}
		case "k":
			// Scroll up
			for i := 0; i < 3; i++ {
				a.execVP, _ = a.execVP.Update(tea.KeyMsg{Type: tea.KeyUp})
			}
		case "p":
			if a.execState != nil && !a.execState.Paused {
				return a, func() tea.Msg { return PauseMsg{} }
			}
		case "r":
			if a.execState != nil && a.execState.Paused {
				return a, func() tea.Msg { return ResumeMsg{} }
			}
		case "q":
			return a, tea.Quit
		}
	}

	return a, nil
}

func (a *App) allPhasesDone() bool {
	if a.execState == nil {
		return false
	}
	for _, p := range a.execState.Phases {
		if p.Status == "pending" || p.Status == "running" {
			return false
		}
	}
	return true
}

func (a *App) sendMessage(msg string) tea.Cmd {
	return func() tea.Msg {
		response, err := a.discovery.SendMessage(msg)
		return ResponseMsg{Content: response, Err: err}
	}
}

func (a *App) initExecuteState() {
	projectName := "."
	if pn, ok := a.spec["project_name"].(string); ok && pn != "" {
		projectName = pn
	}
	a.execState = NewExecuteState(projectName, a.buildTaskFromSpec())
}

func (a *App) runExecute() tea.Cmd {
	// Create a cancellable context for pause support
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelExec = cancel

	// Capture program reference for sending updates
	program := a.program

	return func() tea.Msg {
		task := a.buildTaskFromSpec()

		// Use the project from the app (already opened in NewAppForProject)
		proj := a.proj
		if proj == nil {
			return ExecuteDoneMsg{Err: fmt.Errorf("no project initialized")}
		}

		// Ensure spec is set
		if a.spec != nil {
			proj.SetSpec(a.spec)
		}

		// Start the job
		proj.StartJob()

		spec, err := orchestrator.LoadSpecWithOverrides(a.agentDir, a.spec)
		if err != nil {
			proj.SetJobStatus(project.JobStatusFailed, err.Error())
			return ExecuteDoneMsg{Err: err}
		}

		// Use project's phases directory for run output
		runDir := filepath.Join(proj.KyoteeDir, project.PhasesDir)
		engine, err := orchestrator.NewEngineWithRunDir(spec, task, a.repoRoot, a.agentDir, runDir)
		if err != nil {
			proj.SetJobStatus(project.JobStatusFailed, err.Error())
			return ExecuteDoneMsg{Err: err}
		}

		// Store run dir
		if a.execState != nil {
			a.execState.RunDir = engine.RunDir
		}

		// Connect engine callbacks to update TUI and project state
		engine.OnPhase = func(idx int, status types.PhaseStatus) {
			phaseID := engine.State.Phases[idx].Phase.ID
			switch status {
			case types.PhaseRunning:
				proj.UpdatePhase(phaseID, "running")
				if program != nil {
					program.Send(PhaseStartMsg{PhaseID: phaseID})
				}
			case types.PhasePassed:
				proj.UpdatePhase(phaseID, "passed")
				if program != nil {
					program.Send(PhaseEndMsg{PhaseID: phaseID, Passed: true})
				}
			case types.PhaseFailed:
				proj.UpdatePhase(phaseID, "failed")
				if program != nil {
					program.Send(PhaseEndMsg{PhaseID: phaseID, Passed: false})
				}
			}
		}

		engine.OnOutput = func(phaseID, text string) {
			if program != nil {
				program.Send(PhaseOutputMsg{PhaseID: phaseID, Line: text})
			}
		}

		engine.OnNarrate = func(text string) {
			if program != nil {
				program.Send(NarrationMsg{Text: text})
			}
		}

		// Run with context for pause support
		if err := engine.RunWithContext(ctx); err != nil {
			// Check if this was a pause (not a real error)
			if errors.Is(err, orchestrator.ErrPaused) {
				proj.SetJobStatus(project.JobStatusPaused, "")
				return ExecuteDoneMsg{Err: nil} // Not an error, just paused
			}
			proj.SetJobStatus(project.JobStatusFailed, err.Error())
			return ExecuteDoneMsg{Err: err}
		}

		proj.SetJobStatus(project.JobStatusCompleted, "")
		return ExecuteDoneMsg{}
	}
}

func (a *App) runAutonomous() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelExec = cancel

	program := a.program

	return func() tea.Msg {
		// Build task from spec
		task := a.buildTaskFromSpec()

		// Get skill content if available
		skillContent := ""
		if a.discovery != nil && a.discovery.ActiveSkill != nil {
			skillContent = a.discovery.ActiveSkill.ToPromptContext()
		}

		// Create autonomous engine
		engine := orchestrator.NewAutonomousEngine(a.spec, task, a.repoRoot, a.agentDir)
		engine.SkillContent = skillContent

		// Connect callbacks
		engine.OnOutput = func(text string) {
			if program != nil {
				program.Send(AutonomousOutput{Text: text})
			}
		}

		engine.OnPhase = func(phase, status string) {
			if program != nil {
				program.Send(AutonomousOutput{Text: fmt.Sprintf("\n[%s] %s\n", phase, status)})
			}
		}

		engine.OnTool = func(name string, input any) {
			if program != nil {
				program.Send(AutonomousToolMsg{Name: name, Input: input})
			}
		}

		// Run autonomously
		if err := engine.Run(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return ExecuteDoneMsg{Err: nil} // User cancelled
			}
			return ExecuteDoneMsg{Err: err}
		}

		return ExecuteDoneMsg{}
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

func (a *App) getProjectNameFromSpec() string {
	if a.spec == nil {
		return "my-project"
	}

	// Try to get project name from spec
	if name, ok := a.spec["project_name"].(string); ok && name != "" {
		return sanitizeFolderName(name)
	}
	if name, ok := a.spec["name"].(string); ok && name != "" {
		return sanitizeFolderName(name)
	}
	// Try to derive from goal
	if goal, ok := a.spec["goal"].(string); ok && goal != "" {
		// Take first few words and convert to kebab-case
		words := strings.Fields(goal)
		if len(words) > 3 {
			words = words[:3]
		}
		name := strings.ToLower(strings.Join(words, "-"))
		return sanitizeFolderName(name)
	}

	return "my-project"
}

func sanitizeFolderName(name string) string {
	// Convert to lowercase, replace spaces with hyphens, remove special chars
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	// Keep only alphanumeric and hyphens
	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	// Trim leading/trailing hyphens
	return strings.Trim(result.String(), "-")
}

func setupProjectFolder(a *App, newPath string) error {
	// Create new folder
	if err := os.MkdirAll(newPath, 0755); err != nil {
		return fmt.Errorf("create folder: %w", err)
	}

	// Move .kyotee from current location to new folder
	oldKyotee := filepath.Join(a.repoRoot, ".kyotee")
	newKyotee := filepath.Join(newPath, ".kyotee")

	if _, err := os.Stat(oldKyotee); err == nil {
		// .kyotee exists, move it
		if err := os.Rename(oldKyotee, newKyotee); err != nil {
			// If rename fails (cross-device), try copy
			if err := copyDir(oldKyotee, newKyotee); err != nil {
				return fmt.Errorf("move .kyotee: %w", err)
			}
			os.RemoveAll(oldKyotee)
		}
	}

	// Update project reference
	if a.proj != nil {
		newProj, err := project.Open(newPath)
		if err == nil {
			// Copy spec to new project
			newProj.SetSpec(a.spec)
			a.proj = newProj
		}
	}

	// Create CLAUDE.md
	createClaudeMD(newPath)

	return nil
}

func createClaudeMD(projectPath string) {
	claudeMD := `# Kyotee Project

This project was scaffolded by [Kyotee](https://github.com/stukennedy/kyotee), an autonomous development agent.

## Project Resources

The ` + "`.kyotee/`" + ` folder contains:

- **` + "`spec.json`" + `** - The approved specification that defines what this project should do
- **` + "`conversation.json`" + `** - Discovery conversation history (for context/resume)

## For Claude Code

When working on this project, you can reference the spec for requirements:

` + "```bash" + `
cat .kyotee/spec.json
` + "```" + `

The spec is the source of truth for:
- Project purpose and features
- Tech stack decisions
- Architecture choices made during discovery

## Resuming Work

If the project is incomplete, you can resume with:

` + "```bash" + `
kyotee --continue
` + "```" + `

This will pick up where the last session left off.
`

	claudePath := filepath.Join(projectPath, "CLAUDE.md")
	os.WriteFile(claudePath, []byte(claudeMD), 0644)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(src, path)
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, info.Mode())
	})
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

	contentWidth := a.chatVP.Width - 6
	if contentWidth < 20 {
		contentWidth = 20
	}

	var b strings.Builder
	for _, msg := range a.messages {
		if msg.role == "user" {
			prefix := UserMsgStyle.Render("You: ")
			b.WriteString(prefix)
			wrapped := wordwrap.String(msg.content, contentWidth)
			lines := strings.Split(wrapped, "\n")
			for i, line := range lines {
				if i > 0 {
					b.WriteString("      ")
				}
				b.WriteString(UserContentStyle.Render(line))
				if i < len(lines)-1 {
					b.WriteString("\n")
				}
			}
		} else {
			prefix := AssistantMsgStyle.Render("🐺 ")
			b.WriteString(prefix)
			wrapped := wordwrap.String(msg.content, contentWidth)
			lines := strings.Split(wrapped, "\n")
			for i, line := range lines {
				if i > 0 {
					b.WriteString("   ")
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

	switch a.mode {
	case ModeDiscovery:
		return a.viewDiscovery()
	case ModeExecute:
		return a.viewExecute()
	default:
		return a.viewDiscovery()
	}
}

func (a App) viewDiscovery() string {
	var b strings.Builder

	// Show "Resumed" indicator if we loaded existing state
	modeText := "Discovery"
	if a.proj != nil && a.proj.HasConversation() && len(a.messages) > 1 {
		modeText = "Discovery " + PhasePassedStyle.Render("(resumed)")
	}

	header := HeaderStyle.Width(a.width).Render(
		lipgloss.JoinHorizontal(lipgloss.Left,
			Logo(),
			"  ",
			TitleStyle.Render(modeText),
		),
	)
	b.WriteString(header)
	b.WriteString("\n")

	chatBox := ChatBoxStyle.Width(a.width - 2).Render(a.chatVP.View())
	b.WriteString(chatBox)
	b.WriteString("\n")

	if a.specReady {
		specView := a.renderSpec()
		b.WriteString(specView)
		b.WriteString("\n")
	}

	inputBox := InputBoxStyle.Width(a.width - 2).Render(a.input.View())
	b.WriteString(inputBox)
	b.WriteString("\n")

	help := "Enter: send • Ctrl+J: newline • PgUp/PgDn: scroll • Esc: quit"
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
		lines = append(lines, fmt.Sprintf("  %s", PhasePassedStyle.Render(goal)))
	}
	if projectName, ok := a.spec["project_name"].(string); ok && projectName != "" && projectName != "." {
		lines = append(lines, fmt.Sprintf("  %s %s", HelpStyle.Render("Project:"), TitleStyle.Render(projectName+"/")))
	}
	if lang, ok := a.spec["language"].(string); ok {
		lines = append(lines, fmt.Sprintf("  %s %s", HelpStyle.Render("Language:"), TitleStyle.Render(lang)))
	}
	if framework, ok := a.spec["framework"].(string); ok && framework != "" {
		lines = append(lines, fmt.Sprintf("  %s %s", HelpStyle.Render("Framework:"), TitleStyle.Render(framework)))
	}
	if features, ok := a.spec["features"].([]any); ok && len(features) > 0 {
		lines = append(lines, fmt.Sprintf("  %s", HelpStyle.Render("Features:")))
		for _, f := range features {
			if fs, ok := f.(string); ok {
				lines = append(lines, fmt.Sprintf("    %s %s", PhasePassedStyle.Render("•"), fs))
			}
		}
	}

	return SpecBoxStyle.Width(a.width - 2).Render(strings.Join(lines, "\n"))
}

func (a App) viewExecute() string {
	// Autonomous mode uses different rendering
	if a.autonomous {
		return a.viewAutonomous()
	}

	if a.execState == nil {
		return "Initializing..."
	}

	return RenderExecuteView(a.execState, a.width, a.selectedIdx, a.spinner.View())
}

func (a App) viewAutonomous() string {
	var b strings.Builder

	// Header
	header := HeaderStyle.Width(a.width).Render(
		lipgloss.JoinHorizontal(lipgloss.Left,
			Logo(),
			"  ",
			TitleStyle.Render("Autonomous Execution"),
			"  ",
			a.spinner.View(),
		),
	)
	b.WriteString(header)
	b.WriteString("\n\n")

	// Output viewport
	content := strings.Join(a.autonomousOutput, "")
	if content == "" {
		content = "Starting..."
	}

	wrapped := wordwrap.String(content, a.width-4)
	a.execVP.SetContent(wrapped)

	outputBox := ChatBoxStyle.Width(a.width - 2).Height(a.height - 8).Render(a.execVP.View())
	b.WriteString(outputBox)
	b.WriteString("\n")

	// Help
	help := HelpStyle.Render("Esc: cancel • PgUp/PgDn: scroll")
	b.WriteString(help)

	return b.String()
}
