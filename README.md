# Kyotee

Autonomous development agent that builds projects from conversation to code.

```
Discovery Mode          Execute Mode
     │                       │
     ▼                       ▼
┌─────────┐            ┌─────────────┐
│  Chat   │ ─────────► │ Autonomous  │
│  about  │   Approve  │ Implementation
│  what   │    Spec    │ via Claude  │
│  to     │            │ Code CLI    │
│  build  │            └─────────────┘
└─────────┘
```

## How It Works

1. **Discovery Mode** - Interactive chat to define what you want to build
2. **Spec Generation** - Kyotee builds a structured spec from the conversation
3. **Approval** - Review and approve the spec
4. **Autonomous Execution** - Claude Code implements the spec without further interaction

The key insight: separate the *what* (interactive) from the *how* (autonomous).

## Installation

Requires:
- Go 1.21+
- [Claude Code](https://claude.ai/claude-code) CLI installed and authenticated

```bash
# Clone and build
git clone https://github.com/stukennedy/kyotee.git
cd kyotee
go build -o kyotee ./cmd/kyotee

# Optional: add to PATH
mv kyotee /usr/local/bin/
```

## Usage

```bash
# Start in any directory
cd ~/projects/my-new-app
kyotee
```

### Discovery Flow

Kyotee will ask questions to understand what you want to build:

```
🐺 KYOTEE

Hey! I'm Kyotee. What would you like to build today?

> A REST API for managing bookmarks

Got it! A few questions:

• Tech stack? [Go / Node / Python / other]
> Go

• Database? [PostgreSQL / SQLite / none]
> SQLite

• Auth needed? [yes / no]
> no

Building spec...
```

### Spec Approval

Once enough context is gathered, you'll see the spec:

```
┌─ SPEC ──────────────────────────────────────┐
│ Project: Bookmark API                        │
│ Tech: Go + SQLite                            │
│ Features:                                    │
│   - CRUD endpoints for bookmarks             │
│   - SQLite storage                           │
│   - JSON API                                 │
└──────────────────────────────────────────────┘

Ready to implement? [yes / edit / cancel]
```

### Folder Selection

After approving the spec, Kyotee asks where to create the project:

```
Where should I create the project?

  1) Here (current folder: my-app)
  2) New folder: bookmark-api/

Enter 1 or 2:
```

- **Option 1**: Build in the current directory (good for existing projects)
- **Option 2**: Create a new subdirectory (good for new projects)

If you choose a new folder, Kyotee moves the `.kyotee/` state there automatically.

### Autonomous Execution

After folder selection, Kyotee hands off to Claude Code which:
- Creates all necessary files
- Sets up configuration (go.mod, etc.)
- Implements the features
- Runs build/test to verify

You can walk away and come back to working code.

## Skills

Skills are tech-stack specific knowledge stored in `~/.kyotee/skills/`.

```toml
# ~/.kyotee/skills/go-api.toml
name = "Go REST API"
description = "Go APIs with standard library or Chi router"
tags = ["go", "api", "rest"]

[conventions]
project_structure = """
cmd/
  server/
    main.go
internal/
  handlers/
  models/
  db/
go.mod
"""

[patterns]
handler = """
func (h *Handler) GetItem(w http.ResponseWriter, r *http.Request) {
    // ...
}
"""
```

Skills help Kyotee generate code that follows consistent patterns.

## Configuration

### Project State

Kyotee stores state in `.kyotee/` in your project directory:
- `conversation.json` - Chat history (for resume)
- `spec.json` - Generated spec

### CLAUDE.md

Kyotee creates a `CLAUDE.md` file in your project root. This helps Claude Code understand:
- That this is a Kyotee project
- Where to find the spec (`.kyotee/spec.json`)
- How to resume work if needed

This is useful when you return to a project later with Claude Code.

### Claude Code Permissions

For fully autonomous execution, Kyotee uses `--dangerously-skip-permissions`.

If you prefer more control, you can configure allowed tools in Claude Code settings.

## Architecture

```
cmd/kyotee/          # CLI entry point
internal/
  tui/               # Bubble Tea terminal UI
  orchestrator/      # Discovery + autonomous execution
  claude/            # Claude API client (unused - using CLI)
  config/            # Prompt loading
  project/           # .kyotee/ state persistence
  types/             # Shared types
agent/               # Default prompts and skills
```

## Development

```bash
# Build
go build ./...

# Run locally
go run ./cmd/kyotee

# Build binary
go build -o kyotee ./cmd/kyotee
```

## License

MIT
