package testengine

import (
	"context"
	"time"

	"github.com/OWNER/mikrotik-proxy-helper/internal/model"
)

type ProgressFunc func(index, total int, result model.TestResult)

func (e *Engine) TestAll(ctx context.Context, profiles []model.Profile, progress ProgressFunc) []model.TestResult {
	results := make([]model.TestResult, 0, len(profiles))
	for index, profile := range profiles {
		select {
		case <-ctx.Done():
			for remaining := index; remaining < len(profiles); remaining++ {
				result := model.TestResult{
					RunID: newRunID(), ProfileID: profiles[remaining].ID, ProfileName: profiles[remaining].Name,
					Status: "cancelled", Stage: "completed", ErrorCode: "run_cancelled",
					ErrorMessage: ctx.Err().Error(), TestedAt: time.Now().UTC(),
				}
				results = append(results, result)
				if progress != nil { progress(remaining, len(profiles), result) }
			}
			return results
		default:
		}
		result := e.TestProfile(ctx, profile)
		results = append(results, result)
		if progress != nil { progress(index, len(profiles), result) }
	}
	return results
}
