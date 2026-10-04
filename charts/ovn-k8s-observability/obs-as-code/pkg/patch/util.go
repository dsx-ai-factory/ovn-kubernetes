// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"obs-as-code/pkg/log"

	"github.com/prometheus/prometheus/model/labels"
)

func PrintExprDiff(before, after string) {
	log.Debugf("expr before: %s", before)
	log.Debugf("expr after:  %s", after)
	log.Debugf("--------------------------------")
}

func mustMatcher(t labels.MatchType, name, value string) *labels.Matcher {
	m, err := labels.NewMatcher(t, name, value)
	if err != nil {
		panic(err)
	}
	return m
}
