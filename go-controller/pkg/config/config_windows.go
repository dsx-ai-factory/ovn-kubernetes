//go:build windows
// +build windows

package config

// validateMgmtPortConfig validates the existence of MgmtPortNetdev for:
//	 - primary DPU node
//	 - DPU-host node
//	 - full mode node when MgmtPortNetdev is configured
func validateMgmtPortConfig() error {
	return nil
}
