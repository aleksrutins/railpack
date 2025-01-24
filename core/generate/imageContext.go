package generate

import (
	"github.com/railwayapp/railpack-go/core/plan"
	"github.com/railwayapp/railpack-go/core/resolver"
)

type ImageStepBuilder struct {
	DisplayName string
	Resolver    *resolver.Resolver
	Packages    []*resolver.PackageRef
	Outputs     []string

	ResolveStepImage func(options *BuildStepOptions) string
}

func (c *GenerateContext) NewImageStep(name string, resolveStepImage func(options *BuildStepOptions) string) *ImageStepBuilder {
	step := &ImageStepBuilder{
		DisplayName:      c.GetStepName(name),
		Resolver:         c.resolver,
		ResolveStepImage: resolveStepImage,
	}

	c.Steps = append(c.Steps, step)

	return step
}

func (b *ImageStepBuilder) Default(name string, defaultVersion string) resolver.PackageRef {
	for _, pkg := range b.Packages {
		if pkg.Name == name {
			return *pkg
		}
	}

	pkg := b.Resolver.Default(name, defaultVersion)
	b.Packages = append(b.Packages, &pkg)
	return pkg
}

func (b *ImageStepBuilder) Version(name resolver.PackageRef, version string, source string) {
	b.Resolver.Version(name, version, source)
}

func (b *ImageStepBuilder) Build(options *BuildStepOptions) (*plan.Step, error) {
	step := plan.NewStep(b.DisplayName)

	step.StartingImage = b.ResolveStepImage(options)
	step.Outputs = b.Outputs

	return step, nil
}
