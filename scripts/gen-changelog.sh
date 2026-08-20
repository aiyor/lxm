#!/usr/bin/env bash
# ==============================================================================
# LXM - Local Changelog Generator
# ==============================================================================
# Generates a clean, detailed, emoji-free Markdown changelog from conventional git commits.
# Usage:
#   ./scripts/gen-changelog.sh [tag]        # Generate changelog for a specific tag / range
#   ./scripts/gen-changelog.sh --all        # Generate full changelog history across all tags
# ==============================================================================
set -euo pipefail

python3 - "$@" << 'EOF'
import sys
import subprocess
import re

def run_git(*args):
    try:
        return subprocess.check_output(["git"] + list(args), stderr=subprocess.DEVNULL).decode("utf-8", errors="replace").strip()
    except subprocess.CalledProcessError:
        return ""

def parse_body(body_text):
    lines = body_text.strip().split("\n")
    cleaned_items = []
    current_bullet = []
    has_explicit_bullets = any(l.strip().startswith(("- ", "* ")) for l in lines)
    
    for line in lines:
        l = line.strip()
        if not l:
            if current_bullet:
                cleaned_items.append(" ".join(current_bullet))
                current_bullet = []
            continue
        if l.startswith("Co-authored-by:") or l.startswith("Signed-off-by:"):
            continue
        if l.lower().startswith("key capabilities") or l.lower().startswith("key changes"):
            if current_bullet:
                cleaned_items.append(" ".join(current_bullet))
                current_bullet = []
            continue
            
        if l.startswith("- ") or l.startswith("* "):
            if current_bullet:
                cleaned_items.append(" ".join(current_bullet))
                current_bullet = []
            current_bullet.append(l[2:].strip())
        else:
            if current_bullet:
                current_bullet.append(l)
            else:
                current_bullet.append(l)
                
    if current_bullet:
        cleaned_items.append(" ".join(current_bullet))

    if has_explicit_bullets:
        return [item for item in cleaned_items if item]
    
    # If it was a continuous paragraph without explicit bullets, split on sentences
    split_items = []
    for item in cleaned_items:
        sentences = re.split(r"(?<=\.)\s+(?=[A-Z0-9<`])", item)
        for s in sentences:
            s_clean = s.strip()
            if s_clean:
                split_items.append(s_clean)
    return split_items

def generate_for_range(tag, prev_tag):
    range_str = f"{prev_tag}..{tag}" if prev_tag else tag
    
    if tag == "HEAD":
        date = run_git("log", "-1", "--format=%cs")
        version_clean = "Unreleased"
    else:
        date = run_git("log", "-1", "--format=%cs", tag)
        version_clean = tag.lstrip("v")
        
    out = [f"## [{version_clean}] - {date}\n"]

    sections_order = [
        ("Breaking Changes", r"^[a-z]+(\(.*\))?!:"),
        ("Features", r"^feat(\(.*\))?:"),
        ("Bug Fixes", r"^fix(\(.*\))?:"),
        ("Performance", r"^perf(\(.*\))?:"),
        ("Refactoring", r"^refactor(\(.*\))?:"),
        ("Testing", r"^test(\(.*\))?:"),
        ("Documentation", r"^docs(\(.*\))?:"),
        ("Maintenance", r"^(chore|build|ci|deps|nit)(\(.*\))?:"),
    ]

    raw_log = run_git("log", range_str, "--format=%H%x1f%s%x1f%b%x1e")
    commits = [c.strip() for c in raw_log.split("\x1e") if c.strip()]
    
    categorized = {title: [] for title, _ in sections_order}
    other_commits = []

    for c in commits:
        parts = c.split("\x1f")
        subj = parts[1] if len(parts) > 1 else ""
        body = parts[2] if len(parts) > 2 else ""

        matched = False
        for title, pattern in sections_order:
            if re.search(pattern, subj, re.IGNORECASE):
                categorized[title].append((subj, body))
                matched = True
                break
        if not matched:
            other_commits.append((subj, body))

    has_content = False
    for title, _ in sections_order:
        items = categorized[title]
        if not items:
            continue
        has_content = True
        out.append(f"### {title}")
        for subj, body in items:
            formatted_subj = re.sub(r"^[a-z]+(\(([^)]+)\))!?: ", r"- **\2**: ", subj, flags=re.IGNORECASE)
            formatted_subj = re.sub(r"^[a-z]+: ", r"- ", formatted_subj, flags=re.IGNORECASE)
            if not formatted_subj.startswith("- "):
                formatted_subj = "- " + formatted_subj
            out.append(formatted_subj)

            bullets = parse_body(body)
            for b in bullets:
                out.append(f"  - {b}")
        out.append("")

    if not has_content and other_commits:
        out.append("### Initial Release")
        for subj, body in other_commits:
            if "consolidated single commit" in subj.lower():
                out.append("- Initial GA release of LXM declarative container orchestrator")
                out.append("  - Declarative container management for LXD with CUE schema validation")
                out.append("  - Deterministic plan/apply reconciliation workflow")
                out.append("  - Interactive shell, command execution, snapshot and rollback support")
            else:
                out.append(f"- {subj}")
                bullets = parse_body(body)
                for b in bullets:
                    out.append(f"  - {b}")
        out.append("")

    return "\n".join(out)

def main():
    target = sys.argv[1] if len(sys.argv) > 1 else ""
    
    tags_raw = run_git("tag", "--sort=-creatordate")
    tags = [t for t in tags_raw.split("\n") if t]

    if target == "--all":
        print("# Changelog\n\nAll notable changes to this project will be documented in this file.\n")
        for i, tag in enumerate(tags):
            prev_tag = tags[i+1] if i+1 < len(tags) else None
            print(generate_for_range(tag, prev_tag))
            print("---\n")
    elif target:
        prev_tag = None
        if target in tags:
            idx = tags.index(target)
            prev_tag = tags[idx+1] if idx+1 < len(tags) else None
        print(generate_for_range(target, prev_tag))
    else:
        latest = run_git("describe", "--tags", "--abbrev=0")
        if latest:
            prev = None
            if latest in tags:
                idx = tags.index(latest)
                prev = tags[idx+1] if idx+1 < len(tags) else None
            print(generate_for_range(latest, prev))
        else:
            print(generate_for_range("HEAD", None))

if __name__ == "__main__":
    main()
EOF
