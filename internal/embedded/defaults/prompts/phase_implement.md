PHASE: implement

Goal: Provide the exact file contents needed to satisfy the plan.

IMPORTANT: You are in a non-interactive mode. You cannot use tools or edit files directly.
Instead, output the full content of each file that needs to be created or modified.
The orchestrator will apply these changes for you.

For each file in the plan:
- Provide the complete file path
- Specify the action: create, modify, or delete
- Include the FULL file content (not a diff or patch)

---

## IMPLEMENTATION DISCIPLINE

### Read Before Write
Before providing modified file content, you MUST understand the existing code.
If the CURRENT_GIT_DIFF or project files show existing patterns (naming conventions,
error handling style, import organization), follow them exactly. Do not introduce
a new style into an existing codebase.

### No Stubs, No Placeholders, No Shortcuts
Every piece of code you output must be COMPLETE and FUNCTIONAL. Specifically:

- **Never** return empty objects (`return {}`, `return []`, `return null`) as placeholders
- **Never** write `// TODO: implement` — implement it or return an error explaining what's missing
- **Never** write `console.log("handler called")` as a function body — write the real logic
- **Never** leave empty event handlers, empty catch blocks, or no-op functions
- **Never** use placeholder text in UI ("Lorem ipsum", "TODO: add content here")
- **Never** write `pass` in Python as a function body without implementation
- If you genuinely cannot implement something (missing external API key, unknown schema),
  return a proper error or throw with a descriptive message — not a silent no-op

### Complete Error Handling
- Every function that can fail MUST handle errors explicitly
- HTTP handlers MUST return proper status codes (not just 200 for everything)
- Database operations MUST handle connection failures and query errors
- File operations MUST handle missing files and permission errors
- User input MUST be validated before processing
- Never swallow errors silently — log them or propagate them

### Security by Default
- Sanitize all user input that touches HTML, SQL, or shell commands
- Use parameterized queries for database operations (never string interpolation)
- Set appropriate CORS headers (not `*` in production)
- Don't hardcode secrets — use environment variables
- Validate and sanitize file paths to prevent traversal attacks

### File Content Rules
- Every file must be COMPLETE — the orchestrator writes the full file, not a patch
- Include all imports, even standard library ones
- Include all type definitions referenced in the file
- Maintain consistent formatting (indentation, line endings)
- Add necessary directory structure (the orchestrator handles mkdir)
- Config files (package.json, tsconfig.json, etc.) must be valid and complete

### Dependency Management
- Only add dependencies that are actually used in the code
- Prefer well-maintained, widely-used packages over obscure ones
- Pin major versions in package.json (e.g., `^4.0.0`, not `*`)
- If the spec says a specific framework, use that framework — don't substitute

### Deviation Rules
When you encounter issues during implementation:

**AUTO-FIX (do it, don't stop):**
- Missing imports or wrong import paths
- Type errors that have an obvious fix
- Missing error handling that should clearly be there
- Missing input validation on public APIs
- Minor bugs in existing code that block your implementation
- Missing directories in file paths

**ADD AUTOMATICALLY (critical for production-readiness):**
- Error handling on all fallible operations
- Input validation on all public-facing functions
- Proper HTTP status codes and error responses
- Logging for important operations and errors
- Graceful shutdown handling for servers

**STOP AND NOTE IN OUTPUT (don't silently change architecture):**
- Need for a database table/schema not in the spec
- Switching frameworks or major libraries
- Changing the API contract (different routes, different response shapes)
- Adding authentication when not specified
- Major structural changes to existing code

Note deviations in the `notes` field of your JSON output.

---

Return JSON matching schema/implement_output.schema.json
