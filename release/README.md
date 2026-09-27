# release/

> **The verbs run; the version pinning does not.** All six exist and
> `release-qualify.yml` wires them end to end, but no spec consults
> `-e2e.versions`, so a run qualifies what the working tree builds rather than
> the candidate the plan named. See
> [what a result means today](../docs/qualification/workflow.md#what-a-result-means-today).

Release tooling: the orchestration that takes a candidate set of already-built
artifacts, qualifies it, and — on success — publishes the immutable record of
what was qualified together.

**The workflow itself is documented in
[`docs/qualification/workflow.md`](../docs/qualification/workflow.md)** — the
phases, the stream and scope model, what the e2e suite is told, and the layers
that gate a release. This file covers the package: what belongs here, why it is
a module, and where each piece lives.

## What this is not

`build/` produces the artifacts for one component. `release/` never builds
anything. It consumes artifacts that already exist, addressed by digest, and it
is an error for anything under this directory to compile source into a shipped
artifact. That separation is what makes the qualification meaningful: a release
promotes the exact bytes that were tested, and tooling that can rebuild is
tooling that can substitute.

"Addressed by digest" is not as simple as it sounds, and the record's schema
reflects that. `oras push` writes `org.opencontainers.image.created` into the
manifest with wall-clock push time, so for anything published by a fresh push
rather than copied — the agent, the CLI, the chart — the manifest digest
identifies a *publication event*, not bytes. Republish identical content and
the digest changes. A container image is different: its manifest digest is its
content identity, and `crane copy` preserves it. So the schema carries two
fields. `digest` is what you pull by and is unambiguous within one registry;
`contentDigest` is the layer, exists only where there is a single one, and is
the only thing that still means anything once an artifact has been mirrored.

Per-component publication stays where it is. `agent/v*`, `os-image/v*`,
`controller/v*` and `cli/v*` tags continue to drive their own workflows exactly
as [`RELEASE.md`](../RELEASE.md) describes. `release/` orchestrates *over* those
publications; it does not replace them.

## Why there is a go.mod here

An earlier revision of this file argued against one. The argument was that
`.github/workflows/release.yml` reads the directory prefix off a release tag
and checks it against `go.work`, so a module here would make `release/vX.Y.Z` a
valid release — a fourth independently versioned thing in a repository whose
versioning model has three categories. That does not hold up.

The tag trigger is `*/v[0-9]*`, and a glob does not cross a slash, so only
top-level modules are reachable: `agent`, `cli`, `controller`, `os-image`,
`management-engine` and `cluster-api-provider-provisioned`. Two of those six
are already not product categories, and `management-engine` has carried zero
tags without anyone concluding it ships. Being taggable is not being a product.
Tagging is a deliberate act, and the three-category model lives in the
versioning proposal rather than in the set of directories a workflow can
resolve.

The alternative was real but self-defeating. `make work-check` accounts for
every `go.mod` against two lists, and the second, `PRIVATE_MODULES`, exists for
modules held out of the workspace. A module on it is never built or synced,
which would forfeit exactly the `lint`, `test`, `govulncheck` and license
coverage that made Go worth choosing over a shell and Python pair.

The module costs nothing at the dependency boundary: `cobra`, `sigs.k8s.io/yaml`
and `golang.org/x/mod` are already in the committed `vendor/` tree, so adding it
left that tree byte-for-byte unchanged.

## Layout

| Path | Contents |
|---|---|
| [`qualification.yaml`](qualification.yaml) | The release-wide policy: components in scope, the streams they run against, and the named scopes over those streams |
| [`cmd/qualify/`](cmd/qualify/) | The binary: one file per verb |
| [`internal/plan/`](internal/plan/) | Turning a trigger into a run list: dependency resolution, scope expansion, and what an earlier run already settled |
| [`internal/policy/`](internal/policy/) | Reading and validating the `qualification.yaml` files, and the result vocabulary every verb shares |
| [`internal/capacity/`](internal/capacity/) | Confirming the hardware a stream needs before anything runs, so a shortage reports as a shortage |
| [`internal/lane/`](internal/lane/) | Which CI lane a stream's provider runs in: backend, label filter and Tilt providers, resolved at plan time |
| [`internal/suite/`](internal/suite/) | Every invocation of the e2e suite, both the dry run that asks what a selection needs and the run itself, plus folding a lane's legs into one summary |
| [`internal/record/`](internal/record/) | Where a result goes: the composite tag, the artifact, and the on-disk handoff between `run` and `publish` |
| [`internal/aggregate/`](internal/aggregate/) | Folding every result into one artifact and deciding what each candidate is qualified at |
| [`internal/dashboard/`](internal/dashboard/) | Rendering that artifact as a static page |

A component says how it is qualified in its own file, next to the code:
[`controller/qualification.yaml`](../controller/qualification.yaml),
[`cli/qualification.yaml`](../cli/qualification.yaml),
[`os-image/qualification.yaml`](../os-image/qualification.yaml). `agent/` has
none, because the agent is built from the same checkout as the OS image and
baked into it, so it is never deployed alone and a run has nothing to select.
