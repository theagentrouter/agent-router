// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// person is an entry in the OWNERS file.
type person struct {
	GitHub  string   `json:"github"`
	Name    string   `json:"name"`
	Company string   `json:"company,omitempty"`
	Areas   []string `json:"areas,omitempty"`
}

// owners is the parsed OWNERS file.
type owners struct {
	Maintainers         []person `json:"maintainers"`
	Reviewers           []person `json:"reviewers"`
	EmeritusMaintainers []person `json:"emeritus-maintainers"`
	EmeritusReviewers   []person `json:"emeritus-reviewers"`
}

// parseOwners parses and validates the OWNERS file. Unknown fields are rejected so that a typo
// can't silently drop someone from a role.
func parseOwners(data []byte) (*owners, error) {
	var o owners
	if err := yaml.UnmarshalStrict(data, &o); err != nil {
		return nil, fmt.Errorf("parse OWNERS: %w", err)
	}
	seen := make(map[string]string)
	for _, g := range o.groups() {
		for i, p := range g.people {
			if p.GitHub == "" {
				return nil, fmt.Errorf("OWNERS %s[%d]: github is required", g.role, i)
			}
			if p.Name == "" {
				return nil, fmt.Errorf("OWNERS %s: @%s needs a name", g.role, p.GitHub)
			}
			if g.active && strings.TrimSpace(p.Company) == "" {
				return nil, fmt.Errorf("OWNERS %s: @%s needs a company (use Independent if none)", g.role, p.GitHub)
			}
			key := strings.ToLower(p.GitHub)
			if prev, ok := seen[key]; ok {
				return nil, fmt.Errorf("OWNERS: @%s is listed in both %s and %s", p.GitHub, prev, g.role)
			}
			seen[key] = g.role
		}
	}
	return &o, nil
}

type group struct {
	role   string
	people []person
	active bool
}

func (o *owners) groups() []group {
	return []group{
		{role: "maintainers", people: o.Maintainers, active: true},
		{role: "reviewers", people: o.Reviewers, active: true},
		{role: "emeritus-maintainers", people: o.EmeritusMaintainers},
		{role: "emeritus-reviewers", people: o.EmeritusReviewers},
	}
}

// lookup returns the OWNERS entry for login from any group, including emeritus.
func (o *owners) lookup(login string) (person, bool) {
	for _, g := range o.groups() {
		if p, ok := find(g.people, login); ok {
			return p, true
		}
	}
	return person{}, false
}

func find(people []person, login string) (person, bool) {
	for _, p := range people {
		if strings.EqualFold(p.GitHub, login) {
			return p, true
		}
	}
	return person{}, false
}

// affiliation is the key used to compare companies. People who are independent each count as
// their own affiliation, so two independent reviewers count as two different companies.
func affiliation(p person) string {
	c := strings.ToLower(strings.TrimSpace(p.Company))
	if c == "" || c == "independent" {
		return "independent/" + strings.ToLower(p.GitHub)
	}
	return c
}

// review is a pull request review as returned by the GitHub REST API.
type review struct {
	User *struct {
		Login string `json:"login"`
	} `json:"user"`
	State string `json:"state"`
}

type decision struct {
	approved bool
	message  string
}

// evaluate applies the approval rules in COMMUNITY_ROLES.md. reviews must be in the order the
// GitHub API returns them, oldest first.
func evaluate(o *owners, author string, reviews []review) decision {
	// As on GitHub, each person's most recent approval, change request, or dismissal decides
	// whether they currently approve. Comment-only reviews don't change it.
	latest := make(map[string]string)
	for _, r := range reviews {
		if r.User == nil {
			continue // Deleted accounts.
		}
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			latest[strings.ToLower(r.User.Login)] = r.State
		}
	}

	var maintainers, reviewers []person
	for login, state := range latest {
		if state != "APPROVED" || strings.EqualFold(login, author) {
			continue
		}
		if p, ok := find(o.Maintainers, login); ok {
			maintainers = append(maintainers, p)
		} else if p, ok := find(o.Reviewers, login); ok {
			reviewers = append(reviewers, p)
		}
	}
	byLogin := func(a, b person) int { return strings.Compare(strings.ToLower(a.GitHub), strings.ToLower(b.GitHub)) }
	slices.SortFunc(maintainers, byLogin)
	slices.SortFunc(reviewers, byLogin)

	if len(maintainers) > 0 {
		return decision{approved: true, message: fmt.Sprintf("Approved by %s %s.", plural(len(maintainers), "maintainer"), handles(maintainers))}
	}
	if len(reviewers) < 2 {
		return decision{message: fmt.Sprintf("Needs an approval from 1 maintainer or from 2 reviewers. Reviewer approvals so far: %s.", handles(reviewers))}
	}

	const needMore = "One more approval from a reviewer at another company, or an approval from a maintainer, is needed."
	if a, known := o.lookup(author); known {
		for _, r := range reviewers {
			if affiliation(r) != affiliation(a) {
				return decision{approved: true, message: fmt.Sprintf("Approved by reviewers %s.", handles(reviewers))}
			}
		}
		return decision{message: fmt.Sprintf("Approved by reviewers %s, who work at %s, the same company as the author. %s",
			handles(reviewers), a.Company, needMore)}
	}

	companies := make(map[string]struct{})
	for _, r := range reviewers {
		companies[affiliation(r)] = struct{}{}
	}
	if len(companies) >= 2 {
		return decision{approved: true, message: fmt.Sprintf("Approved by reviewers %s.", handles(reviewers))}
	}
	return decision{message: fmt.Sprintf("Approved by reviewers %s, who all work at %s. The author isn't listed in OWNERS, so the approving reviewers must work at two different companies. %s",
		handles(reviewers), reviewers[0].Company, needMore)}
}

func handles(people []person) string {
	if len(people) == 0 {
		return "none"
	}
	h := make([]string, len(people))
	for i, p := range people {
		h[i] = "@" + p.GitHub
	}
	if len(h) == 1 {
		return h[0]
	}
	return strings.Join(h[:len(h)-1], ", ") + " and " + h[len(h)-1]
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
