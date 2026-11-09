package check

import (
	"context"
	"testing"
)

func TestDryRun(t *testing.T) {
	if isDryRun(context.Background()) {
		t.Error("a plain context is a dry run")
	}
	if !isDryRun(DryRun(context.Background())) {
		t.Error("DryRun isn't a dry run")
	}
}
