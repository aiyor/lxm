# lxm Documentation Style Guide

This document records the conventions every page of the lxm user guide follows. It is the checklist applied in every phase review (see `lxm_user_guide_plan.md` §8.4). It is written for guide **authors** and reviewers — it is not part of the published site navigation.

---

## 1. Audience

Write for the two personas in `lxm_user_guide_plan.md` §2:

- **P1 — Fleet operator / developer**: knows Linux, LXD basics, YAML, shell. Wants reproducible dev containers with minimal ceremony.
- **P2 — Automation engineer**: wires lxm into CI/CD and scripts. Needs deterministic, parseable output.

Rule that follows: **answer "what does this let me do" before "how does it work."**

## 2. Voice & Tone

- Second person, present tense: "You can …", "Run …".
- No marketing superlatives. "lxm helps you deploy" → "You can deploy".
- No contributor jargon in tutorials and explanation pages. Do not use: *reconciler*, *executor*, *CUE*, *envelope*, *ETag*, *manifest loader*, *presence-wins*, *lattice merge*. Where a technical term is unavoidable, link to the [Concepts](getting-started/concepts.md) page or the repository specs.
- The **Reference** section may use exact flag, schema, and exit-code terms verbatim — that is its job.

## 3. Task-First Structure

- Page titles and headings are verbs or outcomes: "Mount host directories", "Roll back a failed change". Not nouns: "The mounts feature".
- How-to pages follow the template in §7.
- Tutorials never skip steps; how-tos may assume prerequisites and link to them.

## 4. Verified Output Only

- **Every CLI code block is a verbatim paste from a real run** of the shipped binary (against the fake server or an LXD dev host).
- Include `$ echo $?` lines where exit codes matter.
- Never invent output, timestamps, hashes, or error messages.
- Never invent a flag or field. If it is not in `--help` or the manifest schema, it is not in the docs.
- The CI `docs:verify` job enforces `--help` conformance and example-manifest compilation; authors must additionally verify every transcript locally before committing.

## 5. Examples

- Example manifests are **full, valid `lxm/config/v2` manifests** with `schema:`, not fragments that would fail validation.
- Every example manifest used by the guide lives in [`docs/examples/`](examples/) and is compile-gated by CI.
- Copy-paste-correct: the reader must be able to run the example as-is.

## 6. Admonitions

Use MkDocs admonitions deliberately:

| Admonition | When |
|---|---|
| `!!! warning` | **Security-critical behavior only** — host-key verification, `--force`, snapshot destruction on recreate/delete, `--prune` scope, `--insecure`. Must not be skimmable. |
| `!!! note` | Clarifications, pointers, under-construction markers. |
| `!!! tip` | Performance or ergonomics suggestions. |
| `!!! danger` | Reserved for irreversible data loss (e.g. recreate fallback destroying snapshots). |

Keep the number of warnings per page low; if many steps are dangerous, prefer a "Safety" callout once at the top.

## 7. How-To Page Template

```markdown
# <Outcome verb phrase>

<One paragraph: what this guide accomplishes and when to use it.>

## Prerequisites

<What the reader must already have / have read (link to pages).>

## Steps

### 1. <imperative step>

<explanation> + <code block>

### 2. ...

## Verify

<command that proves success + expected (verbatim) output.>

## When things go wrong

<exit code / error → cause → fix. Link to Troubleshooting.>

## Next steps

<links to related guides / reference sections.>
```

## 8. Links & Navigation

- Every how-to links its prerequisites and the exact Reference sections it depends on.
- Use relative markdown links within the site; do not link to `aiyor.github.io/lxm/` from within the docs.
- The `nav` in `mkdocs.yml` is the single source of truth for page titles and order; keep it in sync when adding pages.

## 9. File & Naming Conventions

- Section landing pages are `index.md` under each directory.
- How-to files: lowercase, kebab-case, verb-led (`mount-host-dirs.md`).
- Example manifests: lowercase, kebab-case, under `docs/examples/` with a subdirectory per tutorial when needed.

## 10. Review Checklist

Applied at the end of every phase (UG1–UG5):

- [ ] Every CLI block is verbatim from a real run (no invented output).
- [ ] Every example manifest compiles (`lxm compile docs/examples/` exits 0, no warnings).
- [ ] No contributor jargon in tutorials/explanation pages.
- [ ] Every `!!! warning` / `!!! danger` callout is genuinely security-critical.
- [ ] All links resolve; nav in `mkdocs.yml` is in sync.
- [ ] `mkdocs build` passes.
