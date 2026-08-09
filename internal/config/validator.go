package config

import (
	_ "embed"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/encoding/yaml"
)

//go:embed schemas/v2.cue
var v2SchemaBytes []byte

type Validator struct {
	ctx             *cue.Context
	authoringSchema cue.Value
	resolvedSchema  cue.Value
}

func NewValidator() (*Validator, error) {
	ctx := cuecontext.New()
	val := ctx.CompileBytes(v2SchemaBytes)
	if val.Err() != nil {
		return nil, fmt.Errorf("compile cue schema: %w", val.Err())
	}
	authoring := val.LookupPath(cue.ParsePath("#LXM_AUTHORING"))
	if authoring.Err() != nil {
		return nil, fmt.Errorf("lookup #LXM_AUTHORING: %w", authoring.Err())
	}
	resolved := val.LookupPath(cue.ParsePath("#LXM_RESOLVED"))
	if resolved.Err() != nil {
		return nil, fmt.Errorf("lookup #LXM_RESOLVED: %w", resolved.Err())
	}

	return &Validator{
		ctx:             ctx,
		authoringSchema: authoring,
		resolvedSchema:  resolved,
	}, nil
}

func (v *Validator) ValidateAuthoring(yamlBytes []byte) error {
	file, err := yaml.Extract("authoring.yaml", yamlBytes)
	if err != nil {
		return fmt.Errorf("yaml parse: %w", err)
	}
	val := v.ctx.BuildFile(file)
	if val.Err() != nil {
		return fmt.Errorf("build cue value: %w", val.Err())
	}
	res := v.authoringSchema.Unify(val)
	if err := res.Validate(cue.Final(), cue.Concrete(true)); err != nil {
		return fmt.Errorf("authoring schema validation failed: %w", err)
	}
	return nil
}

func (v *Validator) ValidateResolved(yamlBytes []byte) error {
	file, err := yaml.Extract("resolved.yaml", yamlBytes)
	if err != nil {
		return fmt.Errorf("yaml parse: %w", err)
	}
	val := v.ctx.BuildFile(file)
	if val.Err() != nil {
		return fmt.Errorf("build cue value: %w", val.Err())
	}
	res := v.resolvedSchema.Unify(val)
	if err := res.Validate(cue.Final(), cue.Concrete(true)); err != nil {
		return fmt.Errorf("resolved schema validation failed: %w", err)
	}
	return nil
}
