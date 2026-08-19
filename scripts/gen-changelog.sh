#!/usr/bin/env bash
# ==============================================================================
# LXM - Local Changelog Generator
# ==============================================================================
# Generates a clean, emoji-free Markdown changelog from conventional git commits.
# Usage:
#   ./scripts/gen-changelog.sh [tag]        # Generate changelog for a specific tag / range
#   ./scripts/gen-changelog.sh --all        # Generate full changelog history across all tags
# ==============================================================================
set -euo pipefail

format_section() {
    local range="$1"
    local title="$2"
    local pattern="$3"
    local commits
    commits=$(git log "${range}" -E --grep="${pattern}" --format="- %s" 2>/dev/null || true)
    if [[ -n "${commits}" ]]; then
        echo "### ${title}"
        # Convert "- feat(scope): msg" to "- **scope**: msg" or "- feat: msg" to "- msg"
        echo "${commits}" | sed -E \
            -e 's/^- [a-z]+(\([^\)]+\))!?: /- \1: /' \
            -e 's/^- \(([^)]+)\): /- **\1**: /' \
            -e 's/^- [a-z]+: /- /'
        echo ""
    fi
}

generate_for_range() {
    local tag="$1"
    local prev_tag="$2"
    local range
    local date

    if [[ -n "${prev_tag}" ]]; then
        range="${prev_tag}..${tag}"
    else
        range="${tag}"
    fi

    if git rev-parse "${tag}" >/dev/null 2>&1; then
        date=$(git log -1 --format=%cs "${tag}")
    else
        date=$(date +%Y-%m-%d)
    fi

    local version_clean="${tag#v}"
    echo "## [${version_clean}] - ${date}"
    echo ""

    format_section "${range}" "Breaking Changes" "^[a-z]+(\(.*\))?!:"
    format_section "${range}" "Features" "^feat(\(.*\))?:"
    format_section "${range}" "Bug Fixes" "^fix(\(.*\))?:"
    format_section "${range}" "Performance" "^perf(\(.*\))?:"
    format_section "${range}" "Refactoring" "^refactor(\(.*\))?:"
    format_section "${range}" "Testing" "^test(\(.*\))?:"
    format_section "${range}" "Documentation" "^docs(\(.*\))?:"
    format_section "${range}" "Maintenance" "^(chore|build|ci|deps|nit)(\(.*\))?:"
}

TARGET="${1:-}"

if [[ "${TARGET}" == "--all" ]]; then
    echo "# Changelog"
    echo ""
    echo "All notable changes to this project will be documented in this file."
    echo ""

    TAGS=($(git tag --sort=-creatordate))
    for i in "${!TAGS[@]}"; do
        TAG="${TAGS[$i]}"
        PREV_TAG="${TAGS[$((i + 1))]:-}"
        generate_for_range "${TAG}" "${PREV_TAG}"
        echo "---"
        echo ""
    done
elif [[ -n "${TARGET}" ]]; then
    # Find previous tag before TARGET
    PREV_TAG=$(git tag --sort=creatordate | grep -B1 -x "${TARGET}" | head -n1 || true)
    if [[ "${PREV_TAG}" == "${TARGET}" ]]; then
        PREV_TAG=""
    fi
    generate_for_range "${TARGET}" "${PREV_TAG}"
else
    # Default: diff between latest tag and previous tag, or latest and HEAD
    LATEST_TAG=$(git describe --tags --abbrev=0 2>/dev/null || echo "")
    if [[ -n "${LATEST_TAG}" ]]; then
        PREV_TAG=$(git tag --sort=creatordate | grep -B1 -x "${LATEST_TAG}" | head -n1 || true)
        if [[ "${PREV_TAG}" == "${LATEST_TAG}" ]]; then
            PREV_TAG=""
        fi
        generate_for_range "${LATEST_TAG}" "${PREV_TAG}"
    else
        generate_for_range "HEAD" ""
    fi
fi
