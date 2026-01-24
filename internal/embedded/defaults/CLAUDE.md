# Kyotee Project

This project was scaffolded by [Kyotee](https://github.com/stukennedy/kyotee), an autonomous development agent.

## Project Resources

The `.kyotee/` folder contains:

- **`spec.json`** - The approved specification that defines what this project should do
- **`conversation.json`** - Discovery conversation history (for context/resume)

## For Claude Code

When working on this project, you can reference the spec for requirements:

```bash
cat .kyotee/spec.json
```

The spec is the source of truth for:
- Project purpose and features
- Tech stack decisions
- Architecture choices made during discovery

## Resuming Work

If the project is incomplete, you can resume with:

```bash
kyotee --continue
```

This will pick up where the last session left off.
