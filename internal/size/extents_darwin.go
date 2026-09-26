package size

import (
	"context"
	"os"
)

// macOS has no portable exclusive-extent query. Never infer whole-file
// sharing from one physical block; report the uncertainty explicitly.
func probeExtents(context.Context, string, os.FileInfo) (extentSummary, error) {
	return extentSummary{}, nil
}
