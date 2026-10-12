package target

import (
	"context"
	"slices"
	"testing"
)

func TestSyncReportsStoppedRuntimeOnReplacementClientFailure(t *testing.T) {
	definitions := independentDefinitions()
	clients := newRecordingClientFactory()
	manager, err := NewRuntimeManager(resolverForDefinitions(t, definitions), clients, newRecordingResourceFactory(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defer func() { _, _ = manager.Sync(ctx, nil) }()
	if _, err := manager.Sync(ctx, definitions); err != nil {
		t.Fatal(err)
	}
	key := definitions[0].Key()
	clients.fail[key] = true
	definitions[0].Generation++
	result, err := manager.Sync(ctx, definitions)
	if err != nil || result.Failures[key] != FailureClient || !slices.Equal(result.Stopped, []string{key}) || len(result.Changed) != 0 {
		t.Fatalf("failed replacement transition = %+v, %v", result, err)
	}
	result, err = manager.Sync(ctx, definitions)
	if err != nil || len(result.Stopped) != 0 || len(result.Changed) != 0 {
		t.Fatalf("already-stopped transition = %+v, %v", result, err)
	}
}
