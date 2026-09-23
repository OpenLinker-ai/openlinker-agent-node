package adapters

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/skillpackages"
)

// Called after session isolation selects the actual readable workspace, before
// the Provider process starts. Private package contents never enter task metadata.
func loadSkillPackages(ctx context.Context, run RunContext, provider, workspace string, config ProviderConfig) (RunContext, error) {
	environment := config.Env
	if environment == nil {
		environment = os.Environ()
	}
	check := func(ctx context.Context, names []string) error {
		var allow func(string) bool
		if config.toolPolicy != nil {
			allow = config.toolPolicy.commandVisible
		}
		return skillpackages.CheckCommands(ctx, names, environment, workspace, allow)
	}
	loaded, err := skillpackages.Load(ctx, skillpackages.Request{Snapshot: run.PackageSnapshot, AgentID: run.AgentID, Trusted: run.Authority != nil, Emit: run.Emit, CheckCommands: check}, provider, workspace, skillpackages.Cache{})
	if err != nil {
		return run, err
	}
	run.SkillPackagesDigest, run.LoadedSkillPackages = loaded.Digest, loaded.Packages
	return run, nil
}

func (p *nativeToolPolicy) commandVisible(name string) bool {
	target, err := filepath.EvalSymlinks(name)
	if err != nil {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		return false
	}
	targetAllowed, parentAllowed := false, false
	for _, root := range p.commandReadRoots {
		if target == root && filepath.Join(parent, filepath.Base(name)) == target {
			return true
		}
		contains := func(path string) bool {
			return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
		}
		targetAllowed = targetAllowed || contains(target)
		parentAllowed = parentAllowed || contains(parent)
	}
	return targetAllowed && parentAllowed
}
func skillPackageSessionMode(run RunContext) string {
	return skillpackages.SessionMode(run.SkillPackagesDigest)
}
func skillPackageInstructions(run RunContext) string {
	return skillpackages.Instructions(run.LoadedSkillPackages, run.SkillPackagesAlreadyLoaded)
}
