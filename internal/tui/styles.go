package tui

import (
	"strings"

	"github.com/stukennedy/tooey/markdown"
	"github.com/stukennedy/tooey/node"
)

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

// Markdown color schemes for chat roles
var (
	mdAssistant = markdown.ColorScheme{
		Text:     colText,
		Heading:  colPrimary,
		Code:     colSecondary,
		CodeBG:   colDarkBg,
		Quote:    colMuted,
		Link:     colSecondary,
		Bullet:   colPrimary,
		CheckOn:  colPrimary,
		CheckOff: colMuted,
		Rule:     colDim,
	}

	mdUser = markdown.ColorScheme{
		Text:     colSecondary,
		Heading:  colSecondary,
		Code:     colWhite,
		CodeBG:   colDarkBg,
		Quote:    colMuted,
		Link:     colAccent,
		Bullet:   colSecondary,
		CheckOn:  colPrimary,
		CheckOff: colMuted,
		Rule:     colDim,
	}
)

// Logo renders the kyotee logo text
func Logo() string {
	return "🐺 KYOTEE"
}

// WrapText wraps text at the given width, preserving existing newlines.
// Handles markdown code blocks by not wrapping inside them.
func WrapText(text string, width int) string {
	if width <= 0 {
		return text
	}
	
	var result []string
	lines := strings.Split(text, "\n")
	inCodeBlock := false
	
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		
		// Toggle code block state
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			result = append(result, line)
			continue
		}
		
		// Don't wrap code blocks or short lines
		if inCodeBlock || len(line) <= width {
			result = append(result, line)
			continue
		}
		
		// Wrap long lines
		result = append(result, wrapLine(line, width)...)
	}
	
	return strings.Join(result, "\n")
}

// wrapLine wraps a single line at word boundaries.
func wrapLine(line string, width int) []string {
	if len(line) <= width {
		return []string{line}
	}
	
	var lines []string
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{line}
	}
	
	// Preserve leading whitespace
	leadingSpace := ""
	for _, r := range line {
		if r == ' ' || r == '\t' {
			leadingSpace += string(r)
		} else {
			break
		}
	}
	
	current := leadingSpace
	for _, word := range words {
		if current == leadingSpace {
			current += word
		} else if len(current)+1+len(word) <= width {
			current += " " + word
		} else {
			lines = append(lines, current)
			current = leadingSpace + word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	
	return lines
}
