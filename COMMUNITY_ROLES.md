# Community Roles

This document describes the roles in the Agent Router community, what each role can do, what
it is responsible for, and how people move from one role to the next:

**Contributor → Frequent Contributor → Reviewer → Maintainer**

The [Technical Charter](Technical_Charter.pdf) defines Contributors and Maintainers and lets the
Technical Steering Committee (TSC) add roles as long as they are documented from the
CONTRIBUTING file. The TSC adopts and amends this document under section 2 of the charter.

The [OWNERS](OWNERS) file is the source of truth for who holds the Reviewer and Maintainer
roles, which company each person works for, and their areas. [MAINTAINERS.md](MAINTAINERS.md)
is the readable version of the same list.

## At a glance

| Role                 | GitHub access | Can approve PRs                                         | Can merge PRs | TSC vote |
| -------------------- | ------------- | ------------------------------------------------------- | ------------- | -------- |
| Contributor          | None needed   | Comments only                                           | No            | No       |
| Frequent Contributor | Read          | Comments only                                           | No            | No       |
| Reviewer             | Write         | Yes. Two Reviewer approvals equal one from a Maintainer | Yes           | No       |
| Maintainer           | Maintain      | Yes, on their own                                       | Yes           | Yes      |

The numbers in this document are typical, not minimums. Quality matters more than quantity: a
strong candidate is not held back by a count, and meeting a number does not guarantee a
nomination.

## Contributor

Anyone who opens an issue, sends a pull request, reviews code, improves documentation, answers
questions, or takes part in community meetings is a Contributor. No sign-up is needed. Read
[CONTRIBUTING.md](CONTRIBUTING.md) to get started.

## Frequent Contributor

A Frequent Contributor is someone who has started contributing regularly and wants to take on
issues and reviews.

**Privileges**

- Read access to the repository through the project's GitHub organization, which means issues
  and pull requests can be assigned to you and you can be requested as a reviewer.

**Responsibilities**

- Follow [CONTRIBUTING.md](CONTRIBUTING.md) and the project's code of conduct.
- Own the issues assigned to you. If you can't continue, say so and unassign yourself.
- Respond to review feedback on your pull requests.
- Review other people's pull requests in areas you know. Your comments do not count as
  approvals, but they are how you build toward the Reviewer role.

**Typical expectations**

- About 5 merged pull requests, or a similar amount of documentation, testing, or triage work.
- A Maintainer who agrees to sponsor you.

**How to become one**

Ask a Maintainer, or a Maintainer can invite you. One Maintainer's sponsorship is enough.

**Stepping back**

Frequent Contributor access is removed after about 12 months without activity. You can ask for
it again at any time.

## Reviewer

A Reviewer takes on first-line review so that Maintainers can focus on design, API, and release
work. Two Reviewer approvals count the same as one Maintainer approval.

**Privileges**

- Write access to the repository through the `@theagentrouter/agent-router-reviewers` team.
- Approvals that count toward merging. See [How pull requests get approved](#how-pull-requests-get-approved).
- Merge pull requests once they meet the approval rules and CI passes.
- Start CI for pull requests from first-time contributors (the **Approve and run workflows**
  button), and re-run failed jobs.
- Triage: label, assign, close, and reopen issues and pull requests, and set milestones.

**Limits**

- A Maintainer must approve changes to Maintainer-only paths: the API (`/api/`), CI and
  repository automation (`/.github/`), design proposals (`/docs/proposals/`), and governance
  files such as this one. [CODEOWNERS](CODEOWNERS) has the full list.
- A Maintainer must approve pull requests into `release/**` branches.
- Reviewers do not merge their own pull requests.
- Reviewers work from forks. Only Maintainers create branches or tags in the main repository,
  and only Maintainers cut releases.
- Reviewers do not vote on the TSC.

**Responsibilities**

- Give a first review within about 3 business days on pull requests in your areas.
- Hold pull requests to [CONTRIBUTING.md](CONTRIBUTING.md): DCO sign-off, the pull request
  template, tests for new code paths, and the generative AI policy.
- Approve only changes you understand and would be comfortable maintaining. If a pull request
  raises API, design, or security questions, bring in a Maintainer instead of approving.
- You can use AI tools to help with a review if you say so in the review. You are responsible
  for every comment and approval you post.
- When you merge, clean up the squash commit title and description as CONTRIBUTING.md
  describes.
- Triage new issues and pull requests in your areas.
- Help new contributors, and point out good candidates for Frequent Contributor and Reviewer.
- Keep your company and areas in [OWNERS](OWNERS) up to date.

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
4. A Maintainer opens a pull request that adds the nominee to [OWNERS](OWNERS) and
   [MAINTAINERS.md](MAINTAINERS.md). Once it merges, an organization admin adds them to the
   Reviewers team.

Individual Reviewer nominations do not need a TSC vote.

**Stepping back**

Reviewers can step down at any time by opening a pull request that moves them to
`emeritus-reviewers` in [OWNERS](OWNERS). A Reviewer with no reviews for 6 months is contacted by
a Maintainer and, if they don't plan to return, moved to emeritus. An emeritus Reviewer can be
reinstated with a +1 from two Maintainers, without a new nomination.

## Maintainer

Maintainers are responsible for the project as a whole. Under the charter, Maintainers are the
voting members of the TSC.

**Privileges**

- Maintain access to the repository, and code owner for every path, including the
  Maintainer-only paths.
- A single Maintainer approval is enough to merge, subject to CI and CODEOWNERS.
- Cut releases, push release tags, and manage release branches.
- Handle security reports and advisories as described in [SECURITY.md](SECURITY.md).
- Vote on TSC decisions.

**Responsibilities**

- Review and merge pull requests, and give the final review for API, CI, governance, and
  release changes.
- Own the project's technical direction: design proposals, the roadmap, and deprecations.
- Run releases and backports.
- Respond to security reports.
- Take part in community meetings and TSC decisions.
- Mentor Reviewers, and nominate Frequent Contributors, Reviewers, and Maintainers.

**What we look for**

- A record as a Reviewer whose approvals Maintainers trust, including being the main reviewer on
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
4. A Maintainer opens a pull request that moves the nominee to `maintainers` in
   [OWNERS](OWNERS) and updates [MAINTAINERS.md](MAINTAINERS.md). Once it merges, an organization
   admin updates their GitHub access.

**Stepping back**

Maintainers can step down at any time by opening a pull request that moves them to
`emeritus-maintainers`. The TSC can move an inactive Maintainer to emeritus by majority vote, as
the charter allows. An emeritus Maintainer can return through a TSC vote.

## How pull requests get approved

A pull request into `main` can merge when all of the following are true:

1. Required CI checks pass.
2. The `approval-policy` check passes. It needs either:
   - an approval from at least one Maintainer, or
   - approvals from at least two Reviewers, at least one of whom works at a different company
     from the pull request author. If the author is not listed in [OWNERS](OWNERS), the two
     Reviewers must work at two different companies.
3. If the pull request changes a Maintainer-only path, a Maintainer has approved it.
   [CODEOWNERS](CODEOWNERS) enforces this.
4. Nobody with Write access has an open **Request changes** review on it.

The author's own approval never counts. Approvals stay valid when new commits are pushed, so the
person who merges should check that the final diff is what was approved.

Pull requests into `release/**` branches need a Maintainer's approval.

Anyone is welcome to review any pull request. Reviews from people who do not hold the Reviewer or
Maintainer role don't count toward merging, but they are valued and are how people grow into
those roles.
