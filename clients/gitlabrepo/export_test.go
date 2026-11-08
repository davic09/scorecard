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

package gitlabrepo

import (
	"time"

	gitlab "gitlab.com/gitlab-org/api/client-go"

	"github.com/ossf/scorecard/v5/clients"
)

// CommitFixture is a REST commit input for ZipCommitsForTest.
// This file is compiled only by go test.
type CommitFixture struct {
	Committed time.Time
	ID        string
	Message   string
}

// ReviewerFixture is one GraphQL reviewer input for ZipCommitsForTest.
type ReviewerFixture struct {
	Username    string
	ReviewState string
}

// MergeRequestFixture is the merge-request shape read by zip.
// It has no squash commit SHA. A caller that knows one applies it after zip.
type MergeRequestFixture struct {
	MergedAt       time.Time
	IID            string
	MergeCommitSHA string
	Author         string
	Merger         string
	CommitSHAs     []string
	Approvers      []string
	Reviewers      []ReviewerFixture
}

// ZipCommitsForTest runs the production commit and merge-request matcher.
func ZipCommitsForTest(commits []CommitFixture, mrs []MergeRequestFixture) []clients.Commit {
	raw := make([]*gitlab.Commit, 0, len(commits))
	for _, commit := range commits {
		committed := commit.Committed
		raw = append(raw, &gitlab.Commit{
			ID:            commit.ID,
			Message:       commit.Message,
			CommittedDate: &committed,
		})
	}

	var data graphqlData
	for _, mr := range mrs {
		node := graphqlMergeRequestNode{
			IID:            mr.IID,
			MergedAt:       mr.MergedAt,
			MergeCommitSHA: mr.MergeCommitSHA,
		}
		node.Author.Username = mr.Author
		node.MergedBy.Username = mr.Merger
		for _, sha := range mr.CommitSHAs {
			node.Commits.Nodes = append(node.Commits.Nodes, struct {
				SHA string `graphql:"sha"`
			}{SHA: sha})
		}
		for _, reviewer := range mr.Reviewers {
			// Field order is the production reviewer element type. Struct identity includes that order.
			node.Reviewers.Nodes = append(node.Reviewers.Nodes, struct { //nolint:govet // Must match the production reviewer field order.
				Username                string    `graphql:"username"`
				ID                      GitlabGID `graphql:"id"`
				MergeRequestInteraction struct {
					ReviewState string `graphql:"reviewState"`
				} `graphql:"mergeRequestInteraction"`
			}{
				Username: reviewer.Username,
				MergeRequestInteraction: struct {
					ReviewState string `graphql:"reviewState"`
				}{ReviewState: reviewer.ReviewState},
			})
		}
		for _, approver := range mr.Approvers {
			node.Approvers.Nodes = append(node.Approvers.Nodes, struct {
				Username string    `graphql:"username"`
				ID       GitlabGID `graphql:"id"`
			}{Username: approver})
		}
		data.Project.MergeRequests.Nodes = append(data.Project.MergeRequests.Nodes, node)
	}
	return (&commitsHandler{}).zip(raw, data)
}
