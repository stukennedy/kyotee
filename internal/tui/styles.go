package tui

import "github.com/charmbracelet/lipgloss"

// Cyberpunk color palette - vibrant neon colors
var (
	Primary    = lipgloss.Color("#00ff9f") // Neon green - success, active
	Secondary  = lipgloss.Color("#00d4ff") // Cyan - info, headers
	Accent     = lipgloss.Color("#ff00ff") // Magenta - highlights
	Warning    = lipgloss.Color("#ffaa00") // Amber - warnings, running
	Error      = lipgloss.Color("#ff3366") // Red-pink - errors
	Muted      = lipgloss.Color("#6a6a7a") // Gray - inactive (brighter)
	Dim        = lipgloss.Color("#3a3a4a") // Darker gray
	Bg         = lipgloss.Color("#0a0a0f") // Near-black background
	Text       = lipgloss.Color("#e0e0e0") // Light text
	FileAdd    = lipgloss.Color("#00ff9f") // Green for new files
	FileModify = lipgloss.Color("#ffaa00") // Amber for modified
	FileDelete = lipgloss.Color("#ff3366") // Red for deleted
)

// Styles
var (
	// Header
	HeaderStyle = lipgloss.NewStyle().
			Foreground(Primary).
			Bold(true).
			Padding(0, 1)

	TitleStyle = lipgloss.NewStyle().
			Foreground(Secondary).
			Bold(true)

	// Phase panel
	PhasePanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Dim).
			Padding(1, 2)

	PhaseActiveStyle = lipgloss.NewStyle().
				Foreground(Warning).
				Bold(true)

	PhasePendingStyle = lipgloss.NewStyle().
				Foreground(Muted)

	PhasePassedStyle = lipgloss.NewStyle().
				Foreground(Primary).
				Bold(true)

	PhaseFailedStyle = lipgloss.NewStyle().
				Foreground(Error).
				Bold(true)

	// Selection highlight
	SelectedStyle = lipgloss.NewStyle().
			Foreground(Secondary).
			Bold(true)

	// File change styles
	FileAddStyle = lipgloss.NewStyle().
			Foreground(FileAdd)

	FileModifyStyle = lipgloss.NewStyle().
			Foreground(FileModify)

	FileDeleteStyle = lipgloss.NewStyle().
			Foreground(FileDelete)

	// Output viewport
	OutputPanelStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(Dim).
				Padding(1, 2)

	OutputTextStyle = lipgloss.NewStyle().
			Foreground(Text)

	// Narration bar
	NarrationStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Accent).
			Foreground(Accent).
			Padding(0, 2).
			Italic(true)

	// Status bar
	StatusBarStyle = lipgloss.NewStyle().
			Foreground(Muted).
			Padding(0, 1)

	IterCountStyle = lipgloss.NewStyle().
			Foreground(Warning)

	// Spinner
	SpinnerStyle = lipgloss.NewStyle().
			Foreground(Primary)

	// Warning/Running style
	WarningStyle = lipgloss.NewStyle().
			Foreground(Warning).
			Bold(true)

	// Symbols
	SymbolPending = PhasePendingStyle.Render("○")
	SymbolActive  = PhaseActiveStyle.Render("●")
	SymbolPassed  = PhasePassedStyle.Render("✓")
	SymbolFailed  = PhaseFailedStyle.Render("✗")

	// Chat styles
	ChatBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Dim).
			Padding(1, 2)

	InputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Secondary).
			Padding(0, 1)

	UserMsgStyle = lipgloss.NewStyle().
			Foreground(Secondary).
			Bold(true)

	UserContentStyle = lipgloss.NewStyle().
				Foreground(Text)

	AssistantMsgStyle = lipgloss.NewStyle().
				Foreground(Primary).
				Bold(true)

	AssistantContentStyle = lipgloss.NewStyle().
				Foreground(Text)

	ThinkingStyle = lipgloss.NewStyle().
			Foreground(Muted).
			Italic(true)

	SpecBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(Accent).
			Padding(0, 2)

	SpecTitleStyle = lipgloss.NewStyle().
			Foreground(Accent).
			Bold(true)

	HelpStyle = lipgloss.NewStyle().
			Foreground(Muted).
			Padding(0, 1)
)

// Logo renders the kyotee logo
func Logo() string {
	return HeaderStyle.Render("🐺 KYOTEE")
}
