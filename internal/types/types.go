package types

// Phase represents an orchestration phase
type Phase struct {
	ID         string `toml:"id"`
	SchemaPath string `toml:"required_outputs_schema"`
}

// Spec represents the full TOML specification
type Spec struct {
	Version  string            `toml:"version"`
	Name     string            `toml:"name"`
	Meta     Meta              `toml:"meta"`
	Persona  Persona           `toml:"persona"`
	Limits   Limits            `toml:"limits"`
	Policies Policies          `toml:"policies"`
	Gates    Gates             `toml:"gates"`
	Commands map[string]string `toml:"commands"`
	Phases   []Phase           `toml:"phases"`
}

type Meta struct {
	Owner    string `toml:"owner"`
	Timezone string `toml:"timezone"`
}

type Persona struct {
	ModeName       string `toml:"mode_name"`
	NarrationStyle string `toml:"narration_style"`
	ControlStyle   string `toml:"control_style"`
}

type Limits struct {
	MaxTotalIterations int `toml:"max_total_iterations"`
	MaxPhaseIterations int `toml:"max_phase_iterations"`
	MaxLLMTokens       int `toml:"max_llm_tokens"`
}

type Policies struct {
	RequireEvidence    bool     `toml:"require_evidence"`
	ForbidNetwork      bool     `toml:"forbid_network"`
	ForbidSecretAccess bool     `toml:"forbid_secret_access"`
	AllowFileWrites    bool     `toml:"allow_file_writes"`
	AllowedWritePaths  []string `toml:"allowed_write_paths"`
	ForbidWritePaths   []string `toml:"forbid_write_paths"`
	FailOnTodo         bool     `toml:"fail_on_todo"`
}

type Gates struct {
	RequiredChecks []string `toml:"required_checks"`
}

// PhaseStatus tracks phase execution state
type PhaseStatus int

const (
	PhasePending PhaseStatus = iota
	PhaseRunning
	PhasePassed
	PhaseFailed
)

func (s PhaseStatus) String() string {
	switch s {
	case PhasePending:
		return "pending"
	case PhaseRunning:
		return "running"
	case PhasePassed:
		return "passed"
	case PhaseFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// PhaseState holds runtime state for a phase
type PhaseState struct {
	Phase       Phase
	Status      PhaseStatus
	Iteration   int
	Output      string
	Narration   string
	Error       error
	ControlJSON map[string]any
	SubPlan     *SubPlanState // Non-nil during chunked implement execution
}

// RunState holds the entire run's state
type RunState struct {
	Task            string
	Spec            *Spec
	Phases          []PhaseState
	CurrentPhase    int
	TotalIterations int
	RunDir          string
}

// SubPlanState tracks progress of a chunked sub-plan within the implement phase
type SubPlanState struct {
	ChunkIndex     int          // Which chunk we're on (0-based)
	TotalChunks    int          // Total number of chunks
	StepGroups     [][]PlanStep // Steps grouped into chunks
	CompletedFiles []string     // Files created/modified so far
	CumulativeDiff string       // Git diff after each chunk
	Checkpoint     *Checkpoint  // Non-nil if waiting for human input
	CheckpointResolutions []string // Past checkpoint resolutions for context
}

// PlanStep represents a single step from the plan phase output
type PlanStep struct {
	ID            string   `json:"id"`
	Goal          string   `json:"goal"`
	Actions       []string `json:"actions"`
	ExpectedFiles []string `json:"expected_files"`
	Checks        []string `json:"checks"`
}

// CheckpointType represents the kind of checkpoint
type CheckpointType string

const (
	CheckpointVerify   CheckpointType = "human-verify"
	CheckpointDecision CheckpointType = "decision"
	CheckpointAction   CheckpointType = "human-action"
)

// Checkpoint represents a mid-build pause requesting human input
type Checkpoint struct {
	Type       CheckpointType `json:"type"`
	Message    string         `json:"message"`
	Options    []string       `json:"options,omitempty"`    // For decision type
	Resolution string         `json:"resolution,omitempty"` // Human's response
	ResolvedAt string         `json:"resolved_at,omitempty"`
}

// ErrCheckpoint is returned when a checkpoint is hit during execution
type ErrCheckpoint struct {
	Checkpoint Checkpoint
}

func (e *ErrCheckpoint) Error() string {
	return "checkpoint: " + string(e.Checkpoint.Type) + ": " + e.Checkpoint.Message
}

// Evidence from phase outputs
type Evidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Note string `json:"note"`
}

// GateResult from verification
type GateResult struct {
	Name      string `json:"name"`
	Command   string `json:"command"`
	ExitCode  int    `json:"exit_code"`
	OutputRef string `json:"output_ref"`
	Passed    bool
}
