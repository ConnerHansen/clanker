package data

// Test-only accessors/mutators for the system prompt layer vars (see
// contexts.go). Used by agent package tests to point SystemPromptFile at
// fixtures; production code never calls these.

func LocalSystemMDForTest() string  { return localSystemMD }
func GlobalSystemMDForTest() string { return globalSystemMD }
func LocalProjectMDForTest() string { return localProjectMD }
func SetSystemMDPathsForTest(local, global, project string) {
	localSystemMD = local
	globalSystemMD = global
	localProjectMD = project
}
