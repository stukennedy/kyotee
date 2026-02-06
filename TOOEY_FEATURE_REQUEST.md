# Tooey Feature Requests

Feature requests from real-world usage building [Kyotee](https://github.com/stukennedy/kyotee), an autonomous development agent TUI. Every item below comes from an actual workaround or verbose pattern in the kyotee codebase (`internal/tui/`).

---

## 1. Streaming Commands (Sub/Channel Pattern)

**What's missing:** `app.Cmd` returns a single `app.Msg`. There's no way to stream multiple messages back to the app from a long-running command (e.g., real-time process output).

**Current workaround:** Kyotee bypasses the message system entirely and mutates model state directly from goroutine callbacks, which is unsafe and loses reactivity:

```go
// app.go:839-841
// Note: In Tooey, we can't send messages to the app from callbacks directly.
// The engine callbacks won't update the TUI in real-time without a message channel.
// For now, we run and return the final result.

// app.go:898-910 — direct model mutation from goroutine
engine.OnOutput = func(text string) {
    mdl.autonomousOutput = append(mdl.autonomousOutput, text)
}
engine.OnPhase = func(phase, status string) {
    mdl.autonomousOutput = append(mdl.autonomousOutput, fmt.Sprintf("\n[%s] %s\n", phase, status))
}
engine.OnTool = func(name string, input any) {
    mdl.autonomousOutput = append(mdl.autonomousOutput, fmt.Sprintf("\u{1f527} %s\n", name))
}
```

**Proposed API:**

```go
// A Sub is a long-running command that can send multiple messages
type Sub func(send func(Msg)) Msg

// Usage
func streamProcessOutput(ctx context.Context) app.Sub {
    return func(send func(app.Msg)) app.Msg {
        scanner := bufio.NewScanner(stdout)
        for scanner.Scan() {
            send(OutputLineMsg{Line: scanner.Text()}) // intermediate
        }
        return ProcessDoneMsg{} // final
    }
}

// In update:
return app.WithSub(mdl, streamProcessOutput(ctx))
```

---

## 2. Full-Width Bar / FillWidth Node

**What's missing:** No way to create a text node that fills the remaining terminal width with a background color (for header/footer bars).

**Current workaround:** Manual padding calculation repeated 6+ times across `app.go` and `execute.go`:

```go
// app.go:614-618 — header bar
headerText := fmt.Sprintf(" %s  %s", Logo(), modeText)
pad := w - len([]rune(headerText))
if pad < 0 {
    pad = 0
}
header := node.TextStyled(headerText+strings.Repeat(" ", pad), colPrimary, colDarkBg, node.Bold)

// app.go:670-674 — footer bar (same pattern)
helpPad := w - len([]rune(helpText))
if helpPad < 0 {
    helpPad = 0
}
help := node.TextStyled(helpText+strings.Repeat(" ", helpPad), colMuted, colDarkBg, 0)

// execute.go:270-274, 321-325 — same pattern again
```

**Proposed API:**

```go
node.Bar(" KYOTEE  Discovery", colPrimary, colDarkBg, node.Bold)
// Automatically pads to terminal width with background color
```

---

## 3. Horizontal Separator

**What's missing:** No separator/divider node. Drawing a horizontal rule requires manual width tracking.

**Current workaround:** `strings.Repeat("─", w)` appears 4+ times:

```go
// app.go:662, 678, 771, 774
node.TextStyled(strings.Repeat("─", w), colDim, 0, 0)

// execute.go:329, 335
node.TextStyled(strings.Repeat("─", width), colDim, 0, 0)
```

**Proposed API:**

```go
node.Separator()                          // default ─ in dim
node.SeparatorStyled('═', colAccent)     // custom char + color
```

---

## 4. Padding / Indent Wrapper

**What's missing:** No structural way to indent or pad child nodes. All indentation is done with manual string prefixes.

**Current workaround:** Hard-coded space prefixes at every level:

```go
// execute.go:129-132 — selector indentation
selector := "  "
if selected {
    selector = "› "
}

// execute.go:167-169 — output lines indented 6 spaces
for _, line := range phase.Output[startIdx:] {
    nodes = append(nodes, node.TextStyled("      "+line, colText, 0, 0))
}

// execute.go:192 — file changes indented 8 spaces
fmt.Sprintf("        %s %s", prefix, f.Path)

// app.go:625-638 — chat messages with role-dependent prefixes
prefix := "      "
if i == 0 {
    prefix = "  You: "
}
```

**Proposed API:**

```go
node.Indent(2, node.Column(
    node.Text("Output line 1"),
    node.Text("Output line 2"),
))

// Or padding on all sides
node.Pad(1, 2, 0, 2, child) // top, right, bottom, left
```

---

## 5. Paragraph / Text Wrapping Node

**What's missing:** No text node that handles multi-line content or word wrapping. Displaying a block of text requires manually splitting on `\n` and creating a node per line.

**Current workaround:** Manual line splitting for every multi-line string:

```go
// app.go:625-631 — user messages
for i, line := range strings.Split(msg.content, "\n") {
    prefix := "      "
    if i == 0 {
        prefix = "  You: "
    }
    chatNodes = append(chatNodes, node.TextStyled(prefix+line, colSecondary, 0, 0))
}

// app.go:633-639 — assistant messages (same pattern)
for i, line := range strings.Split(msg.content, "\n") {
    prefix := "     "
    if i == 0 {
        prefix = "  \u{1f43a} "
    }
    chatNodes = append(chatNodes, node.TextStyled(prefix+line, colText, 0, 0))
}
```

**Proposed API:**

```go
// Handles newlines and word-wrapping automatically
node.Paragraph(content, colText, 0, 0)

// With a hanging indent (first line different from continuation)
node.ParagraphStyled(content, node.ParagraphOpts{
    FirstLinePrefix: "  You: ",
    ContinuePrefix:  "      ",
    FG: colSecondary,
})
```

---

## 6. Expandable / Collapsible Section

**What's missing:** No built-in expand/collapse component. Kyotee manually tracks an `Expanded` bool per section and conditionally renders children.

**Current workaround:**

```go
// execute.go:19 — manual bool on each phase
type PhaseProgress struct {
    // ...
    Expanded bool
    // ...
}

// app.go:561-563 — manual toggle on Enter
if mdl.execState != nil && mdl.selectedIdx < len(mdl.execState.Phases) {
    mdl.execState.Phases[mdl.selectedIdx].Expanded = !mdl.execState.Phases[mdl.selectedIdx].Expanded
}

// execute.go:134-137 — manual icon rendering
expandIcon := "▶"
if phase.Expanded {
    expandIcon = "▼"
}

// execute.go:159 — conditional child rendering
if phase.Expanded && len(phase.Output) > 0 {
    // ... render all children ...
}
```

**Proposed API:**

```go
node.Collapsible("▶ context (3s)", "▼ context (3s)", expanded, childNodes...)
// Or as a component with built-in toggle state
component.Accordion(sections)
```

---

## 7. Progress / Step Indicator

**What's missing:** No step/progress indicator component. Kyotee hand-builds a phase bar with icons, arrows, and status colors.

**Current workaround:**

```go
// execute.go:75-101
func RenderPhaseBar(phases []PhaseProgress, width int) node.Node {
    var items []node.Node
    for i, p := range phases {
        var icon string
        var fg node.Color
        switch p.Status {
        case "pending":  icon = "○"; fg = colMuted
        case "running":  icon = "●"; fg = colWarning
        case "passed":   icon = "✓"; fg = colPrimary
        case "failed":   icon = "✗"; fg = colError
        }
        label := fmt.Sprintf(" %s %s ", icon, p.ID)
        items = append(items, node.TextStyled(label, fg, 0, node.Bold))
        if i < len(phases)-1 {
            items = append(items, node.TextStyled("→", colDim, 0, 0))
        }
    }
    return node.Row(items...)
}
```

**Proposed API:**

```go
component.Steps([]component.Step{
    {Label: "context", Status: component.StepDone},
    {Label: "plan",    Status: component.StepActive},
    {Label: "implement", Status: component.StepPending},
    {Label: "verify",  Status: component.StepPending},
    {Label: "deliver", Status: component.StepPending},
})
```

---

## 8. Spinner Component

**What's missing:** No built-in spinner. Every app needs to maintain its own frames array, tick index, and timer command.

**Current workaround:**

```go
// app.go:32 — frame array
var spinnerFrames = []string{"\u280b", "\u2819", "\u2839", "\u2838", "\u283c", "\u2834", "\u2826", "\u2827", "\u2807", "\u280f"}

// app.go:90 — index on model
spinnerIdx int

// app.go:256-257 — manual tick handler
case TickMsg:
    mdl.spinnerIdx = (mdl.spinnerIdx + 1) % len(spinnerFrames)
    return app.WithCmd(mdl, tickCmd())

// app.go:781-786 — tick command boilerplate
func tickCmd() app.Cmd {
    return func() app.Msg {
        time.Sleep(100 * time.Millisecond)
        return TickMsg{}
    }
}

// app.go:644 — usage
frame := spinnerFrames[mdl.spinnerIdx]
```

**Proposed API:**

```go
// As a node (auto-animates)
node.Spinner(node.SpinnerDots) // or SpinnerLine, SpinnerBraille, etc.

// Or as a component with label
component.Spinner("thinking...", colMuted)
```

---

## 9. Truncation Helper

**What's missing:** No built-in text truncation with ellipsis.

**Current workaround:**

```go
// execute.go:278-279
taskPreview := state.Task
if len(taskPreview) > 80 {
    taskPreview = taskPreview[:77] + "..."
}
```

**Proposed API:**

```go
node.Truncate(text, 80) // returns "long text he..."
// Or as a node option
node.Text(text).WithMaxWidth(80)
```

---

## 10. Badge / Tag Component

**What's missing:** No inline badge/tag for status indicators. Status labels with icon + color are built inline repeatedly.

**Current workaround:** The icon+color+status pattern appears in three separate places (`RenderPhaseBar`, `RenderPhaseDetail`, gate rendering in `RenderPhaseDetail`):

```go
// execute.go:80-93 — phase bar
switch p.Status {
case "pending":  icon = "○"; fg = colMuted
case "running":  icon = "●"; fg = colWarning
case "passed":   icon = "✓"; fg = colPrimary
case "failed":   icon = "✗"; fg = colError
}

// execute.go:111-127 — phase detail (same switch, different icons for running)
// execute.go:199-212 — gates (same switch again)
switch g.Status {
case "running":  gIcon = "●"; gFG = colWarning
case "passed":   gIcon = "✓"; gFG = colPrimary
case "failed":   gIcon = "✗"; gFG = colError
}
```

**Proposed API:**

```go
component.Badge("passed", component.BadgeSuccess) // ✓ passed (green)
component.Badge("failed", component.BadgeError)   // ✗ failed (red)
component.Badge("running", component.BadgeWarning) // ● running (amber)
```

---

## Summary

| # | Feature | Frequency in kyotee | Complexity to add |
|---|---------|-------------------|-------------------|
| 1 | Streaming commands (Sub) | Core architecture gap | High |
| 2 | FillWidth bar | 6+ occurrences | Low |
| 3 | Horizontal separator | 4+ occurrences | Low |
| 4 | Padding/indent wrapper | Pervasive | Medium |
| 5 | Paragraph/text wrap | 4+ occurrences | Medium |
| 6 | Collapsible section | 1 complex implementation | Medium |
| 7 | Step indicator | 1 complex implementation | Medium |
| 8 | Spinner component | 4 boilerplate locations | Low |
| 9 | Truncation helper | 1 occurrence (but universal need) | Low |
| 10 | Badge/tag | 3 duplicate switch blocks | Low |

The streaming commands (Sub pattern) is the most impactful -- it's a fundamental architecture gap that forced kyotee to bypass the message system entirely. The low-complexity items (Bar, Separator, Spinner, Truncate, Badge) are quick wins that would immediately reduce boilerplate.
