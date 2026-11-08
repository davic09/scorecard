// Copyright 2026 OpenSSF Scorecard Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gitlabrepo_test

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/ossf/scorecard/v5/checker"
	"github.com/ossf/scorecard/v5/checks"
	"github.com/ossf/scorecard/v5/checks/evaluation"
	"github.com/ossf/scorecard/v5/checks/raw"
	"github.com/ossf/scorecard/v5/clients"
	"github.com/ossf/scorecard/v5/clients/gitlabrepo"
	mockrepo "github.com/ossf/scorecard/v5/clients/mockclients"
	"github.com/ossf/scorecard/v5/probes/codeApproved"
	scut "github.com/ossf/scorecard/v5/utests"
)

// Synthetic default-branch history for one approved squash merge, one approved
// ordinary merge, and one unapproved self-merge. The squash commit is on the
// branch. The GraphQL commits connection contains only the pre-squash SHA,
// which is not on the branch. That is the shape GitLab returned for sylva-core
// !9820: mergeCommitSha linked, commits.nodes was a different SHA, and the
// squash commit was a separate default-branch commit.
const (
	squashMergeSHA     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	squashCommitSHA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2"
	preSquashSHA       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa3"
	ordinaryMergeSHA   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb1"
	ordinaryCommitSHA  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb2"
	unapprovedMergeSHA = "ccccccccccccccccccccccccccccccccccccccc1"

	squashMRIID     = "10"
	ordinaryMRIID   = "11"
	unapprovedMRIID = "12"
)

// TestSquashCommitLowersCodeReviewScore runs the production GitLab matcher and
// the production Code-Review score on a squash merge. getChangesets groups
// commits that already carry the same merged MR number into one changeset
// (checks/raw/code_review_test.go). codeApproved counts a changeset with an
// empty author as unapproved and does not drop it: bot skipping applies only
// to approved changesets whose author is marked a bot, and the GitLab matcher
// never sets IsBot. This test fails while the squash SHA stays unassociated,
// because the final approved/total ratio changes. The failure is the reproduction.
func TestSquashCommitLowersCodeReviewScore(t *testing.T) {
	t.Parallel()

	zipped := gitlabrepo.ZipCommitsForTest(squashFixtureCommits(), squashFixtureMRs())
	baseline := scoreCommits(t, zipped)
	grouped := scoreCommits(t, copyAssociation(zipped, squashMergeSHA, squashCommitSHA))

	if grouped.approved != 2 || grouped.total != 3 || grouped.score != 6 {
		t.Fatalf("associated squash commit did not score as one MR changeset: got %d/%d score %d\n%s",
			grouped.approved, grouped.total, grouped.score, formatChangesets(grouped.changesets))
	}
	squashGrouped, ok := changesetContaining(grouped.changesets, squashCommitSHA)
	if !ok || squashGrouped.RevisionID != squashMRIID || !changesetHasSHA(squashGrouped, squashMergeSHA) {
		t.Fatalf("associated squash commit was not grouped with its merge commit: %+v", squashGrouped)
	}
	assertControlChangesets(t, baseline.changesets)

	if baseline.approved == grouped.approved && baseline.total == grouped.total && baseline.score == grouped.score {
		squashCS, found := changesetContaining(baseline.changesets, squashCommitSHA)
		if found && squashCS.RevisionID == squashMRIID {
			return
		}
	}

	t.Fatalf("baseline Code-Review counts the unlinked squash commit as its own unreviewed changeset.\n"+
		"observed baseline: %d/%d score %d reason %q\n"+
		"expected once the squash SHA carries the merge commit's MR association: %d/%d score %d reason %q\n"+
		"baseline changesets:\n%s",
		baseline.approved, baseline.total, baseline.score, baseline.reason,
		grouped.approved, grouped.total, grouped.score, grouped.reason,
		formatChangesets(baseline.changesets))
}

// TestSylvaPinnedWindowCodeReviewBaseline replays the public sylva-core window
// at 976afa003dd07fc663ec579c2a7d25a24a4eac9b through zip and Code-Review.
// It records the baseline score. It does not treat that score as correct.
// restSquashCommitSha is not a GraphQL field zip reads; the second score copies
// the merge commit association onto those REST squash SHAs.
func TestSylvaPinnedWindowCodeReviewBaseline(t *testing.T) {
	t.Parallel()

	commits, mrs, squashToMerge := loadSylvaWindow(t)
	zipped := gitlabrepo.ZipCommitsForTest(commits, mrs)
	baseline := scoreCommits(t, zipped)
	linked := countAssociated(zipped)
	if linked != 17 || baseline.approved != 10 || baseline.total != 27 || baseline.score != 3 {
		t.Fatalf("pinned window baseline got associated=%d score=%d/%d result=%d reason %q; want associated=17 and 10/27 score 3",
			linked, baseline.approved, baseline.total, baseline.score, baseline.reason)
	}

	const (
		merge9820    = "19fc389efa77be359f9c6e4d28669888aec9fd6f"
		squash9820   = "1d1a44475dd576aa72b172061233ea1527e2d8d5"
		merge9895    = "976afa003dd07fc663ec579c2a7d25a24a4eac9b"
		squash9895   = "cd71ea3b7fed7280836c2ac1fefda0069f71c8f0"
		thanos       = "9ee3a78bb97d7161ff1dfb04e9d1edb5be6e1f02"
		linkedSquash = "914834ac48d179b6a9213de8adffc4b276446ca0"
	)
	if got := associatedNumber(zipped, merge9820); got != 9820 {
		t.Fatalf("MR 9820 merge commit association = %d, want 9820", got)
	}
	if got := associatedNumber(zipped, squash9820); got != 0 {
		t.Fatalf("MR 9820 squash commit association = %d, want 0", got)
	}
	if got := associatedNumber(zipped, merge9895); got != 9895 {
		t.Fatalf("MR 9895 merge commit association = %d, want 9895", got)
	}
	if got := associatedNumber(zipped, squash9895); got != 0 {
		t.Fatalf("MR 9895 squash commit association = %d, want 0", got)
	}
	if got := associatedNumber(zipped, thanos); got != 0 {
		t.Fatalf("update_thanos_defaults merge association = %d, want 0", got)
	}
	if got := associatedNumber(zipped, linkedSquash); got != 9879 {
		t.Fatalf("squash SHA present in commits.nodes association = %d, want 9879", got)
	}
	mr9895, ok := changesetContaining(baseline.changesets, merge9895)
	if !ok || mr9895.RevisionID != "9895" || nonAuthorApproved(mr9895) {
		t.Fatalf("MR 9895 changeset = %+v, want linked and unapproved", mr9895)
	}

	linkedSquashes := associateSquashSHAs(zipped, squashToMerge)
	counterfactual := scoreCommits(t, linkedSquashes)
	if countAssociated(linkedSquashes) != 28 ||
		counterfactual.approved != 10 || counterfactual.total != 16 || counterfactual.score != 6 {
		t.Fatalf("after copying merge associations onto REST squash SHAs got associated=%d %d/%d score %d; want associated=28 and 10/16 score 6\n%s",
			countAssociated(linkedSquashes),
			counterfactual.approved, counterfactual.total, counterfactual.score,
			formatChangesets(counterfactual.changesets))
	}
}

func assertControlChangesets(t *testing.T, sets []checker.Changeset) {
	t.Helper()
	ordinary, ok := changesetContaining(sets, ordinaryMergeSHA)
	if !ok || ordinary.RevisionID != ordinaryMRIID || !changesetHasSHA(ordinary, ordinaryCommitSHA) || !nonAuthorApproved(ordinary) {
		t.Fatalf("ordinary approved merge = %+v", ordinary)
	}
	unapproved, ok := changesetContaining(sets, unapprovedMergeSHA)
	if !ok || unapproved.RevisionID != unapprovedMRIID || nonAuthorApproved(unapproved) {
		t.Fatalf("unapproved self-merge = %+v", unapproved)
	}
	squashMerge, ok := changesetContaining(sets, squashMergeSHA)
	if !ok || squashMerge.RevisionID != squashMRIID || !nonAuthorApproved(squashMerge) {
		t.Fatalf("approved squash merge commit = %+v", squashMerge)
	}
}

type codeReviewResult struct {
	reason     string
	changesets []checker.Changeset
	approved   int
	total      int
	score      int
}

func scoreCommits(t *testing.T, commits []clients.Commit) codeReviewResult {
	t.Helper()
	ctrl := gomock.NewController(t)
	repo := mockrepo.NewMockRepoClient(ctrl)
	repo.EXPECT().ListCommits().Return(commits, nil)

	data, err := raw.CodeReview(repo)
	if err != nil {
		t.Fatalf("raw.CodeReview: %v", err)
	}
	findings, _, err := codeApproved.Run(&checker.RawResults{CodeReviewResults: data})
	if err != nil {
		t.Fatalf("codeApproved.Run: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("codeApproved findings = %d, want 1", len(findings))
	}
	approved, err := strconv.Atoi(findings[0].Values[codeApproved.NumApprovedKey])
	if err != nil {
		t.Fatalf("approved count: %v", err)
	}
	total, err := strconv.Atoi(findings[0].Values[codeApproved.NumTotalKey])
	if err != nil {
		t.Fatalf("total count: %v", err)
	}
	result := evaluation.CodeReview(checks.CheckCodeReview, findings, &scut.TestDetailLogger{})
	return codeReviewResult{
		changesets: data.DefaultBranchChangesets,
		reason:     result.Reason,
		approved:   approved,
		total:      total,
		score:      result.Score,
	}
}

func squashFixtureCommits() []gitlabrepo.CommitFixture {
	when := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	return []gitlabrepo.CommitFixture{
		{ID: ordinaryMergeSHA, Message: ordinaryMergeSHA, Committed: when},
		{ID: ordinaryCommitSHA, Message: ordinaryCommitSHA, Committed: when.Add(-time.Minute)},
		{ID: squashMergeSHA, Message: squashMergeSHA, Committed: when.Add(-2 * time.Minute)},
		{ID: squashCommitSHA, Message: squashCommitSHA, Committed: when.Add(-3 * time.Minute)},
		{ID: unapprovedMergeSHA, Message: unapprovedMergeSHA, Committed: when.Add(-4 * time.Minute)},
	}
}

func squashFixtureMRs() []gitlabrepo.MergeRequestFixture {
	return []gitlabrepo.MergeRequestFixture{
		approvedMergeRequest(squashMRIID, squashMergeSHA, []string{preSquashSHA}, "author", "reviewer", "merger"),
		approvedMergeRequest(ordinaryMRIID, ordinaryMergeSHA, []string{ordinaryCommitSHA}, "author", "reviewer", "merger"),
		{
			IID:            unapprovedMRIID,
			MergedAt:       time.Date(2024, 1, 2, 3, 0, 0, 0, time.UTC),
			MergeCommitSHA: unapprovedMergeSHA,
			Author:         "same-user",
			Merger:         "same-user",
		},
	}
}

func approvedMergeRequest(iid, mergeSHA string, commitSHAs []string, author, approver, merger string) gitlabrepo.MergeRequestFixture {
	return gitlabrepo.MergeRequestFixture{
		IID:            iid,
		MergedAt:       time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		MergeCommitSHA: mergeSHA,
		Author:         author,
		Merger:         merger,
		CommitSHAs:     commitSHAs,
		Approvers:      []string{approver},
		Reviewers: []gitlabrepo.ReviewerFixture{{
			Username:    approver,
			ReviewState: "APPROVED",
		}},
	}
}

func copyAssociation(commits []clients.Commit, fromSHA, toSHA string) []clients.Commit {
	out := slices.Clone(commits)
	var mr clients.PullRequest
	found := false
	for _, commit := range out {
		if commit.SHA == fromSHA && commit.AssociatedMergeRequest.Number != 0 {
			mr = commit.AssociatedMergeRequest
			found = true
		}
	}
	if !found {
		return out
	}
	for i := range out {
		if out[i].SHA == toSHA {
			out[i].AssociatedMergeRequest = mr
		}
	}
	return out
}

func associateSquashSHAs(commits []clients.Commit, squashToMerge map[string]string) []clients.Commit {
	byMerge := map[string]clients.PullRequest{}
	for _, commit := range commits {
		if commit.AssociatedMergeRequest.Number != 0 {
			byMerge[commit.SHA] = commit.AssociatedMergeRequest
		}
	}
	out := slices.Clone(commits)
	for i := range out {
		if out[i].AssociatedMergeRequest.Number != 0 {
			continue
		}
		mergeSHA, ok := squashToMerge[out[i].SHA]
		if !ok {
			continue
		}
		if mr, ok := byMerge[mergeSHA]; ok {
			out[i].AssociatedMergeRequest = mr
		}
	}
	return out
}

func countAssociated(commits []clients.Commit) int {
	n := 0
	for _, commit := range commits {
		if commit.AssociatedMergeRequest.Number != 0 {
			n++
		}
	}
	return n
}

func associatedNumber(commits []clients.Commit, sha string) int {
	for _, commit := range commits {
		if commit.SHA == sha {
			return commit.AssociatedMergeRequest.Number
		}
	}
	return -1
}

func changesetContaining(sets []checker.Changeset, sha string) (checker.Changeset, bool) {
	for _, cs := range sets {
		if changesetHasSHA(cs, sha) {
			return cs, true
		}
	}
	return checker.Changeset{}, false
}

func changesetHasSHA(cs checker.Changeset, sha string) bool {
	for _, commit := range cs.Commits {
		if commit.SHA == sha {
			return true
		}
	}
	return false
}

func nonAuthorApproved(cs checker.Changeset) bool {
	if cs.Author.Login == "" {
		return false
	}
	for _, review := range cs.Reviews {
		if review.State == "APPROVED" &&
			review.Author != nil &&
			review.Author.Login != "" &&
			review.Author.Login != cs.Author.Login {
			return true
		}
	}
	return false
}

func formatChangesets(sets []checker.Changeset) string {
	sorted := slices.Clone(sets)
	slices.SortFunc(sorted, func(a, b checker.Changeset) int {
		return strings.Compare(a.RevisionID, b.RevisionID)
	})
	var b strings.Builder
	for _, cs := range sorted {
		shas := make([]string, 0, len(cs.Commits))
		for _, commit := range cs.Commits {
			shas = append(shas, commit.SHA)
		}
		slices.Sort(shas)
		b.WriteString("  platform=" + cs.ReviewPlatform +
			" revision=" + cs.RevisionID +
			" author=" + cs.Author.Login +
			" approved=" + strconv.FormatBool(nonAuthorApproved(cs)) +
			" commits=" + strings.Join(shas, ",") + "\n")
	}
	return b.String()
}

type sylvaWindow struct {
	Commits []struct {
		ID            string `json:"id"`
		CommittedDate string `json:"committed_date"`
		Message       string `json:"message"`
	} `json:"commits"`
	MergeRequests []struct {
		IID                 string  `json:"iid"`
		MergedAt            string  `json:"mergedAt"`
		MergeCommitSHA      string  `json:"mergeCommitSha"`
		RESTSquashCommitSHA *string `json:"restSquashCommitSha"`
		Author              struct {
			Username string `json:"username"`
		} `json:"author"`
		MergeUser struct {
			Username string `json:"username"`
		} `json:"mergeUser"`
		Commits struct {
			Nodes []struct {
				SHA string `json:"sha"`
			} `json:"nodes"`
		} `json:"commits"`
		Reviewers struct {
			Nodes []struct {
				Username                string `json:"username"`
				MergeRequestInteraction struct {
					ReviewState string `json:"reviewState"`
				} `json:"mergeRequestInteraction"`
			} `json:"nodes"`
		} `json:"reviewers"`
		ApprovedBy struct {
			Nodes []struct {
				Username string `json:"username"`
			} `json:"nodes"`
		} `json:"approvedBy"`
	} `json:"merge_requests"`
}

func loadSylvaWindow(t *testing.T) ([]gitlabrepo.CommitFixture, []gitlabrepo.MergeRequestFixture, map[string]string) {
	t.Helper()
	buf, err := os.ReadFile("testdata/sylva-core-code-review-window")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var window sylvaWindow
	if err := json.Unmarshal(buf, &window); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	commits := make([]gitlabrepo.CommitFixture, 0, len(window.Commits))
	for _, commit := range window.Commits {
		committed, err := time.Parse(time.RFC3339Nano, commit.CommittedDate)
		if err != nil {
			t.Fatalf("parse %s: %v", commit.ID, err)
		}
		commits = append(commits, gitlabrepo.CommitFixture{
			ID:        commit.ID,
			Message:   commit.Message,
			Committed: committed,
		})
	}
	mrs := make([]gitlabrepo.MergeRequestFixture, 0, len(window.MergeRequests))
	squashToMerge := map[string]string{}
	for _, mr := range window.MergeRequests {
		mergedAt, err := time.Parse(time.RFC3339Nano, mr.MergedAt)
		if err != nil {
			t.Fatalf("parse MR %s mergedAt: %v", mr.IID, err)
		}
		fixture := gitlabrepo.MergeRequestFixture{
			IID:            mr.IID,
			MergedAt:       mergedAt,
			MergeCommitSHA: mr.MergeCommitSHA,
			Author:         mr.Author.Username,
			Merger:         mr.MergeUser.Username,
		}
		for _, commit := range mr.Commits.Nodes {
			fixture.CommitSHAs = append(fixture.CommitSHAs, commit.SHA)
		}
		for _, approver := range mr.ApprovedBy.Nodes {
			fixture.Approvers = append(fixture.Approvers, approver.Username)
		}
		for _, reviewer := range mr.Reviewers.Nodes {
			fixture.Reviewers = append(fixture.Reviewers, gitlabrepo.ReviewerFixture{
				Username:    reviewer.Username,
				ReviewState: reviewer.MergeRequestInteraction.ReviewState,
			})
		}
		mrs = append(mrs, fixture)
		if mr.RESTSquashCommitSHA != nil && *mr.RESTSquashCommitSHA != "" {
			squashToMerge[*mr.RESTSquashCommitSHA] = mr.MergeCommitSHA
		}
	}
	return commits, mrs, squashToMerge
}
