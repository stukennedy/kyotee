iAlright — CLI orchestrator it is. Below is a complete, workable starter kit: folder layout + TOML spec + JSON schemas + prompt templates + a Python CLI orchestrator that:
	•	reads your TOML spec
	•	runs phases as a deterministic state machine
	•	calls Claude Code (or any CLI LLM) as a worker
	•	enforces strict JSON outputs via JSON Schema
	•	runs verification gates (tests/lint/typecheck) automatically
	•	loops on failures up to limits
	•	writes a full run log + artifacts

It also supports Ralph narration as a sidecar file that the orchestrator ignores.

⸻

1) Folder layout

agent/
  spec.toml
  prompts/
    system.md
    phase_context.md
    phase_plan.md
    phase_implement.md
    phase_verify.md
    phase_deliver.md
  schemas/
    context_output.schema.json
    plan_output.schema.json
    implement_output.schema.json
    verify_output.schema.json
    deliver_output.schema.json
  tools/
    orchestrator.py
  runs/                     # auto-created


⸻

2) spec.toml (starter)

version = "1.0"
name = "ralph_orchestrated_agent"

[meta]
owner = "Stu Kennedy"
timezone = "Europe/London"

[persona]
mode_name = "Ralph Wiggum"
narration_style = "Cheerful, short, slightly naive sentences. Never impacts compliance."
control_style = "STRICT JSON ONLY matching the provided schema. No extra keys. No prose."

[limits]
max_total_iterations = 25
max_phase_iterations = 6
max_llm_tokens = 6000

[policies]
require_evidence = true
forbid_network = true
forbid_secret_access = true
allow_file_writes = true
allowed_write_paths = ["src/", "tests/", "docs/"]
forbid_write_paths = ["infra/", ".github/workflows/", "secrets/", ".env"]
fail_on_todo = true

[gates]
required_checks = ["unit_tests", "lint", "typecheck"]

[commands]
unit_tests = "pytest -q"
lint = "ruff check ."
typecheck = "mypy ."

[[phases]]
id = "context"
required_outputs_schema = "schemas/context_output.schema.json"

[[phases]]
id = "plan"
required_outputs_schema = "schemas/plan_output.schema.json"

[[phases]]
id = "implement"
required_outputs_schema = "schemas/implement_output.schema.json"

[[phases]]
id = "verify"
required_outputs_schema = "schemas/verify_output.schema.json"

[[phases]]
id = "deliver"
required_outputs_schema = "schemas/deliver_output.schema.json"


⸻

3) JSON Schemas (strict outputs)

schemas/context_output.schema.json

{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["phase", "context_pack", "unknowns", "evidence"],
  "properties": {
    "phase": { "const": "context" },
    "context_pack": {
      "type": "object",
      "additionalProperties": false,
      "required": ["summary", "relevant_files", "assumptions"],
      "properties": {
        "summary": { "type": "string", "minLength": 1 },
        "relevant_files": {
          "type": "array",
          "items": { "type": "string", "minLength": 1 }
        },
        "assumptions": {
          "type": "array",
          "items": { "type": "string", "minLength": 1 }
        }
      }
    },
    "unknowns": {
      "type": "array",
      "items": { "type": "string", "minLength": 1 }
    },
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["kind", "ref", "note"],
        "properties": {
          "kind": { "enum": ["file", "command_output"] },
          "ref": { "type": "string", "minLength": 1 },
          "note": { "type": "string", "minLength": 1 }
        }
      }
    },
    "narration": { "type": "string" }
  }
}

schemas/plan_output.schema.json

{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["phase", "plan", "risks", "requirements_mapping", "evidence"],
  "properties": {
    "phase": { "const": "plan" },
    "plan": {
      "type": "object",
      "additionalProperties": false,
      "required": ["goal", "steps", "success_criteria"],
      "properties": {
        "goal": { "type": "string", "minLength": 1 },
        "steps": {
          "type": "array",
          "minItems": 1,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["id", "goal", "actions", "expected_files", "checks"],
            "properties": {
              "id": { "type": "string", "minLength": 1 },
              "goal": { "type": "string", "minLength": 1 },
              "actions": { "type": "array", "items": { "type": "string", "minLength": 1 } },
              "expected_files": { "type": "array", "items": { "type": "string", "minLength": 1 } },
              "checks": { "type": "array", "items": { "type": "string", "minLength": 1 } }
            }
          }
        },
        "success_criteria": { "type": "array", "items": { "type": "string", "minLength": 1 } }
      }
    },
    "risks": { "type": "array", "items": { "type": "string", "minLength": 1 } },
    "requirements_mapping": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["requirement", "plan_step_id", "evidence_refs"],
        "properties": {
          "requirement": { "type": "string", "minLength": 1 },
          "plan_step_id": { "type": "string", "minLength": 1 },
          "evidence_refs": { "type": "array", "items": { "type": "string", "minLength": 1 } }
        }
      }
    },
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["kind", "ref", "note"],
        "properties": {
          "kind": { "enum": ["file", "command_output"] },
          "ref": { "type": "string", "minLength": 1 },
          "note": { "type": "string", "minLength": 1 }
        }
      }
    },
    "narration": { "type": "string" }
  }
}

schemas/implement_output.schema.json

{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["phase", "changes", "notes", "evidence"],
  "properties": {
    "phase": { "const": "implement" },
    "changes": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["path", "change_type", "summary"],
        "properties": {
          "path": { "type": "string", "minLength": 1 },
          "change_type": { "enum": ["added", "modified", "deleted"] },
          "summary": { "type": "string", "minLength": 1 }
        }
      }
    },
    "notes": { "type": "string" },
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["kind", "ref", "note"],
        "properties": {
          "kind": { "enum": ["file", "command_output"] },
          "ref": { "type": "string", "minLength": 1 },
          "note": { "type": "string", "minLength": 1 }
        }
      }
    },
    "narration": { "type": "string" }
  }
}

schemas/verify_output.schema.json

{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["phase", "checks", "all_passed", "failures", "evidence"],
  "properties": {
    "phase": { "const": "verify" },
    "checks": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "command", "exit_code", "output_ref"],
        "properties": {
          "name": { "type": "string", "minLength": 1 },
          "command": { "type": "string", "minLength": 1 },
          "exit_code": { "type": "integer" },
          "output_ref": { "type": "string", "minLength": 1 }
        }
      }
    },
    "all_passed": { "type": "boolean" },
    "failures": { "type": "array", "items": { "type": "string" } },
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["kind", "ref", "note"],
        "properties": {
          "kind": { "enum": ["command_output"] },
          "ref": { "type": "string", "minLength": 1 },
          "note": { "type": "string", "minLength": 1 }
        }
      }
    },
    "narration": { "type": "string" }
  }
}

schemas/deliver_output.schema.json

{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["phase", "final_summary", "artifacts", "requirements_coverage", "evidence"],
  "properties": {
    "phase": { "const": "deliver" },
    "final_summary": { "type": "string", "minLength": 1 },
    "artifacts": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["kind", "path", "description"],
        "properties": {
          "kind": { "enum": ["diff", "report", "log"] },
          "path": { "type": "string", "minLength": 1 },
          "description": { "type": "string", "minLength": 1 }
        }
      }
    },
    "requirements_coverage": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["requirement", "status", "evidence_refs"],
        "properties": {
          "requirement": { "type": "string", "minLength": 1 },
          "status": { "enum": ["met", "partial", "unmet"] },
          "evidence_refs": { "type": "array", "items": { "type": "string", "minLength": 1 } }
        }
      }
    },
    "evidence": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["kind", "ref", "note"],
        "properties": {
          "kind": { "enum": ["file", "command_output"] },
          "ref": { "type": "string", "minLength": 1 },
          "note": { "type": "string", "minLength": 1 }
        }
      }
    },
    "narration": { "type": "string" }
  }
}


⸻

4) Prompts (system + per phase)

prompts/system.md

You are a software worker running inside a deterministic orchestrator.

Hard rules:
- Output MUST be valid JSON only, matching the provided JSON Schema exactly.
- No extra keys. No prose outside JSON.
- If you are missing information, say so in the JSON fields (e.g., unknowns).
- Never claim a check passed unless you have command output evidence.

Persona / narration:
- You may include optional "narration" string in the JSON with cheerful short Ralph-style sentences.
- Narration is ignored by the orchestrator. Control fields must stay precise.

prompts/phase_context.md

PHASE: context

Goal: Build a context pack: relevant files, summary, assumptions, unknowns. Cite evidence refs.

Allowed:
- repo search and file reading summaries (you will be given file snippets)

Return JSON matching schema/context_output.schema.json

prompts/phase_plan.md

PHASE: plan

Goal: Produce an executable plan with steps, checks, and requirements mapping.

Return JSON matching schema/plan_output.schema.json

prompts/phase_implement.md

PHASE: implement

Goal: Apply minimal changes to satisfy the plan.

Return JSON matching schema/implement_output.schema.json

prompts/phase_verify.md

PHASE: verify

Goal: Evaluate the gate results that the orchestrator ran and decide if all passed.
If failures exist, list them clearly.

Return JSON matching schema/verify_output.schema.json

prompts/phase_deliver.md

PHASE: deliver

Goal: Produce final summary + artifacts list + requirements coverage.

Return JSON matching schema/deliver_output.schema.json


⸻

5) The CLI orchestrator (Python)

Install deps

pip install toml jsonschema

tools/orchestrator.py

#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Optional

import toml
from jsonschema import Draft202012Validator


JSON_RE = re.compile(r"\{.*\}\s*$", re.DOTALL)


@dataclass
class PhaseSpec:
    id: str
    schema_path: str


@dataclass
class RunContext:
    run_dir: Path
    spec: Dict[str, Any]
    task: str
    total_iterations: int = 0
    phase_iterations: Dict[str, int] = None

    def __post_init__(self):
        if self.phase_iterations is None:
            self.phase_iterations = {}


def die(msg: str, code: int = 1) -> None:
    print(f"[orchestrator] ERROR: {msg}", file=sys.stderr)
    sys.exit(code)


def load_schema(schema_path: Path) -> Draft202012Validator:
    if not schema_path.exists():
        die(f"Schema not found: {schema_path}")
    schema = json.loads(schema_path.read_text(encoding="utf-8"))
    v = Draft202012Validator(schema)
    return v


def validate_json(validator: Draft202012Validator, obj: Any) -> None:
    errors = sorted(validator.iter_errors(obj), key=lambda e: e.path)
    if errors:
        msg_lines = ["JSON failed schema validation:"]
        for e in errors[:20]:
            path = ".".join([str(p) for p in e.path]) if e.path else "<root>"
            msg_lines.append(f" - {path}: {e.message}")
        die("\n".join(msg_lines))


def safe_write_text(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def run_cmd(cmd: str, cwd: Path, outfile: Path) -> int:
    outfile.parent.mkdir(parents=True, exist_ok=True)
    p = subprocess.Popen(
        cmd,
        cwd=str(cwd),
        shell=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    out, _ = p.communicate()
    safe_write_text(outfile, out)
    return int(p.returncode)


def check_write_paths(repo_root: Path, changed_paths: List[Path], allowed: List[str], forbidden: List[str]) -> None:
    # Normalize to posix prefix matching
    allowed_prefixes = [a.replace("\\", "/") for a in allowed]
    forbidden_prefixes = [f.replace("\\", "/") for f in forbidden]

    for p in changed_paths:
        rel = p.relative_to(repo_root).as_posix()
        if any(rel.startswith(fp) for fp in forbidden_prefixes):
            die(f"Write policy violation: attempted change in forbidden path: {rel}")
        if allowed_prefixes and not any(rel.startswith(ap) for ap in allowed_prefixes):
            die(f"Write policy violation: attempted change outside allowed paths: {rel}")


def git_changed_files(repo_root: Path) -> List[Path]:
    r = subprocess.run(
        ["git", "diff", "--name-only"],
        cwd=str(repo_root),
        capture_output=True,
        text=True,
    )
    if r.returncode != 0:
        die("git diff failed (is this a git repo?)")
    files = [repo_root / line.strip() for line in r.stdout.splitlines() if line.strip()]
    return files


def get_diff(repo_root: Path) -> str:
    r = subprocess.run(["git", "diff"], cwd=str(repo_root), capture_output=True, text=True)
    return r.stdout if r.returncode == 0 else ""


def call_worker(worker_cmd: List[str], prompt_text: str, timeout_s: int, out_path: Path) -> Dict[str, Any]:
    # Worker must print JSON to stdout.
    out_path.parent.mkdir(parents=True, exist_ok=True)

    try:
        p = subprocess.run(
            worker_cmd,
            input=prompt_text,
            text=True,
            capture_output=True,
            timeout=timeout_s,
        )
    except subprocess.TimeoutExpired:
        die("Worker timed out")

    combined = (p.stdout or "").strip()
    safe_write_text(out_path, combined + ("\n" + (p.stderr or "") if p.stderr else ""))

    if p.returncode != 0:
        die(f"Worker returned non-zero exit code {p.returncode}. See {out_path}")

    # Extract JSON (in case the CLI wraps it — we still enforce JSON-only strongly, but be resilient)
    m = JSON_RE.search(combined)
    if not m:
        die(f"Worker output did not contain a JSON object. See {out_path}")

    try:
        obj = json.loads(m.group(0))
    except json.JSONDecodeError as e:
        die(f"Worker output JSON parse error: {e}. See {out_path}")

    return obj


def assemble_prompt(repo_root: Path, ctx: RunContext, phase_id: str, prompts_dir: Path) -> str:
    sys_prompt = (prompts_dir / "system.md").read_text(encoding="utf-8")
    phase_prompt = (prompts_dir / f"phase_{phase_id}.md").read_text(encoding="utf-8")

    # Minimal context injection. Add more (grep results, file snippets) later.
    diff = get_diff(repo_root)
    return "\n\n".join([
        sys_prompt,
        phase_prompt,
        "TASK:\n" + ctx.task,
        "CURRENT_GIT_DIFF:\n" + (diff if diff.strip() else "<none>"),
        "INSTRUCTIONS:\nReturn ONLY JSON matching the schema provided by the orchestrator."
    ])


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--spec", default="agent/spec.toml")
    ap.add_argument("--repo", default=".")
    ap.add_argument("--task", required=True, help="Task description (string).")
    ap.add_argument("--worker", default="claude", help="Worker CLI command (e.g., claude).")
    ap.add_argument("--worker-args", default="", help="Extra args for worker command (string).")
    ap.add_argument("--timeout", type=int, default=600)
    args = ap.parse_args()

    repo_root = Path(args.repo).resolve()
    spec_path = Path(args.spec).resolve()
    if not spec_path.exists():
        die(f"Spec not found: {spec_path}")

    spec = toml.loads(spec_path.read_text(encoding="utf-8"))

    phases = [PhaseSpec(id=p["id"], schema_path=p["required_outputs_schema"]) for p in spec.get("phases", [])]
    if not phases:
        die("No phases defined in spec.")

    # Create run dir
    ts = time.strftime("%Y%m%d-%H%M%S")
    run_dir = (spec_path.parent / "runs" / ts).resolve()
    run_dir.mkdir(parents=True, exist_ok=True)

    ctx = RunContext(run_dir=run_dir, spec=spec, task=args.task)

    prompts_dir = spec_path.parent / "prompts"
    schemas_dir = spec_path.parent

    # Save immutable run inputs
    safe_write_text(run_dir / "task.txt", args.task)
    shutil.copy2(spec_path, run_dir / "spec.toml")

    limits = spec.get("limits", {})
    max_total = int(limits.get("max_total_iterations", 25))
    max_phase = int(limits.get("max_phase_iterations", 6))

    policies = spec.get("policies", {})
    allow_writes = bool(policies.get("allow_file_writes", True))
    allowed_write_paths = policies.get("allowed_write_paths", [])
    forbid_write_paths = policies.get("forbid_write_paths", [])

    commands = spec.get("commands", {})
    gates = spec.get("gates", {})
    required_checks = gates.get("required_checks", [])

    worker_cmd = [args.worker] + ([a for a in args.worker_args.split(" ") if a.strip()] if args.worker_args else [])

    current_phase_index = 0

    while current_phase_index < len(phases):
        if ctx.total_iterations >= max_total:
            die(f"Reached max_total_iterations={max_total}. Failing run. See {run_dir}")

        phase = phases[current_phase_index]
        phase_id = phase.id
        ctx.phase_iterations[phase_id] = ctx.phase_iterations.get(phase_id, 0) + 1
        if ctx.phase_iterations[phase_id] > max_phase:
            die(f"Reached max_phase_iterations={max_phase} for phase '{phase_id}'. See {run_dir}")

        ctx.total_iterations += 1

        # Assemble prompt
        prompt = assemble_prompt(repo_root, ctx, phase_id, prompts_dir)

        # Call worker
        phase_dir = run_dir / phase_id / f"iter_{ctx.phase_iterations[phase_id]}"
        phase_dir.mkdir(parents=True, exist_ok=True)
        worker_out_path = phase_dir / "worker_output.txt"
        control = call_worker(worker_cmd, prompt, args.timeout, worker_out_path)

        # Validate JSON schema
        schema_path = (schemas_dir / phase.schema_path).resolve()
        validator = load_schema(schema_path)
        validate_json(validator, control)

        # Persist validated control json + narration
        safe_write_text(phase_dir / "control.json", json.dumps(control, indent=2))
        if isinstance(control.get("narration"), str) and control["narration"].strip():
            safe_write_text(phase_dir / "ralph.md", control["narration"].strip() + "\n")

        # Enforce write policy after implement phase (or any phase that might write)
        if allow_writes:
            changed = git_changed_files(repo_root)
            # Only enforce if there are changes
            if changed:
                check_write_paths(repo_root, changed, allowed_write_paths, forbid_write_paths)

        # Special handling: verify phase runs gates deterministically here
        if phase_id == "verify":
            checks_report = []
            failures = []
            verify_dir = phase_dir / "gate_outputs"
            verify_dir.mkdir(parents=True, exist_ok=True)

            for check_name in required_checks:
                cmd = commands.get(check_name)
                if not cmd:
                    die(f"Missing command for gate '{check_name}' in [commands]")
                out_file = verify_dir / f"{check_name}.log"
                exit_code = run_cmd(cmd, repo_root, out_file)
                checks_report.append({
                    "name": check_name,
                    "command": cmd,
                    "exit_code": exit_code,
                    "output_ref": str(out_file.relative_to(run_dir))
                })
                if exit_code != 0:
                    failures.append(f"{check_name} failed (exit {exit_code})")

            # Overwrite verify control with authoritative gate results
            all_passed = (len(failures) == 0)
            verify_control = {
                "phase": "verify",
                "checks": checks_report,
                "all_passed": all_passed,
                "failures": failures,
                "evidence": [{"kind": "command_output", "ref": c["output_ref"], "note": "Gate output"} for c in checks_report],
                "narration": control.get("narration", "")
            }
            validate_json(load_schema(schema_path), verify_control)
            safe_write_text(phase_dir / "control.json", json.dumps(verify_control, indent=2))

            if not all_passed:
                # Loop back to implement to attempt repair
                current_phase_index = [p.id for p in phases].index("implement")
                continue

        # Advance
        current_phase_index += 1

    # Deliver artifacts
    final_diff = get_diff(repo_root)
    safe_write_text(run_dir / "final.diff", final_diff)

    print(f"[orchestrator] DONE. Run artifacts in: {run_dir}")


if __name__ == "__main__":
    main()


⸻

6) Running it

From your repo root:

python agent/tools/orchestrator.py \
  --spec agent/spec.toml \
  --repo . \
  --task "Add a CLI flag --dry-run to foo.py and ensure tests cover it" \
  --worker claude \
  --worker-args "" \
  --timeout 600

Notes about the worker command
	•	If your Claude CLI command needs a different invocation (like claude -p or similar), set --worker-args accordingly.
	•	The orchestrator sends the prompt via stdin and expects JSON on stdout.

⸻

7) The “brutal truth” about what you just built

This is already the right shape, but your biggest risk is LLM tool access. The orchestrator currently:
	•	assumes the worker can edit files / run formatters via its own mechanisms
	•	but doesn’t directly mediate edits

Next upgrade (worth doing):
Make the orchestrator the only thing that can write files (patch-based), and have the LLM output a structured patch plan (or unified diff) instead of editing directly. That turns “agent” into auditable compiler.

But as a first CLI, this will run.

⸻

What you do next (no questions, just the path)
	1.	Drop this scaffold into agent/ in a repo.
	2.	Make sure your worker CLI can output strict JSON.
	3.	Run a trivial task first (docs-only change) to validate the loop.
	4.	Then tighten safety: patch-only writes, denylist expansions, and add semgrep as a gate.

If you want, I can give you the patch-only version (LLM outputs unified diff → orchestrator applies it) which is the “real” safe automation mode
