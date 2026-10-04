// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package log

import (
	stdlog "log"
	"os"
)

var (
	logger      = stdlog.New(os.Stderr, "", 0)
	debugEnable bool
)

func Setup(verbose bool) {
	debugEnable = verbose
	logger.SetFlags(0)
	logger.SetOutput(os.Stderr)
}

func Infof(format string, a ...any) {
	logger.Printf(format, a...)
}

func Debugf(format string, a ...any) {
	if debugEnable {
		logger.Printf(format, a...)
	}
}

func Fatalf(format string, a ...any) {
	logger.Fatalf(format, a...)
}
