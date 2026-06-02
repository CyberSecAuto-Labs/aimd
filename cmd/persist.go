package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/CyberSecAuto-Labs/aimd/internal/registry"
	"github.com/CyberSecAuto-Labs/aimd/internal/store"
)

// persistChange is the single "persistence ritual" shared by track, untrack, and
// restore. It records the current machine, writes the registry and per-project
// metadata, commits the three store artifacts (registry + overlays + metadata)
// in one commit, then pushes — warning (never failing) on a push error.
//
// Centralising it keeps the durability and 3-artifact-consistency invariants in
// one place instead of being hand-reassembled (and drifting) in each command.
func persistChange(
	storeDir, projectKey, projectRoot, verb, machineName string,
	reg *registry.Registry, projEntry *registry.Project, registryPath string,
	files []string, out io.Writer,
) error {
	registry.UpsertMachine(projEntry, machineName, &registry.Machine{
		LocalPath: projectRoot,
		LastSeen:  time.Now().UTC(),
	})
	registry.UpsertProject(reg, projectKey, projEntry)

	if err := registry.Save(registryPath, reg); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}
	if err := writeProjectMetadata(storeDir, projectKey, projEntry); err != nil {
		return fmt.Errorf("writing project metadata: %w", err)
	}
	if err := store.Commit(storeDir, projectKey, projectRoot, verb, machineName, files); err != nil {
		if !isNothingToCommit(err) {
			return fmt.Errorf("committing to store: %w", err)
		}
	}
	if pushErr := store.Push(storeDir); pushErr != nil {
		warnOnPushError(pushErr, storeDir, out)
	}
	return nil
}
