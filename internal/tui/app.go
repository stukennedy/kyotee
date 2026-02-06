package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stukennedy/tooey/app"
	"github.com/stukennedy/tooey/component"
	"github.com/stukennedy/tooey/input"
	"github.com/stukennedy/tooey/markdown"
	"github.com/stukennedy/tooey/node"

	"github.com/stukennedy/kyotee/internal/orchestrator"
	"github.com/stukennedy/kyotee/internal/project"
	"github.com/stukennedy/kyotee/internal/skills"
	"github.com/stukennedy/kyotee/internal/types"
)

// AppMode represents the current mode
type AppMode int

const (
	ModeDiscovery AppMode = iota
	ModeExecute
)

// --- Message types ---

type (
	ResponseMsg          struct{ Content string; Err error }
	ExecuteStartMsg      struct{}
	ExecuteDoneMsg       struct{ Err error }
	PhaseStartMsg        struct{ PhaseID string }
	PhaseEndMsg          struct{ PhaseID string; Passed bool }
	PhaseOutputMsg       struct{ PhaseID string; Line string }
	FileChangeMsg        struct{ PhaseID string; Path string; Action string }
	GateStartMsg         struct{ PhaseID string; GateName string }
	GateEndMsg           struct{ PhaseID string; GateName string; Passed bool }
	NarrationMsg         struct{ Text string }
	PauseMsg             struct{}
	ResumeMsg            struct{}
	CheckpointMsg        struct{ Checkpoint types.Checkpoint }
	CheckpointResolveMsg struct{ Resolution string }
	AutonomousOutput     struct{ Text string }
	AutonomousToolMsg    struct{ Name string; Input any }
	FolderSelectedMsg    struct{ Path string; IsNew bool }
)

// Model is the TUI application state
type Model struct {
	mode      AppMode
	discovery *orchestrator.Discovery
	agentDir  string
	repoRoot  string
	width     int
	height    int

	// Discovery mode
	messages     []chatMessage
	input        component.TextInput
	spec         map[string]any
	specReady    bool
	askingFolder bool
	projectName  string
	waiting      bool
	scrollOffset int

	// Execute mode
	execState   *ExecuteState
	selectedIdx int
	cancelExec  func()
	autonomous  bool

	// Autonomous mode
	autonomousOutput []string
	autoScrollOffset int

	// State persistence
	proj *project.Project

	// Animation
	spinnerIdx int
}

type chatMessage struct {
	role    string
	content string
}

// NewApp creates a new application in discovery mode
func NewApp(agentDir, repoRoot string) *Model {
	discovery := orchestrator.NewDiscovery(agentDir, repoRoot)
	return &Model{
		mode:      ModeDiscovery,
		discovery: discovery,
		agentDir:  agentDir,
		repoRoot:  repoRoot,
		input:     component.NewTextInput("What would you like to build?"),
		messages: []chatMessage{
			{role: "assistant", content: "Hey! I'm Kyotee. What would you like to build today?\n\nTell me about your project and I'll help figure out the details."},
		},
	}
}

// NewAppForProject creates an application with state persistence
func NewAppForProject(agentDir, repoRoot string) *Model {
	discovery := orchestrator.NewDiscovery(agentDir, repoRoot)

	proj, err := project.Open(repoRoot)
	if err != nil {
		return &Model{
			mode:      ModeDiscovery,
			discovery: discovery,
			agentDir:  agentDir,
			repoRoot:  repoRoot,
			input:     component.NewTextInput("What would you like to build?"),
			messages: []chatMessage{
				{role: "assistant", content: "Hey! I'm Kyotee. What would you like to build today?\n\nTell me about your project and I'll help figure out the details."},
			},
		}
	}

	messages := []chatMessage{
		{role: "assistant", content: "Hey! I'm Kyotee. What would you like to build today?\n\nTell me about your project and I'll help figure out the details."},
	}

	if proj.HasConversation() {
		messages = []chatMessage{}
		discoveryHistory := []orchestrator.DiscoveryMessage{}
		for _, m := range proj.Conversation.Messages {
			messages = append(messages, chatMessage{role: m.Role, content: m.Content})
			discoveryHistory = append(discoveryHistory, orchestrator.DiscoveryMessage{Role: m.Role, Content: m.Content})
		}
		discovery.LoadHistory(discoveryHistory)
	}

	var spec map[string]any
	specReady := false
	if proj.HasSpec() {
		spec = proj.Spec.Raw
		specReady = true
	}

	mdl := &Model{
		mode:      ModeDiscovery,
		discovery: discovery,
		agentDir:  agentDir,
		repoRoot:  repoRoot,
		input:     component.NewTextInput("What would you like to build?"),
		messages:  messages,
		proj:      proj,
		spec:      spec,
		specReady: specReady,
	}

	// Auto-resume unfinished job
	if proj.CanResume() {
		mdl.mode = ModeExecute
		mdl.execState = &ExecuteState{
			ProjectName: proj.Spec.ProjectName,
			Task:        proj.Spec.Goal,
			StartTime:   proj.Job.StartedAt,
			Paused:      proj.Job.Status == project.JobStatusPaused,
		}
		for _, p := range proj.Job.Phases {
			mdl.execState.Phases = append(mdl.execState.Phases, PhaseProgress{
				ID:     p.ID,
				Status: p.Status,
			})
		}
	}

	return mdl
}

// NewAppWithJob creates an application to resume an existing job
func NewAppWithJob(agentDir, repoRoot string, jobState *orchestrator.JobState) *Model {
	execState := &ExecuteState{
		ProjectName: jobState.ProjectName,
		Task:        jobState.Task,
		StartTime:   jobState.StartTime,
		RunDir:      filepath.Join(agentDir, "runs", jobState.ID),
		Paused:      jobState.Status == "paused",
	}
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

	return &Model{
		mode:      ModeExecute,
		agentDir:  agentDir,
		repoRoot:  repoRoot,
		spec:      jobState.Spec,
		execState: execState,
	}
}

// Run starts the TUI application
func (m *Model) Run() error {
	mdl := m
	a := &app.App{
		Init: func() interface{} {
			w, h := input.TermSize()
			mdl.width = w
			mdl.height = h
			return mdl
		},
		Update: tuiUpdate,
		View:   tuiView,
	}
	return a.Run(context.Background())
}

// --- Update ---

func tuiUpdate(m interface{}, msg app.Msg) app.UpdateResult {
	mdl := m.(*Model)

	switch msg := msg.(type) {
	case app.ResizeMsg:
		mdl.width, mdl.height = msg.Width, msg.Height

	case app.ScrollMsg:
		if mdl.mode == ModeDiscovery {
			mdl.scrollOffset += msg.Delta
			if mdl.scrollOffset < 0 {
				mdl.scrollOffset = 0
			}
		} else {
			mdl.autoScrollOffset += msg.Delta
			if mdl.autoScrollOffset < 0 {
				mdl.autoScrollOffset = 0
			}
		}

	case app.KeyMsg:
		return handleKey(mdl, msg)

	case component.SpinnerTickMsg:
		mdl.spinnerIdx++
		return app.WithCmd(mdl, component.SpinnerTick(100*time.Millisecond))

	// Discovery responses
	case ResponseMsg:
		mdl.waiting = false
		content := msg.Content
		if msg.Err != nil {
			content = fmt.Sprintf("Error: %v", msg.Err)
		}
		mdl.messages = append(mdl.messages, chatMessage{role: "assistant", content: content})
		if mdl.proj != nil {
			mdl.proj.AddMessage("assistant", content)
		}
		if mdl.discovery.IsSpecReady() {
			mdl.spec = mdl.discovery.GetSpec()
			mdl.specReady = true
			if mdl.proj != nil {
				mdl.proj.SetSpec(mdl.spec)
			}
		}
		mdl.scrollOffset = 0

	// Folder selection
	case FolderSelectedMsg:
		mdl.askingFolder = false
		if msg.IsNew {
			if err := setupProjectFolder(mdl, msg.Path); err != nil {
				mdl.messages = append(mdl.messages, chatMessage{
					role: "assistant", content: fmt.Sprintf("Error creating folder: %v", err),
				})
				return app.NoCmd(mdl)
			}
			mdl.repoRoot = msg.Path
			mdl.messages = append(mdl.messages, chatMessage{
				role: "assistant", content: fmt.Sprintf("Created %s/\nStarting build...", mdl.projectName),
			})
		} else {
			createClaudeMD(msg.Path)
			mdl.messages = append(mdl.messages, chatMessage{
				role: "assistant", content: "Starting build in current folder...",
			})
		}
		return app.WithCmd(mdl, func() app.Msg { return ExecuteStartMsg{} })

	// Execute messages
	case ExecuteStartMsg:
		mdl.mode = ModeExecute
		mdl.autonomous = true
		mdl.autonomousOutput = []string{"🚀 Starting autonomous execution...\n\n"}
		return app.WithSub(mdl, mdl.autonomousSub())

	case PhaseStartMsg:
		if mdl.execState != nil {
			for i := range mdl.execState.Phases {
				if mdl.execState.Phases[i].ID == msg.PhaseID {
					mdl.execState.Phases[i].Status = "running"
					mdl.execState.Phases[i].StartTime = time.Now()
					mdl.execState.CurrentIdx = i
					mdl.execState.Phases[i].Expanded = true
					mdl.selectedIdx = i
					break
				}
			}
		}

	case PhaseEndMsg:
		if mdl.execState != nil {
			for i := range mdl.execState.Phases {
				if mdl.execState.Phases[i].ID == msg.PhaseID {
					if msg.Passed {
						mdl.execState.Phases[i].Status = "passed"
					} else {
						mdl.execState.Phases[i].Status = "failed"
					}
					mdl.execState.Phases[i].EndTime = time.Now()
					break
				}
			}
		}

	case PhaseOutputMsg:
		if mdl.execState != nil {
			for i := range mdl.execState.Phases {
				if mdl.execState.Phases[i].ID == msg.PhaseID {
					mdl.execState.Phases[i].Output = append(mdl.execState.Phases[i].Output, msg.Line)
					break
				}
			}
		}

	case FileChangeMsg:
		if mdl.execState != nil {
			for i := range mdl.execState.Phases {
				if mdl.execState.Phases[i].ID == msg.PhaseID {
					mdl.execState.Phases[i].Files = append(mdl.execState.Phases[i].Files, FileChange{Path: msg.Path, Action: msg.Action})
					break
				}
			}
		}

	case GateStartMsg:
		if mdl.execState != nil {
			for i := range mdl.execState.Phases {
				if mdl.execState.Phases[i].ID == msg.PhaseID {
					mdl.execState.Phases[i].Gates = append(mdl.execState.Phases[i].Gates, GateProgress{Name: msg.GateName, Status: "running"})
					break
				}
			}
		}

	case GateEndMsg:
		if mdl.execState != nil {
			for i := range mdl.execState.Phases {
				if mdl.execState.Phases[i].ID == msg.PhaseID {
					for j := range mdl.execState.Phases[i].Gates {
						if mdl.execState.Phases[i].Gates[j].Name == msg.GateName {
							if msg.Passed {
								mdl.execState.Phases[i].Gates[j].Status = "passed"
							} else {
								mdl.execState.Phases[i].Gates[j].Status = "failed"
							}
							break
						}
					}
					break
				}
			}
		}

	case NarrationMsg:
		if mdl.execState != nil {
			idx := mdl.execState.CurrentIdx
			if idx >= 0 && idx < len(mdl.execState.Phases) {
				mdl.execState.Phases[idx].Output = append(mdl.execState.Phases[idx].Output, "💭 "+msg.Text)
			}
		}

	case CheckpointMsg:
		if mdl.execState != nil {
			mdl.execState.Checkpoint = &CheckpointState{
				Active:  true,
				Type:    string(msg.Checkpoint.Type),
				Message: msg.Checkpoint.Message,
				Options: msg.Checkpoint.Options,
			}
			mdl.execState.Paused = true
		}

	case CheckpointResolveMsg:
		if mdl.execState != nil {
			mdl.execState.Checkpoint = nil
			mdl.execState.Paused = false
		}

	case ExecuteDoneMsg:
		if mdl.execState != nil {
			mdl.execState.Error = msg.Err
		}

	case PauseMsg:
		if mdl.execState != nil && !mdl.execState.Paused {
			mdl.execState.Paused = true
			if mdl.cancelExec != nil {
				mdl.cancelExec()
			}
		}

	case ResumeMsg:
		if mdl.execState != nil && mdl.execState.Paused {
			mdl.execState.Paused = false
			return app.WithCmd(mdl, mdl.runExecuteCmd())
		}

	case AutonomousOutput:
		mdl.autonomousOutput = append(mdl.autonomousOutput, msg.Text)
		mdl.autoScrollOffset = 0

	case AutonomousToolMsg:
		mdl.autonomousOutput = append(mdl.autonomousOutput, fmt.Sprintf("🔧 %s\n", msg.Name))
	}

	return app.NoCmd(mdl)
}

func handleKey(mdl *Model, msg app.KeyMsg) app.UpdateResult {
	switch msg.Key.Type {
	case input.Escape:
		if mdl.mode == ModeExecute {
			if mdl.autonomous {
				if mdl.cancelExec != nil {
					mdl.cancelExec()
				}
				return app.UpdateResult{Model: nil}
			}
			if mdl.execState != nil && (mdl.execState.Error != nil || allPhasesDone(mdl)) {
				return app.UpdateResult{Model: nil}
			}
			return app.WithCmd(mdl, func() app.Msg { return PauseMsg{} })
		}
		return app.UpdateResult{Model: nil}

	case input.PageUp:
		if mdl.mode == ModeDiscovery {
			mdl.scrollOffset += 5
		} else {
			mdl.autoScrollOffset += 5
		}
		return app.NoCmd(mdl)

	case input.PageDown:
		if mdl.mode == ModeDiscovery {
			mdl.scrollOffset -= 5
			if mdl.scrollOffset < 0 {
				mdl.scrollOffset = 0
			}
		} else {
			mdl.autoScrollOffset -= 5
			if mdl.autoScrollOffset < 0 {
				mdl.autoScrollOffset = 0
			}
		}
		return app.NoCmd(mdl)
	}

	if mdl.mode == ModeDiscovery {
		return handleDiscoveryKey(mdl, msg)
	}
	return handleExecuteKey(mdl, msg)
}

func handleDiscoveryKey(mdl *Model, msg app.KeyMsg) app.UpdateResult {
	switch msg.Key.Type {
	case input.Enter:
		inputVal := strings.TrimSpace(mdl.input.Value)
		inputLower := strings.ToLower(inputVal)

		// Folder selection
		if mdl.askingFolder {
			_, newInput := mdl.input.Submit()
			mdl.input = newInput
			switch inputLower {
			case "1", "here", "current":
				return app.WithCmd(mdl, func() app.Msg {
					return FolderSelectedMsg{Path: mdl.repoRoot, IsNew: false}
				})
			case "2", "new", "child":
				newPath := filepath.Join(mdl.repoRoot, mdl.projectName)
				return app.WithCmd(mdl, func() app.Msg {
					return FolderSelectedMsg{Path: newPath, IsNew: true}
				})
			default:
				mdl.messages = append(mdl.messages, chatMessage{
					role: "assistant", content: "Please enter 1 (current folder) or 2 (new folder):",
				})
				return app.NoCmd(mdl)
			}
		}

		// Spec approved
		if mdl.specReady && (inputLower == "yes" || inputLower == "y") {
			_, newInput := mdl.input.Submit()
			mdl.input = newInput
			mdl.askingFolder = true
			mdl.projectName = getProjectNameFromSpec(mdl.spec)
			mdl.messages = append(mdl.messages, chatMessage{
				role: "assistant",
				content: fmt.Sprintf("Where should I create the project?\n\n  1) Here (current folder: %s)\n  2) New folder: %s/\n\nEnter 1 or 2:", filepath.Base(mdl.repoRoot), mdl.projectName),
			})
			return app.NoCmd(mdl)
		}

		// Send message
		if !mdl.waiting && inputVal != "" {
			text, newInput := mdl.input.Submit()
			mdl.input = newInput
			mdl.messages = append(mdl.messages, chatMessage{role: "user", content: text})
			if mdl.proj != nil {
				mdl.proj.AddMessage("user", text)
			}
			mdl.waiting = true
			mdl.scrollOffset = 0
			return app.WithCmd(mdl, sendMessageCmd(mdl, text), component.SpinnerTick(100*time.Millisecond))
		}

	default:
		if !mdl.waiting {
			mdl.input = mdl.input.Update(msg.Key)
		}
	}

	return app.NoCmd(mdl)
}

func handleExecuteKey(mdl *Model, msg app.KeyMsg) app.UpdateResult {
	switch msg.Key.Type {
	case input.Up:
		if mdl.selectedIdx > 0 {
			mdl.selectedIdx--
		}
	case input.Down:
		if mdl.execState != nil && mdl.selectedIdx < len(mdl.execState.Phases)-1 {
			mdl.selectedIdx++
		}
	case input.Enter:
		if mdl.execState != nil && mdl.selectedIdx < len(mdl.execState.Phases) {
			mdl.execState.Phases[mdl.selectedIdx].Expanded = !mdl.execState.Phases[mdl.selectedIdx].Expanded
		}
	case input.RuneKey:
		switch msg.Key.Rune {
		case 'p':
			if mdl.execState != nil && !mdl.execState.Paused {
				return app.WithCmd(mdl, func() app.Msg { return PauseMsg{} })
			}
		case 'r':
			if mdl.execState != nil && mdl.execState.Paused {
				return app.WithCmd(mdl, func() app.Msg { return ResumeMsg{} })
			}
		case 'q':
			return app.UpdateResult{Model: nil}
		case 'j':
			mdl.autoScrollOffset -= 3
			if mdl.autoScrollOffset < 0 {
				mdl.autoScrollOffset = 0
			}
		case 'k':
			mdl.autoScrollOffset += 3
		}
	}
	return app.NoCmd(mdl)
}

// --- View ---

func tuiView(m interface{}, focused string) node.Node {
	mdl := m.(*Model)

	switch mdl.mode {
	case ModeDiscovery:
		return viewDiscovery(mdl)
	case ModeExecute:
		if mdl.autonomous {
			return viewAutonomous(mdl)
		}
		return viewExecute(mdl)
	}
	return viewDiscovery(mdl)
}

func viewDiscovery(mdl *Model) node.Node {
	w := mdl.width

	// Header bar
	modeText := "Discovery"
	if mdl.proj != nil && mdl.proj.HasConversation() && len(mdl.messages) > 1 {
		modeText = "Discovery (resumed)"
	}
	header := node.Bar(fmt.Sprintf(" %s  %s", Logo(), modeText), colPrimary, colDarkBg, node.Bold)

	// Chat messages rendered with markdown
	var chatNodes []node.Node
	for _, msg := range mdl.messages {
		chatNodes = append(chatNodes, node.Text(""))
		if msg.role == "user" {
			label := node.TextStyled("  You: ", colSecondary, 0, node.Bold)
			chatNodes = append(chatNodes, label)
			wrapped := WrapText(msg.content, w-8)
			rendered := markdown.RenderWithColors(wrapped, w-6, mdUser)
			for _, n := range rendered {
				chatNodes = append(chatNodes, node.Indent(6, n))
			}
		} else {
			label := node.TextStyled("  🐺 ", colPrimary, 0, 0)
			chatNodes = append(chatNodes, label)
			wrapped := WrapText(msg.content, w-7)
			rendered := markdown.RenderWithColors(wrapped, w-5, mdAssistant)
			for _, n := range rendered {
				chatNodes = append(chatNodes, node.Indent(5, n))
			}
		}
	}

	if mdl.waiting {
		chatNodes = append(chatNodes,
			node.Text(""),
			node.Indent(2, node.Row(
				node.TextStyled("🐺 ", colPrimary, 0, 0),
				component.Spinner("thinking...", mdl.spinnerIdx, component.SpinnerDots, colMuted),
			)),
		)
	}

	conversation := node.Column(chatNodes...).WithFlex(1).WithScrollToBottom().WithScrollOffset(mdl.scrollOffset)

	// Spec box
	var specNode node.Node
	if mdl.specReady {
		specNode = renderSpecNode(mdl.spec)
	} else {
		specNode = node.Text("")
	}

	// Input area
	inputLine := mdl.input.Render("  > ", colWhite, 0, w)

	// Help bar
	helpText := " Enter: send • Shift+Enter: newline • PgUp/PgDn: scroll • Esc: quit"
	if mdl.specReady {
		helpText = " Type 'yes' to build •" + helpText
	}
	help := node.Bar(helpText, colMuted, colDarkBg, 0)

	return node.Column(
		header,
		node.Separator(w),
		conversation,
		specNode,
		node.Separator(w),
		inputLine,
		node.Separator(w),
		help,
	)
}

func renderSpecNode(spec map[string]any) node.Node {
	if spec == nil {
		return node.Text("")
	}

	var lines []node.Node
	lines = append(lines, node.TextStyled("  📋 READY TO BUILD", colAccent, 0, node.Bold))

	if goal, ok := spec["goal"].(string); ok {
		lines = append(lines, node.TextStyled("  "+goal, colPrimary, 0, 0))
	}
	if pn, ok := spec["project_name"].(string); ok && pn != "" && pn != "." {
		lines = append(lines, node.Row(
			node.TextStyled("  Project: ", colMuted, 0, 0),
			node.TextStyled(pn+"/", colSecondary, 0, node.Bold),
		))
	}
	if lang, ok := spec["language"].(string); ok {
		lines = append(lines, node.Row(
			node.TextStyled("  Language: ", colMuted, 0, 0),
			node.TextStyled(lang, colSecondary, 0, node.Bold),
		))
	}
	if fw, ok := spec["framework"].(string); ok && fw != "" {
		lines = append(lines, node.Row(
			node.TextStyled("  Framework: ", colMuted, 0, 0),
			node.TextStyled(fw, colSecondary, 0, node.Bold),
		))
	}
	if features, ok := spec["features"].([]any); ok && len(features) > 0 {
		lines = append(lines, node.TextStyled("  Features:", colMuted, 0, 0))
		for _, f := range features {
			if fs, ok := f.(string); ok {
				lines = append(lines, node.TextStyled("    • "+fs, colPrimary, 0, 0))
			}
		}
	}

	inner := node.Column(lines...)
	return node.Box(node.BorderRounded, inner)
}

func viewExecute(mdl *Model) node.Node {
	if mdl.execState == nil {
		return node.TextStyled("  Initializing...", colMuted, 0, 0)
	}
	frame := component.SpinnerFrames(component.SpinnerDots)[mdl.spinnerIdx%len(component.SpinnerFrames(component.SpinnerDots))]
	return RenderExecuteView(mdl.execState, mdl.width, mdl.selectedIdx, frame)
}

func viewAutonomous(mdl *Model) node.Node {
	w := mdl.width

	// Header bar with spinner
	header := node.Bar(
		fmt.Sprintf(" %s  Autonomous Execution", Logo()),
		colPrimary, colDarkBg, node.Bold,
	)

	// Spinner status line
	statusLine := node.Indent(2, component.Spinner("running...", mdl.spinnerIdx, component.SpinnerDots, colWarning))

	// Output rendered as markdown (wrap text first)
	content := strings.Join(mdl.autonomousOutput, "")
	if content == "" {
		content = "Starting..."
	}
	content = WrapText(content, w-4) // Wrap before markdown parsing
	outputNodes := markdown.RenderWithColors(content, w-2, mdAssistant)
	var indented []node.Node
	for _, n := range outputNodes {
		indented = append(indented, node.Indent(2, n))
	}

	output := node.Column(indented...).WithFlex(1).WithScrollToBottom().WithScrollOffset(mdl.autoScrollOffset)

	// Footer bar
	footer := node.Bar(" Esc: cancel • PgUp/PgDn: scroll • j/k: scroll", colMuted, colDarkBg, 0)

	return node.Column(
		header,
		node.Separator(w),
		statusLine,
		node.Text(""),
		output,
		node.Separator(w),
		footer,
	)
}

// --- Commands ---

func sendMessageCmd(mdl *Model, msg string) app.Cmd {
	return func() app.Msg {
		response, err := mdl.discovery.SendMessage(msg)
		return ResponseMsg{Content: response, Err: err}
	}
}

func (mdl *Model) runExecuteCmd() app.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	mdl.cancelExec = cancel

	return func() app.Msg {
		task := mdl.buildTaskFromSpec()
		proj := mdl.proj
		if proj == nil {
			return ExecuteDoneMsg{Err: fmt.Errorf("no project initialized")}
		}
		if mdl.spec != nil {
			proj.SetSpec(mdl.spec)
		}

		// Generate AGENTS.md for non-autonomous mode too
		if mdl.spec != nil {
			var skill *skills.Skill
			if mdl.discovery != nil && mdl.discovery.ActiveSkill != nil {
				skill = mdl.discovery.ActiveSkill
			} else if mdl.discovery != nil && mdl.discovery.SkillRegistry != nil {
				skill = orchestrator.MatchSkillFromSpec(mdl.spec, mdl.discovery.SkillRegistry)
			}
			orchestrator.GenerateAgentsFile(mdl.spec, skill, mdl.repoRoot)
		}

		proj.StartJob()

		spec, err := orchestrator.LoadSpecWithOverrides(mdl.agentDir, mdl.spec)
		if err != nil {
			proj.SetJobStatus(project.JobStatusFailed, err.Error())
			return ExecuteDoneMsg{Err: err}
		}

		runDir := filepath.Join(proj.KyoteeDir, project.PhasesDir)
		engine, err := orchestrator.NewEngineWithRunDir(spec, task, mdl.repoRoot, mdl.agentDir, runDir)
		if err != nil {
			proj.SetJobStatus(project.JobStatusFailed, err.Error())
			return ExecuteDoneMsg{Err: err}
		}

		if mdl.execState != nil {
			mdl.execState.RunDir = engine.RunDir
		}

		if err := engine.RunWithContext(ctx); err != nil {
			if errors.Is(err, orchestrator.ErrPaused) {
				proj.SetJobStatus(project.JobStatusPaused, "")
				return ExecuteDoneMsg{Err: nil}
			}
			if cpErr, ok := err.(*types.ErrCheckpoint); ok {
				proj.SetJobStatus("checkpoint", "")
				return CheckpointMsg{Checkpoint: cpErr.Checkpoint}
			}
			proj.SetJobStatus(project.JobStatusFailed, err.Error())
			return ExecuteDoneMsg{Err: err}
		}

		proj.SetJobStatus(project.JobStatusCompleted, "")
		return ExecuteDoneMsg{}
	}
}

// autonomousSub returns a Sub that streams autonomous execution output
// back to the app via the send callback instead of mutating model directly.
func (mdl *Model) autonomousSub() app.Sub {
	ctx, cancel := context.WithCancel(context.Background())
	mdl.cancelExec = cancel

	return func(send func(app.Msg)) app.Msg {
		// Start spinner ticks
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
					send(component.SpinnerTickMsg{})
				}
			}
		}()

		task := mdl.buildTaskFromSpec()

		// Deterministic skill matching from spec + generate AGENTS.md
		var skill *skills.Skill
		if mdl.discovery != nil && mdl.discovery.ActiveSkill != nil {
			skill = mdl.discovery.ActiveSkill
		} else if mdl.discovery != nil && mdl.discovery.SkillRegistry != nil && mdl.spec != nil {
			skill = orchestrator.MatchSkillFromSpec(mdl.spec, mdl.discovery.SkillRegistry)
		}

		// Generate AGENTS.md
		if mdl.spec != nil {
			if _, err := orchestrator.GenerateAgentsFile(mdl.spec, skill, mdl.repoRoot); err != nil {
				send(AutonomousOutput{Text: fmt.Sprintf("⚠ Failed to generate AGENTS.md: %v\n", err)})
			} else {
				send(AutonomousOutput{Text: "📄 Generated .kyotee/AGENTS.md (static context)\n"})
			}
		}

		skillContent := ""
		if skill != nil {
			skillContent = skill.ToPromptContext()
		}

		engine := orchestrator.NewAutonomousEngine(mdl.spec, task, mdl.repoRoot, mdl.agentDir)
		engine.SkillContent = skillContent

		// Stream output via send callback — safe, goes through the message loop
		engine.OnOutput = func(text string) {
			send(AutonomousOutput{Text: text})
		}

		engine.OnPhase = func(phase, status string) {
			send(AutonomousOutput{Text: fmt.Sprintf("\n[%s] %s\n", phase, status)})
		}

		engine.OnTool = func(name string, input any) {
			send(AutonomousToolMsg{Name: name, Input: input})
		}

		if err := engine.Run(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return ExecuteDoneMsg{Err: nil}
			}
			return ExecuteDoneMsg{Err: err}
		}

		return ExecuteDoneMsg{}
	}
}

// --- Helpers ---

func allPhasesDone(mdl *Model) bool {
	if mdl.execState == nil {
		return false
	}
	for _, p := range mdl.execState.Phases {
		if p.Status == "pending" || p.Status == "running" {
			return false
		}
	}
	return true
}

func (mdl *Model) buildTaskFromSpec() string {
	if mdl.spec == nil {
		return "Build the project"
	}
	var parts []string
	if goal, ok := mdl.spec["goal"].(string); ok {
		parts = append(parts, goal)
	}
	if features, ok := mdl.spec["features"].([]any); ok {
		for _, f := range features {
			if fs, ok := f.(string); ok {
				parts = append(parts, "- "+fs)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func getProjectNameFromSpec(spec map[string]any) string {
	if spec == nil {
		return "my-project"
	}
	if name, ok := spec["project_name"].(string); ok && name != "" {
		return sanitizeFolderName(name)
	}
	if name, ok := spec["name"].(string); ok && name != "" {
		return sanitizeFolderName(name)
	}
	if goal, ok := spec["goal"].(string); ok && goal != "" {
		words := strings.Fields(goal)
		if len(words) > 3 {
			words = words[:3]
		}
		return sanitizeFolderName(strings.ToLower(strings.Join(words, "-")))
	}
	return "my-project"
}

func sanitizeFolderName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	return strings.Trim(result.String(), "-")
}

func setupProjectFolder(mdl *Model, newPath string) error {
	if err := os.MkdirAll(newPath, 0755); err != nil {
		return fmt.Errorf("create folder: %w", err)
	}

	oldKyotee := filepath.Join(mdl.repoRoot, ".kyotee")
	newKyotee := filepath.Join(newPath, ".kyotee")

	if _, err := os.Stat(oldKyotee); err == nil {
		if err := os.Rename(oldKyotee, newKyotee); err != nil {
			if err := copyDir(oldKyotee, newKyotee); err != nil {
				return fmt.Errorf("move .kyotee: %w", err)
			}
			os.RemoveAll(oldKyotee)
		}
	}

	if mdl.proj != nil {
		newProj, err := project.Open(newPath)
		if err == nil {
			newProj.SetSpec(mdl.spec)
			mdl.proj = newProj
		}
	}

	createClaudeMD(newPath)
	return nil
}

func createClaudeMD(projectPath string) {
	claudeMD := `# Kyotee Project

This project was scaffolded by [Kyotee](https://github.com/stukennedy/kyotee), an autonomous development agent.

## Project Resources

The ` + "`.kyotee/`" + ` folder contains:

- **` + "`spec.json`" + `** - The approved specification
- **` + "`conversation.json`" + `** - Discovery conversation history

## Resuming Work

` + "```bash" + `
kyotee --continue
` + "```" + `
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
