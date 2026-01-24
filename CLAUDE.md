# Kyotee

Autonomous development agent for Claude Code.

## Quick Start

Run `/kyotee` to start. It will check for existing state in `.kyotee/` and either resume or start fresh.

## State Files

- **`.kyotee/conversation.json`** - Chat history for context
- **`.kyotee/spec.json`** - Approved project specification
- **`.kyotee/job.json`** - Execution progress

## Skills

Tech stack skills are in `internal/embedded/defaults/skills/`:
- `go-gin.toml` - Go + Gin
- `nextjs-tailwind.toml` - Next.js + Tailwind
- `python-fastapi.toml` - Python + FastAPI

## Development (Legacy Go CLI)

```bash
go build -o kyotee ./cmd/kyotee
./kyotee
```
