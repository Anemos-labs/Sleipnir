package repocheck

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// actionsAppID is the GitHub Actions app: the integration that reports the checks of a workflow.
const actionsAppID = 15368

type ruleset struct {
	Name        string `json:"name"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
	Conditions  struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	BypassActors []struct {
		ActorID    int    `json:"actor_id"`
		ActorType  string `json:"actor_type"`
		BypassMode string `json:"bypass_mode"`
	} `json:"bypass_actors"`
	Rules []struct {
		Type       string          `json:"type"`
		Parameters json.RawMessage `json:"parameters"`
	} `json:"rules"`
}

func loadRuleset(t *testing.T, file string) *ruleset {
	t.Helper()
	var r ruleset
	dec := json.NewDecoder(strings.NewReader(read(t, ".github/rulesets/"+file)))
	dec.DisallowUnknownFields() // the file is also imported by hand and posted to the API: stray keys are refused there
	if err := dec.Decode(&r); err != nil {
		t.Fatalf(".github/rulesets/%s is not a ruleset this repository understands: %v", file, err)
	}
	return &r
}

func (r *ruleset) rule(typ string) (json.RawMessage, bool) {
	for _, x := range r.Rules {
		if x.Type == typ {
			return x.Parameters, true
		}
	}
	return nil, false
}

// The ruleset names the checks that must pass. A check it names that no job has blocks every pull request for good; a
// job renamed without the ruleset leaves main unprotected against it. Both fail here instead.
func TestRulesetRequiresJobsOfCI(t *testing.T) {
	r := loadRuleset(t, "main.json")
	raw, ok := r.rule("required_status_checks")
	if !ok {
		t.Fatal(".github/rulesets/main.json has no required_status_checks rule: nothing would have to pass before a merge")
	}
	var p struct {
		Strict bool `json:"strict_required_status_checks_policy"`
		Checks []struct {
			Context       string `json:"context"`
			IntegrationID int    `json:"integration_id"`
		} `json:"required_status_checks"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if !p.Strict {
		t.Error("required_status_checks must be strict: a branch has to be up to date with main before it merges")
	}
	jobs := map[string]bool{}
	for _, j := range workflowNamed(t, "ci.yml").Jobs {
		if j.Name != "" && !strings.Contains(j.Name, "${{") {
			jobs[j.Name] = true
		}
	}
	var fixed []string
	for n := range jobs {
		fixed = append(fixed, strconv.Quote(n))
	}
	sort.Strings(fixed)
	jobList := strings.Join(fixed, ", ")
	if len(p.Checks) != 1 || p.Checks[0].Context != "ci-gate" || p.Checks[0].IntegrationID != actionsAppID {
		t.Errorf("the ruleset requires %+v; the design is the single check ci-gate from the GitHub Actions app (integration_id %d)", p.Checks, actionsAppID)
	}
	for _, c := range p.Checks {
		if !jobs[c.Context] {
			t.Errorf("the ruleset requires the check %q, but no job of ci.yml has that name (the jobs with a fixed name: %s): rename one of them, or every pull request waits for a check that never comes", c.Context, jobList)
		}
	}
}

// The design of the branch ruleset, pinned: a relaxation is a deliberate edit of this test too.
func TestMainRulesetDesign(t *testing.T) {
	r := loadRuleset(t, "main.json")
	if r.Target != "branch" || r.Enforcement != "active" {
		t.Errorf("main.json: target %q, enforcement %q; want a branch ruleset that is active", r.Target, r.Enforcement)
	}
	if strings.Join(r.Conditions.RefName.Include, ",") != "~DEFAULT_BRANCH" || len(r.Conditions.RefName.Exclude) != 0 {
		t.Errorf("main.json applies to %v (excluding %v); want ~DEFAULT_BRANCH", r.Conditions.RefName.Include, r.Conditions.RefName.Exclude)
	}
	if len(r.BypassActors) != 0 {
		t.Errorf("main.json has bypass actors %v; nobody, the owner included, bypasses main", r.BypassActors)
	}
	for _, typ := range []string{"deletion", "non_fast_forward", "required_linear_history", "pull_request", "required_status_checks", "code_scanning"} {
		if _, ok := r.rule(typ); !ok {
			t.Errorf("main.json has no %s rule", typ)
		}
	}
	raw, _ := r.rule("pull_request")
	var pr struct {
		Approvals     int      `json:"required_approving_review_count"`
		DismissStale  bool     `json:"dismiss_stale_reviews_on_push"`
		CodeOwners    bool     `json:"require_code_owner_review"`
		Threads       bool     `json:"required_review_thread_resolution"`
		MergeMethods  []string `json:"allowed_merge_methods"`
		LastPushApprv bool     `json:"require_last_push_approval"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		t.Fatal(err)
	}
	if strings.Join(pr.MergeMethods, ",") != "squash" {
		t.Errorf("allowed merge methods %v; want squash only (the pull request title is the commit subject the release is planned from)", pr.MergeMethods)
	}
	if !pr.DismissStale || !pr.Threads {
		t.Error("the pull_request rule must dismiss stale reviews on push and require resolved threads")
	}
	if pr.Approvals != 0 || pr.CodeOwners || pr.LastPushApprv {
		t.Error("with one maintainer, the pull_request rule cannot require an approval, a code owner's review or a last-push approval: nobody could merge")
	}
	raw, _ = r.rule("code_scanning")
	if !strings.Contains(string(raw), `"CodeQL"`) || !strings.Contains(string(raw), "high_or_higher") {
		t.Errorf("the code_scanning rule must require CodeQL at high_or_higher, got %s", raw)
	}
}

func TestTagsRulesetDesign(t *testing.T) {
	r := loadRuleset(t, "tags.json")
	if r.Target != "tag" || r.Enforcement != "active" {
		t.Errorf("tags.json: target %q, enforcement %q; want an active tag ruleset", r.Target, r.Enforcement)
	}
	if strings.Join(r.Conditions.RefName.Include, ",") != "refs/tags/v*" {
		t.Errorf("tags.json applies to %v; want refs/tags/v*", r.Conditions.RefName.Include)
	}
	for _, typ := range []string{"deletion", "update"} {
		if _, ok := r.rule(typ); !ok {
			t.Errorf("tags.json has no %s rule: a release tag could be moved or removed", typ)
		}
	}
	if _, ok := r.rule("creation"); ok {
		t.Error("tags.json blocks creation: the release workflow could not create a tag")
	}
	found := map[string]bool{}
	for _, b := range r.BypassActors {
		found[b.ActorType+":"+strconv.Itoa(b.ActorID)+":"+b.BypassMode] = true
	}
	for _, want := range []string{"Integration:15368:always", "RepositoryRole:5:always"} {
		if !found[want] {
			t.Errorf("tags.json lacks the bypass actor %s (the Actions app, and the repository admin role)", want)
		}
	}
}
