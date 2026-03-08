package memory_test

import (
	"testing"
)

// TODO Phase 1: TestWrite_ValidDataType_Succeeds
// TODO Phase 1: TestWrite_InvalidDataType_ReturnsError
// TODO Phase 1: TestWrite_UntaggedPayload_ReturnsError
// TODO Phase 1: TestWrite_MemoryDown_RetriesThreeTimes
// TODO Phase 1: TestWrite_AllRetriesExhausted_CallsWriteFailureHandler
// TODO Phase 1: TestRead_ReturnsRecordsOrderedByTimestampAscending
// TODO Phase 1: TestRead_FiltersByTaskID
// TODO Phase 1: TestRead_FiltersByDataType
// TODO Phase 1: TestRead_FiltersByTimeRange
// TODO Phase 1: TestReadLatest_ReturnsNewestRecord
// TODO Phase 1: TestMigrateSchema_CreatesTablesOnFirstRun
// TODO Phase 1: TestMigrateSchema_AppendOnlyTrigger_RejectsUpdateOnAuditLog
// TODO Phase 1: TestMigrateSchema_AppendOnlyTrigger_RejectsDeleteOnPolicyEvent

func TestPlaceholder(t *testing.T) {
	t.Skip("memory interface tests not yet implemented — see Phase 1")
}
