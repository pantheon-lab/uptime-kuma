package check

import "context"

type dryRunKey struct{}

// DryRun marks a check as a trial of a monitor that may not be saved, such as the Edit Monitor
// form's test: it doesn't store a DNS monitor's last result or a browser monitor's screenshot.
func DryRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, dryRunKey{}, true)
}

func isDryRun(ctx context.Context) bool {
	dry, _ := ctx.Value(dryRunKey{}).(bool)
	return dry
}
