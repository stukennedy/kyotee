package tui

import (
	"fmt"
	"strings"

	"github.com/stukennedy/tooey/node"
)

// ANSI-256 palette.
const (
	cAccent  = 39  // blue
	cOK      = 42  // green
	cWarn    = 220 // yellow
	cHot     = 208 // orange
	cDanger  = 196 // red
	cDim     = 245
	cDiverge = 213 // pink — divergent brain
	cConv    = 117 // cyan — convergent brain
)

func View(mi interface{}, focused string) node.Node {
	m := mi.(*Model)

	switch m.Active {
	case overlayConfig:
		return m.viewConfig()
	case overlayResume:
		return m.viewResume()
	case overlayOverride:
		return m.viewOverride()
	}

	return node.Column(
		m.viewHeader(),
		node.Row(
			node.Box(node.BorderRounded, node.Column(
				node.TextStyled(" Prompt ", cAccent, 0, node.Bold),
				m.Input.Render("> ", 0, 0, 70),
			)).WithFlex(3),
			node.Box(node.BorderRounded, m.viewRouting()).WithFlex(2),
		),
		node.Box(node.BorderRounded, m.viewCenter()).WithFlex(1),
		node.Row(
			node.Box(node.BorderRounded, m.viewThinking()).WithFlex(1),
			node.Box(node.BorderRounded, m.viewLog()).WithFlex(2),
		).WithSize(0, 9),
		m.viewFooter(),
	)
}

func (m *Model) viewHeader() node.Node {
	conn := node.TextStyled(" ● offline ", cDanger, 0, 0)
	if m.Connected {
		conn = node.TextStyled(" ● live ", cOK, 0, 0)
	}
	return node.Row(
		node.TextStyled(" Kyotee Harness ", cAccent, 0, node.Bold),
		node.TextStyled(" "+m.TaskID+" ", cDim, 0, 0),
		node.Spacer(),
		conn,
		m.costMeter(),
	)
}

// costMeter is always visible and colour-shifts at 50/80/95% (spec 08 §3).
func (m *Model) costMeter() node.Node {
	if m.LimitUSD <= 0 {
		return node.TextStyled(" cost: $0.00 ", cDim, 0, 0)
	}
	pct := m.SpentUSD / m.LimitUSD
	color := node.Color(cOK)
	switch {
	case pct >= 0.95:
		color = cDanger
	case pct >= 0.80:
		color = cHot
	case pct >= 0.50:
		color = cWarn
	}
	filled := int(pct*8 + 0.5)
	if filled > 8 {
		filled = 8
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", 8-filled)
	return node.TextStyled(fmt.Sprintf(" cost: $%.2f / $%.2f [%s] ", m.SpentUSD, m.LimitUSD, bar), color, 0, node.Bold)
}

func (m *Model) viewRouting() node.Node {
	class := "—"
	if m.Class != nil {
		domain, _ := m.Class["domain"].(string)
		complexity, _ := m.Class["complexity"].(string)
		toolNeed, _ := m.Class["tool_need"].(string)
		class = fmt.Sprintf("%s / %s / tools:%s", domain, complexity, toolNeed)
	}
	ov := ""
	if m.Override.Strategy != "" || m.Override.Thinking != "" || m.Override.MaxCostUSD > 0 {
		ov = fmt.Sprintf("override→ %s %s $%.0f", m.Override.Strategy, m.Override.Thinking, m.Override.MaxCostUSD)
	}
	return node.Column(
		node.TextStyled(" Routing ", cAccent, 0, node.Bold),
		node.Text(" class: "+class),
		node.Text(" strategy: "+orDash(m.Strategy)+"   stage: "+orDash(m.Stage)),
		node.Text(" pipeline: "+orDash(strings.Join(m.Pipeline, "→"))),
		node.TextStyled(" "+ov, cWarn, 0, 0),
	)
}

// viewCenter is strategy-dependent (spec 08 §4).
func (m *Model) viewCenter() node.Node {
	switch m.Strategy {
	case "council":
		return m.viewCouncil()
	case "twobrain":
		return m.viewTwoBrain()
	}
	return m.viewSolo()
}

func (m *Model) viewSolo() node.Node {
	title := " Answer "
	body := m.Final
	if body == "" {
		body = "…working…"
		title = " Working "
	}
	return node.Column(
		node.TextStyled(title, cAccent, 0, node.Bold),
		wrapText(body, 110),
	).WithScrollToBottom()
}

func (m *Model) viewTwoBrain() node.Node {
	var left, right []node.Node
	left = append(left, node.TextStyled(" divergent ", cDiverge, 0, node.Bold))
	right = append(right, node.TextStyled(" convergent ", cConv, 0, node.Bold))
	for _, t := range m.Brains {
		line := node.Column(
			node.TextStyled(fmt.Sprintf(" round %d ", t.Round), cDim, 0, 0),
			wrapText(t.Text, 55),
		)
		if t.Role == "divergent" {
			left = append(left, line)
		} else {
			right = append(right, line)
		}
	}
	cols := node.Row(
		node.Column(left...).WithFlex(1).WithScrollToBottom(),
		node.Column(right...).WithFlex(1).WithScrollToBottom(),
	).WithFlex(1)

	if m.Referee != "" || m.Final != "" {
		text := m.Referee
		if m.Final != "" {
			text = m.Final
		}
		return node.Column(cols,
			node.TextStyled(" referee ", cOK, 0, node.Bold),
			wrapText(text, 110),
		)
	}
	return node.Column(cols)
}

func (m *Model) viewCouncil() node.Node {
	if len(m.Members) == 0 {
		return node.Column(node.TextStyled(" Council convening… ", cDim, 0, 0))
	}
	panes := make([]node.Node, 0, len(m.Members))
	for _, name := range m.Members {
		mv := m.Council[name]
		voteLine := ""
		if mv.Choice != "" {
			voteLine = fmt.Sprintf("vote: %s (%.2f)", mv.Choice, mv.Confidence)
		}
		panes = append(panes, node.Box(node.BorderSingle, node.Column(
			node.TextStyled(" "+name+" ", cAccent, 0, node.Bold),
			node.TextStyled(" "+voteLine, cWarn, 0, 0),
			wrapText(mv.Position, 36),
		).WithScrollToBottom()).WithFlex(1))
	}
	rows := []node.Node{
		node.Row(panes...).WithFlex(1),
		node.TextStyled(" "+orDash(m.Consensus), cOK, 0, node.Bold),
	}
	if m.Synthesis != "" {
		rows = append(rows,
			node.TextStyled(" synthesis ", cOK, 0, node.Bold),
			wrapText(m.Synthesis, 110))
	}
	return node.Column(rows...)
}

func (m *Model) viewThinking() node.Node {
	lines := []node.Node{
		node.TextStyled(" Thinking ", cAccent, 0, node.Bold),
		node.Text(" mode: " + orDash(m.ThinkMode)),
		node.Text(" tool-check: " + orDash(m.ToolCheck)),
	}
	for i, tc := range m.ToolCalls {
		if i >= 3 {
			lines = append(lines, node.TextStyled(fmt.Sprintf("  … %d more", len(m.ToolCalls)-3), cDim, 0, 0))
			break
		}
		lines = append(lines, node.TextStyled("  ⚒ "+tc, cDim, 0, 0))
	}
	return node.Column(lines...)
}

func (m *Model) viewLog() node.Node {
	lines := []node.Node{node.TextStyled(" Event Log ", cAccent, 0, node.Bold)}
	logs := m.Log
	if len(logs) > 6 {
		logs = logs[len(logs)-6:]
	}
	for _, l := range logs {
		lines = append(lines, node.TextStyled(" "+l, cDim, 0, 0))
	}
	return node.Column(lines...).WithScrollToBottom()
}

func (m *Model) viewFooter() node.Node {
	return node.Row(
		node.TextStyled(" Enter: submit · o: override&escalate · c: config · r: resume · q: quit (empty prompt) · Ctrl+C×2 ", cDim, 0, 0),
		node.Spacer(),
		node.TextStyled(" "+m.Status+" ", cWarn, 0, 0),
	)
}

func (m *Model) viewConfig() node.Node {
	return node.Column(
		node.TextStyled(" Config (Enter: save & hot-reload · Shift+Enter: newline · Esc: cancel) ", cAccent, 0, node.Bold),
		node.Box(node.BorderRounded, node.Column(m.ConfigInput.Render("", 0, 0, 100)).WithScrollToBottom()).WithFlex(1),
		node.TextStyled(" "+m.Status+" ", cWarn, 0, 0),
	)
}

func (m *Model) viewResume() node.Node {
	lines := []node.Node{
		node.TextStyled(" Resume a task (↑/↓ · Enter: resume · Esc: cancel) ", cAccent, 0, node.Bold),
	}
	if len(m.Tasks) == 0 {
		lines = append(lines, node.TextStyled(" no persisted tasks ", cDim, 0, 0))
	}
	for i, t := range m.Tasks {
		status := "final"
		if t.Running {
			status = "running"
		} else if t.Final == "" {
			status = "incomplete"
		}
		line := fmt.Sprintf(" %s  $%.2f  %-10s  %s", t.TaskID, t.SpentUSD, status, truncate(t.Original, 60))
		if i == m.TaskSel {
			lines = append(lines, node.TextStyled("> "+line, cAccent, 0, node.Bold))
		} else {
			lines = append(lines, node.Text("  "+line))
		}
	}
	lines = append(lines, node.Spacer(), node.TextStyled(" "+m.Status+" ", cWarn, 0, 0))
	return node.Column(lines...)
}

func (m *Model) viewOverride() node.Node {
	f := func(label, val string) node.Node {
		if val == "" {
			val = "(routed)"
		}
		return node.Text(fmt.Sprintf("  %s: %s", label, val))
	}
	budget := "(routed)"
	if m.Override.MaxCostUSD > 0 {
		budget = fmt.Sprintf("$%.0f", m.Override.MaxCostUSD)
	}
	return node.Column(
		node.TextStyled(" Override & escalate — applies to the NEXT task ", cAccent, 0, node.Bold),
		node.Text(""),
		f("s → strategy   ", m.Override.Strategy),
		f("t → thinking   ", m.Override.Thinking),
		node.Text("  +/- → budget   : "+budget),
		node.Text("  x → clear all overrides"),
		node.Text(""),
		node.TextStyled(" Enter/Esc: back ", cDim, 0, 0),
		node.Spacer(),
	)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// wrapText renders a paragraph as wrapped Text nodes.
func wrapText(s string, width int) node.Node {
	if width < 20 {
		width = 20
	}
	var lines []node.Node
	for _, para := range strings.Split(s, "\n") {
		for len(para) > width {
			cut := strings.LastIndex(para[:width], " ")
			if cut < width/2 {
				cut = width
			}
			lines = append(lines, node.Text(" "+para[:cut]))
			para = strings.TrimLeft(para[cut:], " ")
		}
		lines = append(lines, node.Text(" "+para))
		if len(lines) > 200 {
			break
		}
	}
	return node.Column(lines...)
}
