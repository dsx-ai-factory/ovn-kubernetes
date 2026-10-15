# Releases

This document describes how NVIDIA's distribution of OVN-Kubernetes is
versioned, branched, and released. It covers the releases published in this
repository, not upstream's — upstream runs its own cadence and process, which
is documented in the [Release Guide](docs/developer-guide/release.md) kept
alongside this tree.

This repository is a read-only snapshot. Code review, CI, and release
engineering for this distribution run on NVIDIA infrastructure; what appears
here is the published result — the source tree, its release branches, and its
tags. See [README.md](README.md) for what the repository is and is not.

## Version numbering

**NVIDIA releases are numbered `2.MINOR.PATCH`.** The major is always `2`,
which keeps this distribution clear of upstream's version namespace — upstream
has been on major `1` since v1.0.0. `MINOR` names the upstream minor release
this snapshot is built from, so 2.4.0 is built from upstream 1.4. `PATCH`
counts NVIDIA drops against that same upstream baseline.

Numbering starts at 2.4.0 rather than 2.0.0, because the minor is inherited
from upstream rather than counted from zero. This tree was tracking upstream
1.4 when it was first published, so no 2.0, 2.1, 2.2, or 2.3 release exists.
The minor next moves when the tree is rebased onto upstream 1.5, which makes
the release after 2.4.x be 2.5.0.

The minor is a baseline pointer, not a compatibility boundary: two releases
sharing a minor come from the same upstream branch, but NVIDIA may add, change,
or remove behaviour between them. **Pin an exact version, or at most a `~2.4.0`
range — never `^2.4.0`**, which would admit a different upstream baseline.
[CHANGELOG.md](CHANGELOG.md) records the exact upstream branch, tag, and commit
behind each release.

See [Versions and releases](README.md#versions-and-releases) in the README for
the three distinct numbers that appear in this tree — distribution release,
upstream baseline, and container image version — and where each one lives.

## Release branches and tags

**Release branches are named for the minor**, matching upstream's own
`release-1.4`, so `release-2.4` carries the whole 2.4.x series. Every release
in a series is cut from that one branch; it keeps receiving drops and backports
after the initial cut.

**Releases are tagged `v2.MINOR.PATCH`** as annotated tags on the release
branch — `v2.4.0` is the first. **Pin a tag rather than a branch**: a tag
names one exact tree, while the branch moves.

**`main` is the development line** and carries a `-pr` prerelease suffix, so it
reports `2.5.0-pr` while `release-2.4` reports `2.4.0`. The suffix sorts below
the release it leads to, and Helm keeps it out of ordinary version-range
resolution unless a caller asks for it with `--devel` or pins the exact string.

Under the `2.MINOR.PATCH` scheme the minor is bound to the upstream minor the
drop is based on, so `main` can only claim the next minor by tracking upstream
`master` rather than a tag. `config.Version` therefore also carries `-pr` on
`main`, which keeps its declared baseline honest and forces the decision to
drop the suffix at the moment upstream tags the matching release.

### Cutting a release

A release branch is cut from `main` once the tree is rebased onto the upstream
release it targets and the release content is complete. Cutting a new minor
means:

1. **Branch** `release-2.MINOR` from `main`, and set the distribution version
   on it to `2.MINOR.0` with the upstream baseline set to the upstream tag it
   was built from.
2. **Tag** the release commit `v2.MINOR.0`.
3. **Advance `main`** to the next development version, `2.<MINOR+1>.0-pr`,
   with its baseline moved to upstream `master`.
4. **Open a new CHANGELOG section** for the next release; entries accumulate
   there until that release is cut.

The version appears in more than one place and the copies have to move
together:

| What | Where |
|---|---|
| Distribution version | `DistroVersion` in [`config.go`](go-controller/pkg/config/config.go) |
| Upstream baseline | `Version` in [`config.go`](go-controller/pkg/config/config.go) |
| Chart version | `version` in `charts/*/Chart.yaml`, across each chart, its subcharts, and its dependency constraints |
| Image version | `appVersion` in `charts/*/Chart.yaml`, dated `YYYY-MM.N` |

The chart dependency constraints matter in particular: a subchart left behind
makes Helm report a wrong dependency version rather than fail outright.

Subsequent patch releases on an existing series repeat steps 1 and 2 on the
existing branch — the branch already exists, so a patch is a version bump and
a new tag, with no change to `main`.

## Release cadence

**A new minor release is cut every four months — in April, August, and
December.** This matches the upstream cadence of three releases a year, which
is timed to follow a Kubernetes release by roughly three months so that the
Kubernetes release has stabilized and upstream has consumed it before cutting.
Aligning with upstream means each NVIDIA minor has a settled upstream minor
underneath it rather than a moving `master`.

Patch releases are cut against a release branch as needed, between minors.

## What a release contains

Each release carries, against the upstream baseline it is built from:

- **Bug fixes**, both NVIDIA's own and those backported from upstream.
- **CVE remediation** for the tree and its dependencies. See
  [SECURITY.md](SECURITY.md) for how to report a vulnerability and for the
  disclosure policy that governs the response.
- **Features**, which for this distribution means NVIDIA networking and DPU
  functionality on top of the upstream feature set. [CHANGELOG.md](CHANGELOG.md)
  records what each release adds relative to its upstream baseline.
- **Support for the prior Kubernetes release.** Every release supports both the
  Kubernetes release it is built against and the one before it. 2.4.0 builds
  against the Kubernetes 1.36 client libraries and also supports 1.35. This
  gives consumers a release of overlap in which to move between Kubernetes
  versions without also having to move distribution versions.

## Supported versions

**Two minor series are supported at a time: the current release and the one
before it.** This follows upstream, which likewise maintains two active
releases. At the four-month cadence above, a minor therefore receives fixes
for roughly eight months from the day it is cut, and reaches end of life when
the second release after it ships — 2.4.x ends when 2.6.0 is released.

| Series | Status |
|---|---|
| 2.4.x | Supported — current release |

A supported series receives bug fixes, CVE remediation, and backports on its
`release-2.MINOR` branch, published as patch releases. Security fixes land only
in supported series; see [SECURITY.md](SECURITY.md) for the disclosure policy
and the response timelines that govern them.

A series past end of life receives no further drops, including for security.
Its branch and tags stay published, so the source remains available to read,
audit, and build — but consumers needing fixes have to move to a supported
series. Because the minor is bound to an upstream baseline, moving between
series means taking a new upstream minor too; [CHANGELOG.md](CHANGELOG.md)
records what changed relative to each baseline.

## Who cuts a release

Releases are cut by the NVIDIA team that maintains this distribution, listed as
the owners of this repository in [CODEOWNERS](CODEOWNERS). Release engineering
runs on NVIDIA infrastructure, so the tags and branches here are published
rather than produced by automation in this repository — the only automation
this repository carries is the build that proves the published tree compiles
and passes its unit tests.

[MAINTAINERS.md](MAINTAINERS.md) lists the upstream maintainers, who govern the
upstream project and its releases, not this distribution's.
