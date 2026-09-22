package adapters

import (
	"context"
	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
)

// Called after session isolation selects the actual readable workspace, before
// the Provider process starts. Private package contents never enter task metadata.
func loadSkillPackages(ctx context.Context, run RunContext, provider, workspace string) (RunContext, error) {
	loaded, err := skillpackages.Load(ctx, skillpackages.Request{Snapshot: run.PackageSnapshot, AgentID: run.AgentID, Trusted: run.Authority != nil, Emit: run.Emit}, provider, workspace, skillpackages.Cache{})
	if err != nil {
		return run, err
	}
	run.SkillPackagesDigest, run.LoadedSkillPackages = loaded.Digest, loaded.Packages
	return run, nil
}
func skillPackageSessionMode(run RunContext) string {
	return skillpackages.SessionMode(run.SkillPackagesDigest)
}
func skillPackageInstructions(run RunContext) string {
	return skillpackages.Instructions(run.LoadedSkillPackages, run.SkillPackagesAlreadyLoaded)
}
