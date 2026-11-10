# Notes on Releases

## Release Cycles

Since Envoy AI Gateway depends on the Envoy Gateway and Envoy Proxy, we will normally follow the release cycle of the Envoy Gateway.
In other words, we aim to cut the release of the Envoy AI Gateway a few days or a week after the new version of the Envoy Gateway is released. Therefore, the release cycle of the Envoy AI Gateway will be approximately every 2-3 months.

However, given the rapidly changing nature of the AI ecosystem, we aim to release frequently, as soon as features are available.
The main branch of Envoy AI Gateway is tested against both the latest stable version of Envoy Gateway and its main branch, allowing us to confidently release the Envoy AI Gateway at any time if needed.

## Versioning

We increment the major version number when we have a major architectural change or a major feature addition.
Especially when we have a first stable control plane API, we will cut the major v1.0.0 release. Until then, we will use the version number v0.3.x, v0.4.y, etc. See the [support policy](#Support-Policy) for more details.

The patch version will be incremented when we have a bug fix or a security fix. The end of life for the version will be 2 releases after the release of the version. For example, if we release the version v0.1.0, the end of life for the version will be when we release the version v0.3.0.

The main branch will always use the latest version of the Envoy Gateway, hence the latest version of the Envoy, and the main version will be available just like the tagged released versions in the Docker Hub where we also host the helm chart.

## Support Policy

This document focuses on compatibility concerns of those using Envoy AI Gateway.
It is important to note that the support policy is subject to change at any time. The support policy is as follows:

First of all, there are four areas of compatibility that we are concerned with:

- [Using envoyproxy/ai-gateway as a Go package](#public-go-package).
- [Deploying the Envoy AI Gateway controller through the Kubernetes Custom Resource Definition (CRD)](#Custom-Resource-Definitions).
- [Upgrading the Envoy AI Gateway controller](#Upgrading-the-Envoy-AI-Gateway-controller).
- [Envoy Gateway vs Envoy AI Gateway compatibility](#Envoy-Gateway-vs-Envoy-AI-Gateway-compatibility).

### Public Go package

We do not guarantee any compatibility at Go level except for the api package as described below.
Any other public Go package is solely intended for the internal use, e.g. for testing purpose.

### Custom Resource Definitions (CRDs) / `api` package

The Custom Resource Definitions (CRDs) are defined in `api/${version}/*.go` files. The CRDs are versioned as v1alpha1, v1alpha2, etc.

**For alpha versions**, we simply employ the same deprecation policy as the Go package. In other words, the APIs will be marked as deprecated in the version N and will be removed in the version N+2 but without any guarantee of migration path.

Migration paths for alpha versions will be the best effort and will be documented in the release notes.
**For beta versions**, For beta versions, it is the same as the alpha versions, but we will provide a migration path in the release notes.

**For stable versions**, we will never break the APIs unless there is a critical security issue.
We will provide a migration path in the release notes in case we need to break the APIs.

As of the v1.0.0 release, `v1beta1` is the committed **stable** control-plane API and is
covered by the stable-version guarantee above. The `v1alpha1` API remains served but
deprecated, and will be removed in a future release per the deprecation policy. Note that
`QuotaPolicy` is `v1alpha1`-only and is therefore **not** part of the stable surface; in
particular, its `serviceQuota` field is accepted but not yet enforced end-to-end.

### Upgrading the Envoy AI Gateway controller

We guarantee that simply upgrading the controller will not break the existing configuration assuming there's no _un-migrated_ resources including breaking change left in the k8s API server. In other words, after the proper use of the API and migration path described above, the user should be able to upgrade the controller without any issue. However, this does mean that we do NOT guarantee that the existing configuration will work across more than two version of the controller. For example if you are using the version N of the controller, and you want to upgrade to the version N+2, you should first upgrade to the version N+1 while following the migration path if applicable, and then upgrade to the version N+2.

### Envoy Gateway vs Envoy AI Gateway compatibility

Since Envoy AI Gateway is built on top of Envoy Gateway, the compatibility between the two is important.

We use the latest released version of Envoy Gateway as the base of the Envoy AI Gateway when we release a new version.

Since Envoy Gateway is a stable project and supposed to work across versions, we do not expect any compatibility issue as long as the Envoy Gateway version is also up-to-date prior to the upgrade of the Envoy AI Gateway.

## Release Process

This section is for maintainers of the project. Let's say we are going to release the version v0.50.0.

### Release Candidate (RC) Phase

Each non-patch release should start with Release Candidate (RC) phase as follows:

1. First, notify the community that we are going to cut the release candidate and therefore the main branch is frozen.
   The main branch should only accept the bug fixes, the security fixes, and documentation changes.
   The release candidate should always be cut from the main branch.

2. Prepare the docs site with the new version:

   ```
   cd site
   npm run docusaurus docs:version 0.50
   ```

   This will create the new folder for the release, `versioned_docs/release-0.50`. You'll have to update the `_vars.json` and `compatibility.md` and
   make sure all the versions are correct.
   You also need to udpate the version list in `docusaurus.config.ts` and point the `latestVersion` to the versino being released.

3. Cut the request candidate tag from the main branch. The tag should be v0.50.0-rc1. Assuming the remote `origin` is the main envoyproxy/ai-gateway repository,
   the command to cut the tag is:

   ```
   git fetch origin # make sure you have the latest main branch locally.
   git tag v0.50.0-rc1 origin/main
   git push origin v0.50.0-rc1
   ```

   Pushing a tag will trigger the pipeline to build the release candidate image and the helm chart tagged with the release candidate tag.
   The release candidate image will be available in the Docker Hub.

4. Generate the release notes using the Claude Code `/release-notes` command (defined in `.claude/commands/release-notes`).
   This command analyzes commits since the last release, verifies features against the actual code, and generates
   the release notes JSON data file, MDX page, plain-markdown copy, and navigation updates for the docs site.
   Review the generated output and make any necessary adjustments.

5. The release candidate should be tested by the maintainers and the community. If there is any issue, the issue should be fixed in the main branch
   and the new rc tag should be created. For example, if there is an issue in the release candidate v0.50.0-rc1, replace `v0.50.0-rc1` with `v0.50.0-rc2`
   in the above command and repeat the process.

### Release Phase

1. Once the release candidate is stable, we will cut the release from the main branch, assuming that's exactly the same as the last release candidate.
   The command to cut the release is exactly the same as the release candidate:
   ```
   git fetch origin # make sure you have the latest main branch locally.
   git tag v0.50.0 origin/main
   git push origin v0.50.0
   ```
   Pushing a tag will trigger the pipeline to build the release image and the helm chart tagged with the release tag.
   The release image will be available in the Docker Hub.
2. The draft release note will be created in the GitHub repository after the pipeline is completed.
   Edit the release note nicely by hand to reflect the changes in the release.
3. Announce the release in the community.
4. Create `release/v0.50` branch from the tag for the future backports, bug fixes, etc.

### Backport Phase

1. If there is a bug fix or a security fix that needs to be backported to the previous release, maintainers should cherry-pick the commit and raise the PR to the `release/v0.50` branch.
   Which commit should be backported is up to the maintainers and on a case-by-case basis.
2. Once the PR is merged, the maintainers will decide when to cut the patch release. There's no need to wait for multiple backports to cut the patch release, etc.
   Do not cut the tag until all CI passes on the release/v0.50 branch.
3. The patch release should be cut from the `release/v0.50` branch. The command to cut the patch release is exactly the same as normal release:
   ```
   git fetch origin # make sure you have the latest release/v0.50 branch locally.
   git tag v0.50.1 origin/release/v0.50
   git push origin v0.50.1
   ```
   Pushing a tag will trigger the pipeline to build the patch release image and the helm chart tagged with the patch release tag.
   The patch release image will be available in the Docker Hub.
4. The draft release note will be created in the GitHub repository after the pipeline is completed.
   Edit the release note nicely by hand to reflect the changes in the release.
5. Update the documentation on the main branch to reflect the new version. This has the following items:
   - Change `v0.50.0` to `v0.50.1` in site/versioned_docs/version-0.50 directory.
   - Update the site/src/pages/release-notes.md to add the new release note.
