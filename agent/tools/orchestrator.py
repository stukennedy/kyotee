#!/usr/bin/env python3
"""
Kyotee - CLI orchestrator for deterministic AI agent workflows.

Runs phases as a state machine, calls Claude Code (or any CLI LLM) as a worker,
enforces strict JSON outputs via JSON Schema, runs verification gates automatically,
and loops on failures up to limits.
"""
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


# Pattern to extract JSON from markdown code blocks or raw output
MARKDOWN_JSON_RE = re.compile(r"```(?:json)?\s*(\{.*?\})\s*```", re.DOTALL)
RAW_JSON_RE = re.compile(r"(\{.*\})", re.DOTALL)


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
    print(f"[kyotee] ERROR: {msg}", file=sys.stderr)
    sys.exit(code)


def log(msg: str) -> None:
    print(f"[kyotee] {msg}")


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


def extract_json(text: str) -> Dict[str, Any]:
    """Extract JSON from text, handling markdown code blocks."""
    # First try markdown code blocks
    m = MARKDOWN_JSON_RE.search(text)
    if m:
        try:
            return json.loads(m.group(1))
        except json.JSONDecodeError:
            pass

    # Fall back to raw JSON extraction
    m = RAW_JSON_RE.search(text)
    if m:
        try:
            return json.loads(m.group(1))
        except json.JSONDecodeError as e:
            raise ValueError(f"JSON parse error: {e}")

    raise ValueError("No JSON object found in output")


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

    # Extract JSON (handles markdown code blocks and raw JSON)
    try:
        obj = extract_json(combined)
    except ValueError as e:
        die(f"Worker output error: {e}. See {out_path}")

    return obj


def assemble_prompt(repo_root: Path, ctx: RunContext, phase_id: str, prompts_dir: Path, schema_path: Path) -> str:
    sys_prompt = (prompts_dir / "system.md").read_text(encoding="utf-8")
    phase_prompt = (prompts_dir / f"phase_{phase_id}.md").read_text(encoding="utf-8")
    schema_content = schema_path.read_text(encoding="utf-8") if schema_path.exists() else "{}"

    # Minimal context injection. Add more (grep results, file snippets) later.
    diff = get_diff(repo_root)
    return "\n\n".join([
        sys_prompt,
        phase_prompt,
        "TASK:\n" + ctx.task,
        "CURRENT_GIT_DIFF:\n" + (diff if diff.strip() else "<none>"),
        "REQUIRED JSON SCHEMA:\n" + schema_content,
        "INSTRUCTIONS:\nReturn ONLY a valid JSON object matching the schema above. No markdown, no explanation, just the JSON."
    ])


def main() -> None:
    ap = argparse.ArgumentParser(
        prog="kyotee",
        description="CLI orchestrator for deterministic AI agent workflows"
    )
    ap.add_argument("--spec", default="agent/spec.toml", help="Path to spec.toml")
    ap.add_argument("--repo", default=".", help="Path to repo root")
    ap.add_argument("--task", required=True, help="Task description (string).")
    ap.add_argument("--worker", default="claude", help="Worker CLI command (e.g., claude).")
    ap.add_argument("--worker-args", default="-p", help="Extra args for worker command (default: -p for print mode).")
    ap.add_argument("--timeout", type=int, default=600, help="Timeout in seconds for worker calls")
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

    log(f"Starting run: {run_dir}")

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

        log(f"Phase: {phase_id} (iteration {ctx.phase_iterations[phase_id]})")

        # Validate JSON schema path
        schema_path = (schemas_dir / phase.schema_path).resolve()

        # Assemble prompt
        prompt = assemble_prompt(repo_root, ctx, phase_id, prompts_dir, schema_path)

        # Call worker
        phase_dir = run_dir / phase_id / f"iter_{ctx.phase_iterations[phase_id]}"
        phase_dir.mkdir(parents=True, exist_ok=True)
        worker_out_path = phase_dir / "worker_output.txt"
        control = call_worker(worker_cmd, prompt, args.timeout, worker_out_path)

        # Validate JSON schema
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
                log(f"  Running gate: {check_name}")
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
                    log(f"  Gate FAILED: {check_name} (exit {exit_code})")
                else:
                    log(f"  Gate PASSED: {check_name}")

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
                log(f"Verification failed, looping back to implement phase")
                current_phase_index = [p.id for p in phases].index("implement")
                continue

        # Advance
        current_phase_index += 1

    # Deliver artifacts
    final_diff = get_diff(repo_root)
    safe_write_text(run_dir / "final.diff", final_diff)

    log(f"DONE. Run artifacts in: {run_dir}")


if __name__ == "__main__":
    main()
