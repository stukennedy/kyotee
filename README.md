# Kyotee

Autonomous development agent for Claude Code. Build projects from conversation to code.

```
Discovery                    Implementation
    │                              │
    ▼                              ▼
┌─────────┐                  ┌───────────┐
│  Chat   │    Approve       │ Autonomous│
│  about  │ ───────────────► │   Build   │
│  what   │     Spec         │           │
│  to     │                  │ (files,   │
│  build  │                  │  tests,   │
└─────────┘                  │  etc.)    │
                             └───────────┘
```

## How It Works

1. **Discovery** - Chat with Kyotee about what you want to build
2. **Spec** - Kyotee generates a structured specification
3. **Approve** - Review and confirm the spec
4. **Build** - Kyotee implements autonomously, tracking progress

The key: separate *what* (interactive) from *how* (autonomous).

## Installation

Requires [Claude Code](https://docs.anthropic.com/en/docs/claude-code) installed and authenticated.

```bash
git clone https://github.com/stukennedy/kyotee.git
cp -r kyotee/.claude/skills/kyotee ~/.claude/skills/
```

## Usage

In Claude Code, run:

```
/kyotee
```

### Built-in Tech Stack Patterns

Kyotee includes patterns for common tech stacks in `references/`:

| Pattern | Description |
|---------|-------------|
| `hono-datastar.md` | Hono + Datastar on Cloudflare Workers |
| `go-gin.md` | Go + Gin REST API patterns |
| `nextjs.md` | Next.js + Tailwind patterns |
| `fastapi.md` | Python + FastAPI patterns |

During discovery, Kyotee reads these patterns and applies them during implementation.

### Example Session

```
You: /kyotee

Kyotee: What would you like to build?

You: A REST API for managing bookmarks

Kyotee: What tech stack?
        [Web App / CLI / API / Library]

You: API

Kyotee: [Asks about framework, features...]

You: Go with Gin, CRUD for bookmarks, tags, search

Kyotee: Here's the spec:

        Project: bookmark-api
        Stack: Go + Gin
        Features:
          - CRUD endpoints for bookmarks
          - Tag support
          - Search functionality

        Ready to implement? (yes/no/edit)

You: yes

Kyotee: [Creates files following Go+Gin patterns...]
        Done! Run: go mod tidy && go run ./cmd/server
```

### Resume a Session

Kyotee saves state in `.kyotee/` so you can pick up where you left off:

```
You: /kyotee

Kyotee: Welcome back! Last session we were building your bookmark API.
        The spec is approved and I've implemented the CRUD endpoints.

        Remaining: tags and search

        Should I continue?
```

## State Files

Kyotee creates `.kyotee/` in your project:

```
.kyotee/
├── spec.md           # Approved specification
└── progress.json     # Execution state
```

## Adding Custom Patterns

Create your own tech stack patterns:

```bash
# Create ~/.claude/skills/kyotee/references/my-stack.md
```

Include:
- Project structure
- Naming conventions
- Code patterns
- Configuration templates
- Setup commands

Example:
```markdown
# My Stack Patterns

## Project Structure
...

## Naming Conventions
...

## Code Patterns
...
```

## Tips

- **Be specific** during discovery - more detail = better implementation
- **Review the spec** before approving - it's your source of truth
- **Resume anytime** - just run `/kyotee` again

## License

MIT
