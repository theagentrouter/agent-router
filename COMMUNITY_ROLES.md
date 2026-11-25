# Community Roles

This document describes the roles in the Agent Router community, what each role can do, what
it is responsible for, and how people move from one role to the next:

**Contributor → Reviewer → Maintainer**

The [Technical Charter](Technical_Charter.pdf) gives default definitions of Contributors and
Maintainers, and section 2.c lets the Technical Steering Committee (TSC) adopt or modify roles as
long as they are documented in the CONTRIBUTING file. This document replaces those defaults with
the three roles below. The TSC adopts and amends it under section 2 of the charter.

Contributors are the members of the `@theagentrouter/agent-router-assignable` GitHub team.
[MAINTAINERS.md](MAINTAINERS.md) lists the Reviewers and Maintainers, with each person's company
and areas.

You don't need a role to take part. Anyone can open an issue, send a pull request, review code,
improve documentation, answer questions, or join a community meeting, with no sign-up. Read
[CONTRIBUTING.md](CONTRIBUTING.md) to get started.

## At a glance

| Role        | GitHub access | Part in pull request review | Can merge                   | TSC vote |
| ----------- | ------------- | --------------------------- | --------------------------- | -------- |
| Contributor | Read          | Comments                    | No                          | No       |
| Reviewer    | Write         | First review                | After a Maintainer approves | No       |
| Maintainer  | Maintain      | Final approval              | Yes                         | Yes      |

The numbers in this document are typical, not minimums. Quality matters more than quantity: a
strong candidate is not held back by a count, and meeting a number does not guarantee a
nomination.

## Contributor

A Contributor is someone who contributes regularly and wants to take on issues and reviews.

**Privileges**

- Membership in the `@theagentrouter/agent-router-assignable` team, which gives Read access to
  the repository. Issues and pull requests can be assigned to you, and you can be requested as a
  reviewer.

**Responsibilities**

- Follow [CONTRIBUTING.md](CONTRIBUTING.md) and the project's code of conduct.
- Own the issues assigned to you. If you can't continue, say so and unassign yourself.
- Respond to review feedback on your pull requests.
- Review other people's pull requests in areas you know. Your reviews are how you build toward
  the Reviewer role.

**Typical expectations**

- About 5 merged pull requests, or a similar amount of documentation, testing, or triage work.
- A Maintainer who agrees to sponsor you.

**How to become one**

Ask a Maintainer, or a Maintainer can invite you. One Maintainer's sponsorship is enough. The
sponsoring Maintainer asks an organization admin to add you to the
`@theagentrouter/agent-router-assignable` team.

**Stepping back**

Contributors are removed from the team after about 12 months without activity. You can ask to
rejoin at any time.

## Reviewer

A Reviewer does the first, detailed review of pull requests, so that Maintainers can give the
final approval quickly and spend more of their time on design, API, and release work.

**Privileges**

- Write access to the repository through the `@theagentrouter/agent-router-reviewers` team.
- First review on pull requests in your areas. Maintainers rely on your review when they give
  the final approval.
- Merge pull requests once a Maintainer has approved them and CI passes.
- Start CI for pull requests from first-time contributors (the **Approve and run workflows**
  button), and re-run failed jobs.
- Triage: label, assign, close, and reopen issues and pull requests, and set milestones.

**Limits**

- Every pull request needs a Maintainer's approval to merge. A Reviewer's approval is not enough
  on its own.
- Reviewers do not merge their own pull requests.
- Reviewers work from forks. Only Maintainers create branches or tags in the main repository,
  and only Maintainers cut releases.
- Reviewers do not vote on the TSC.

**Responsibilities**

- Give a first review within about 3 business days on pull requests in your areas.
- Hold pull requests to [CONTRIBUTING.md](CONTRIBUTING.md): DCO sign-off, the pull request
  template, tests for new code paths, and the generative AI policy.
- Approve only changes you understand and would be comfortable maintaining. Your approval tells
  a Maintainer the change is ready for a final look. If a pull request raises API, design, or
  security questions, say so and bring in a Maintainer.
- You can use AI tools to help with a review if you say so in the review. You are responsible
  for every comment and approval you post.
- When you merge, clean up the squash commit title and description as CONTRIBUTING.md
  describes.
- Triage new issues and pull requests in your areas.
- Help newcomers, and point out good candidates for the Contributor and Reviewer roles.
- Keep your company and areas in [MAINTAINERS.md](MAINTAINERS.md) up to date.

**What we look for**

- Reviews that catch real problems, or confirm that a change is correct with evidence: checked
  against the spec or the code it depends on, or run.
- Good judgment about when to approve and when to bring in a Maintainer.
- Follow-through: responds to feedback, finishes what they start, and files clear issues.
- Helps other contributors and the wider community.
- Solid knowledge of at least one area, such as the control plane, the data plane, MCP,
  translation, observability, or the documentation site.

For reference, most Reviewers have about 3 months of steady activity, about 10 substantial
merged pull requests, and about 15 meaningful reviews of other people's pull requests. A
Maintainer can nominate someone earlier when the quality of their work makes the case. The
nomination should link the reviews and contributions that show it, and the Maintainers who +1 it
should have read them.

**How to become one**

1. A Maintainer opens a nomination issue using the **Nominate a Reviewer** template, listing the
   nominee's pull requests and reviews. Nominees can ask a Maintainer to nominate them.
2. The nominee comments to accept.
3. The nomination needs a +1 from at least two Maintainers who work at two different companies,
   and stays open for at least 7 days. Any Maintainer can object. Objections are discussed in
   the issue, and the TSC decides if they cannot be resolved.
4. A Maintainer opens a pull request that adds the nominee to the Reviewers section of
   [MAINTAINERS.md](MAINTAINERS.md). Once it merges, an organization admin adds them to the
   Reviewers team.

Individual Reviewer nominations do not need a TSC vote.

**Stepping back**

Reviewers can step down at any time by opening a pull request that moves them to Emeritus
Reviewers in [MAINTAINERS.md](MAINTAINERS.md). A Reviewer with no reviews for 6 months is
contacted by a Maintainer and, if they don't plan to return, moved to emeritus. An emeritus
Reviewer can be reinstated with a +1 from two Maintainers, without a new nomination.

## Maintainer

Maintainers are responsible for the project as a whole. Under the charter, Maintainers are the
voting members of the TSC.

**Privileges**

- Maintain access to the repository, and code owner for every path.
- Give the approval a pull request needs to merge.
- Cut releases, push release tags, and manage release branches.
- Handle security reports and advisories as described in [SECURITY.md](SECURITY.md).
- Vote on TSC decisions.

**Responsibilities**

- Give the final approval on pull requests. When a Reviewer has already reviewed, focus on
  whether the change fits the project. Review API, CI, governance, and release changes in full.
- Own the project's technical direction: design proposals, the roadmap, and deprecations.
- Run releases and backports.
- Respond to security reports.
- Take part in community meetings and TSC decisions.
- Sponsor Contributors, mentor Reviewers, and nominate Reviewers and Maintainers.

**What we look for**

- A record as a Reviewer whose reviews Maintainers trust, including being the main reviewer on
  many pull requests.
- Good judgment on design, shown by writing or shepherding a design proposal.
- Knowledge of more than one area of the project.
- Reliable follow-through, and care for the community: mentoring, triage, and welcoming new
  contributors.

For reference, most Maintainers have spent about 3 to 6 months as a Reviewer, or have an
equivalent record of sustained contribution. The TSC can approve someone earlier when the quality
of their work makes the case.

**How to become one**

1. A Maintainer opens a nomination issue using the **Nominate a Maintainer** template.
2. The nominee comments to accept.
3. The TSC votes, as section 2 of the charter requires: a Contributor becomes a Maintainer with
   a majority approval of the TSC. A vote held without a meeting needs a majority of all TSC
   voting members.
4. A Maintainer opens a pull request that moves the nominee to the Maintainers section of
   [MAINTAINERS.md](MAINTAINERS.md). Once it merges, an organization admin updates their GitHub
   access.

**Stepping back**

Maintainers can step down at any time by opening a pull request that moves them to Emeritus
Maintainers in [MAINTAINERS.md](MAINTAINERS.md). The TSC can move an inactive Maintainer to
emeritus by majority vote, as the charter allows. An emeritus Maintainer can return through a
TSC vote.

## How pull requests get approved

A pull request into `main` or a `release/**` branch can merge when all of the following are true:

1. Required CI checks pass.
2. A Maintainer has approved it. [CODEOWNERS](CODEOWNERS) makes the Maintainers the code owners
   of every path, and GitHub requires a code owner's approval.
3. Nobody with Write access has an open **Request changes** review on it.

Usually a Reviewer reviews first and a Maintainer gives the final approval. This is how the work
is shared, not a rule: a Maintainer can review and approve directly, for example when no Reviewer
covers the area or a fix is urgent.

The author's own approval never counts. Approvals stay valid when new commits are pushed, so the
person who merges should check that the final diff is what was approved.

Anyone is welcome to review any pull request. Reviews from people who don't hold a role are
valued, and they are how people grow into the Reviewer role.
