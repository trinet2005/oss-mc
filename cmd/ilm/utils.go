// Copyright (c) 2022 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package ilm

import (
	"fmt"
	"strings"
	"time"

	"github.com/trinet2005/oss-go-sdk/pkg/lifecycle"
)

// getPrefix returns the prefix configured
func getPrefix(rule lifecycle.Rule) string {
	// deprecated, but older ILM policies may have them
	if rule.Prefix != "" {
		return rule.Prefix
	}
	if rule.RuleFilter.Prefix != "" {
		return rule.RuleFilter.Prefix
	}
	if rule.RuleFilter.And.Prefix != "" {
		return rule.RuleFilter.And.Prefix
	}
	return ""
}

// getTags returns the tags configured as "k1=v1&k2=v2"
func getTags(rule lifecycle.Rule) string {
	if !rule.RuleFilter.Tag.IsEmpty() {
		return fmt.Sprintf("%s=%s", rule.RuleFilter.Tag.Key, rule.RuleFilter.Tag.Value)
	}
	if len(rule.RuleFilter.And.Tags) > 0 {
		var tags strings.Builder
		for i, tag := range rule.RuleFilter.And.Tags {
			fmt.Fprintf(&tags, "%s=%s", tag.Key, tag.Value)
			if i < len(rule.RuleFilter.And.Tags)-1 {
				fmt.Fprintf(&tags, "&")
			}
		}
		return tags.String()
	}
	return ""
}

// getExpirationDays returns the number of days to expire relative to
// time.Now().UTC() for the given rule.
func getExpirationDays(rule lifecycle.Rule) int {
	if rule.Expiration.Days > 0 {
		return int(rule.Expiration.Days)
	}
	if !rule.Expiration.Date.Time.IsZero() {
		return int(time.Until(rule.Expiration.Date.Time).Hours() / 24)
	}

	return 0
}

// getTransitionDays returns the number of days to transition/tier relative to
// time.Now().UTC() for the given rule.
func getTransitionDays(rule lifecycle.Rule) int {
	if !rule.Transition.Date.IsZero() {
		return int(time.Now().UTC().Sub(rule.Transition.Date.Time).Hours() / 24)
	}

	return int(rule.Transition.Days)
}

// ToTables converts a lifecycle.Configuration into its tabular representation.
func ToTables(cfg *lifecycle.Configuration, filter LsFilter) []Table {
	var tierCur tierCurrentTable
	var tierNoncur tierNoncurrentTable
	var expCur expirationCurrentTable
	var expNoncur expirationNoncurrentTable
	for _, rule := range cfg.Rules {
		if !rule.Expiration.IsNull() {
			expCur = append(expCur, expirationCurrentRow{
				ID:              rule.ID,
				Status:          rule.Status,
				Prefix:          getPrefix(rule),
				Tags:            getTags(rule),
				Days:            getExpirationDays(rule),
				ExpireDelMarker: bool(rule.Expiration.DeleteMarker),
			})
		}
		if !rule.NoncurrentVersionExpiration.IsDaysNull() || rule.NoncurrentVersionExpiration.NewerNoncurrentVersions > 0 {
			expNoncur = append(expNoncur, expirationNoncurrentRow{
				ID:           rule.ID,
				Status:       rule.Status,
				Prefix:       getPrefix(rule),
				Tags:         getTags(rule),
				Days:         int(rule.NoncurrentVersionExpiration.NoncurrentDays),
				KeepVersions: rule.NoncurrentVersionExpiration.NewerNoncurrentVersions,
			})
		}
		if !rule.Transition.IsNull() {
			tierCur = append(tierCur, tierCurrentRow{
				ID:     rule.ID,
				Status: rule.Status,
				Prefix: getPrefix(rule),
				Tags:   getTags(rule),
				Days:   getTransitionDays(rule),
				Tier:   rule.Transition.StorageClass,
			})
		}
		if !rule.NoncurrentVersionTransition.IsStorageClassEmpty() {
			tierNoncur = append(tierNoncur, tierNoncurrentRow{
				ID:     rule.ID,
				Status: rule.Status,
				Prefix: getPrefix(rule),
				Tags:   getTags(rule),
				Days:   int(rule.NoncurrentVersionTransition.NoncurrentDays),
				Tier:   rule.NoncurrentVersionTransition.StorageClass,
			})
		}
	}

	switch filter {
	case ExpiryOnly:
		return []Table{expCur, expNoncur}
	case TransitionOnly:
		return []Table{tierCur, tierNoncur}
	default:
		return []Table{tierCur, tierNoncur, expCur, expNoncur}
	}
}
