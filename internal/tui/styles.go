package tui

import "github.com/stukennedy/tooey/node"

// Cyberpunk color palette (ANSI 256)
const (
	colPrimary   node.Color = 48  // Neon green
	colSecondary node.Color = 45  // Cyan
	colAccent    node.Color = 201 // Magenta
	colWarning   node.Color = 214 // Amber
	colError     node.Color = 197 // Red-pink
	colMuted     node.Color = 245 // Gray
	colDim       node.Color = 238 // Dark gray
	colBg        node.Color = 0   // Black
	colText      node.Color = 252 // Light text
	colWhite     node.Color = 15
	colBlack     node.Color = 0
	colFileAdd   node.Color = 48  // Green
	colFileMod   node.Color = 214 // Amber
	colFileDel   node.Color = 197 // Red
	colDarkBg    node.Color = 236 // Dark background for bars
)

// Logo renders the kyotee logo text
func Logo() string {
	return "🐺 KYOTEE"
}
