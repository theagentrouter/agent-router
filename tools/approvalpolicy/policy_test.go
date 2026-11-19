// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var testOwners = &owners{
	Maintainers: []person{
		{GitHub: "maint-a", Name: "Maintainer A", Company: "Acme"},
		{GitHub: "maint-b", Name: "Maintainer B", Company: "Globex"},
	},
	Reviewers: []person{
		{GitHub: "rev-acme1", Name: "Reviewer Acme 1", Company: "Acme"},
		{GitHub: "rev-acme2", Name: "Reviewer Acme 2", Company: "Acme"},
		{GitHub: "rev-acme3", Name: "Reviewer Acme 3", Company: "acme "},
		{GitHub: "rev-globex", Name: "Reviewer Globex", Company: "Globex"},
		{GitHub: "rev-indie1", Name: "Reviewer Indie 1", Company: "Independent"},
		{GitHub: "rev-indie2", Name: "Reviewer Indie 2", Company: "independent"},
	},
	EmeritusReviewers: []person{
		{GitHub: "old-rev", Name: "Old Reviewer", Company: "Initech"},
	},
}

func rv(login, state string) review {
	r := review{State: state}
	r.User = &struct {
		Login string `json:"login"`
	}{Login: login}
	return r
}

func TestEvaluate(t *testing.T) {
	for _, tc := range []struct {
		name         string
		author       string
		reviews      []review
		wantApproved bool
		wantMessage  string
	}{
		{
			name:        "no reviews",
			author:      "outsider",
			wantMessage: "Needs an approval from 1 maintainer or from 2 reviewers. Reviewer approvals so far: none.",
		},
		{
			name:         "maintainer approval",
			author:       "outsider",
			reviews:      []review{rv("maint-a", "APPROVED")},
			wantApproved: true,
			wantMessage:  "Approved by maintainer @maint-a.",
		},
		{
			name:         "two maintainer approvals",
			author:       "outsider",
			reviews:      []review{rv("maint-b", "APPROVED"), rv("maint-a", "APPROVED")},
			wantApproved: true,
			wantMessage:  "Approved by maintainers @maint-a and @maint-b.",
		},
		{
			name:        "author's own approval does not count",
			author:      "maint-a",
			reviews:     []review{rv("maint-a", "APPROVED")},
			wantMessage: "Reviewer approvals so far: none.",
		},
		{
			name:         "maintainer author approved by another maintainer",
			author:       "maint-a",
			reviews:      []review{rv("maint-b", "APPROVED")},
			wantApproved: true,
			wantMessage:  "Approved by maintainer @maint-b.",
		},
		{
			name:        "one reviewer is not enough",
			author:      "outsider",
			reviews:     []review{rv("rev-globex", "APPROVED")},
			wantMessage: "Reviewer approvals so far: @rev-globex.",
		},
		{
			name:         "two reviewers, one at a different company from a listed author",
			author:       "rev-acme1",
			reviews:      []review{rv("rev-acme2", "APPROVED"), rv("rev-globex", "APPROVED")},
			wantApproved: true,
			wantMessage:  "Approved by reviewers @rev-acme2 and @rev-globex.",
		},
		{
			name:        "two reviewers at the same company as a listed author",
			author:      "rev-acme1",
			reviews:     []review{rv("rev-acme2", "APPROVED"), rv("rev-acme3", "APPROVED")},
			wantMessage: "Approved by reviewers @rev-acme2 and @rev-acme3, who work at Acme, the same company as the author.",
		},
		{
			name:        "maintainer author's company counts",
			author:      "maint-a",
			reviews:     []review{rv("rev-acme1", "APPROVED"), rv("rev-acme2", "APPROVED")},
			wantMessage: "the same company as the author",
		},
		{
			name:         "emeritus author's company counts",
			author:       "old-rev",
			reviews:      []review{rv("rev-acme1", "APPROVED"), rv("rev-acme2", "APPROVED")},
			wantApproved: true,
		},
		{
			name:         "independent author approved by two reviewers from one company",
			author:       "rev-indie1",
			reviews:      []review{rv("rev-acme1", "APPROVED"), rv("rev-acme2", "APPROVED")},
			wantApproved: true,
		},
		{
			name:        "unlisted author needs reviewers from two companies",
			author:      "outsider",
			reviews:     []review{rv("rev-acme1", "APPROVED"), rv("rev-acme2", "APPROVED")},
			wantMessage: "who all work at Acme. The author isn't listed in OWNERS",
		},
		{
			name:         "unlisted author approved by reviewers from two companies",
			author:       "outsider",
			reviews:      []review{rv("rev-acme1", "APPROVED"), rv("rev-globex", "APPROVED")},
			wantApproved: true,
		},
		{
			name:         "independent reviewers count as different companies",
			author:       "outsider",
			reviews:      []review{rv("rev-indie1", "APPROVED"), rv("rev-indie2", "APPROVED")},
			wantApproved: true,
		},
		{
			name:   "a later change request withdraws an approval",
			author: "outsider",
			reviews: []review{
				rv("rev-acme1", "APPROVED"), rv("rev-globex", "APPROVED"), rv("rev-globex", "CHANGES_REQUESTED"),
			},
			wantMessage: "Reviewer approvals so far: @rev-acme1.",
		},
		{
			name:   "a later comment keeps an approval",
			author: "outsider",
			reviews: []review{
				rv("rev-acme1", "APPROVED"), rv("rev-globex", "APPROVED"), rv("rev-globex", "COMMENTED"),
			},
			wantApproved: true,
		},
		{
			name:         "approving after requesting changes counts",
			author:       "outsider",
			reviews:      []review{rv("maint-a", "CHANGES_REQUESTED"), rv("maint-a", "APPROVED")},
			wantApproved: true,
		},
		{
			name:        "dismissed approval does not count",
			author:      "outsider",
			reviews:     []review{rv("maint-a", "DISMISSED")},
			wantMessage: "Reviewer approvals so far: none.",
		},
		{
			name:         "logins are compared case-insensitively",
			author:       "outsider",
			reviews:      []review{rv("MAINT-A", "APPROVED")},
			wantApproved: true,
		},
		{
			name:        "approvals from unlisted and emeritus people do not count",
			author:      "outsider",
			reviews:     []review{rv("someone", "APPROVED"), rv("old-rev", "APPROVED")},
			wantMessage: "Reviewer approvals so far: none.",
		},
		{
			name:        "reviews from deleted accounts are ignored",
			author:      "outsider",
			reviews:     []review{{State: "APPROVED"}},
			wantMessage: "Reviewer approvals so far: none.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := evaluate(testOwners, tc.author, tc.reviews)
			require.Equal(t, tc.wantApproved, d.approved, d.message)
			require.Contains(t, d.message, tc.wantMessage)
		})
	}
}

func TestParseOwners(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		wantErr string
	}{
		{
			name: "valid",
			in: `
maintainers:
  - {github: a, name: A, company: Acme, areas: [everything]}
reviewers:
  - {github: b, name: B, company: Independent}
emeritus-maintainers:
  - {github: c, name: C}
emeritus-reviewers: []
`,
		},
		{
			name:    "unknown field",
			in:      "maintainers:\n  - {github: a, name: A, company: Acme, employer: Acme}\n",
			wantErr: "unknown field",
		},
		{
			name:    "unknown section",
			in:      "approvers: []\n",
			wantErr: "unknown field",
		},
		{
			name:    "missing github",
			in:      "reviewers:\n  - {name: A, company: Acme}\n",
			wantErr: "reviewers[0]: github is required",
		},
		{
			name:    "missing name",
			in:      "reviewers:\n  - {github: a, company: Acme}\n",
			wantErr: "@a needs a name",
		},
		{
			name:    "active role without company",
			in:      "reviewers:\n  - {github: a, name: A}\n",
			wantErr: "@a needs a company",
		},
		{
			name: "listed twice",
			in: `
maintainers:
  - {github: a, name: A, company: Acme}
reviewers:
  - {github: A, name: A, company: Acme}
`,
			wantErr: "@A is listed in both maintainers and reviewers",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseOwners([]byte(tc.in))
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
		})
	}
}

// TestRepoOWNERS keeps the repository's OWNERS file valid and MAINTAINERS.md in sync with it.
func TestRepoOWNERS(t *testing.T) {
	data, err := os.ReadFile("../../OWNERS")
	require.NoError(t, err)
	o, err := parseOwners(data)
	require.NoError(t, err)
	require.NotEmpty(t, o.Maintainers)

	maintainersMD, err := os.ReadFile("../../MAINTAINERS.md")
	require.NoError(t, err)
	md := strings.ToLower(string(maintainersMD))
	for _, p := range append(o.Maintainers, o.Reviewers...) {
		require.Contains(t, md, "[@"+strings.ToLower(p.GitHub)+"]",
			"@%s is in OWNERS but not in MAINTAINERS.md", p.GitHub)
	}
}
