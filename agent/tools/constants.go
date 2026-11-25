package tools

import "time"

// Configuration constants to protect against AI overwhelming the system
const (
	MaxFileLines      = 5000
	MaxSearchResults  = 100
	MaxDirectoryDepth = 20
	MaxNameSearch     = 50
	MaxCommandOutput  = 10000 // max bytes of command output returned to the model
	MaxReadBytes      = 65536 // default max bytes of file content returned per read (64 KiB)
	CommandTimeout    = 5 * time.Minute
)
