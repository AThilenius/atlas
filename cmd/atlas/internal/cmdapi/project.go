// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package cmdapi

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"

	"ariga.io/atlas/cmd/atlas/internal/cmdext"
	"ariga.io/atlas/schemahcl"
	"ariga.io/atlas/sql/schema"

	"github.com/zclconf/go-cty/cty"
)

const defaultConfigPath = "file://atlas.hcl"

type loadConfig struct {
	inputValues map[string]cty.Value
}

// LoadOption configures the LoadEnv function.
type LoadOption func(*loadConfig)

// WithInput is a LoadOption that sets the input values for the LoadEnv function.
func WithInput(values map[string]cty.Value) LoadOption {
	return func(config *loadConfig) {
		config.inputValues = values
	}
}

type (
	// Project represents an atlas.hcl project file.
	Project struct {
		Envs []*Env `spec:"env"`  // List of environments
		Lint *Lint  `spec:"lint"` // Optional global lint policy
		Diff *Diff  `spec:"diff"` // Optional global diff policy
	}

	// Env represents an Atlas environment.
	Env struct {
		// Name for this environment.
		Name string `spec:"name,name"`

		// URL of the database.
		URL string `spec:"url"`

		// URL of the dev-database for this environment.
		// See: https://atlasgo.io/dev-database
		DevURL string `spec:"dev"`

		// List of schemas in this database that are managed by Atlas.
		Schemas []string `spec:"schemas"`

		// Exclude defines a list of glob patterns used to filter
		// resources on inspection.
		Exclude []string `spec:"exclude"`

		// Migration containing the migration configuration of the env.
		Migration *Migration `spec:"migration"`

		// Diff policy of the environment.
		Diff *Diff `spec:"diff"`

		// Lint policy of the environment.
		Lint *Lint `spec:"lint"`

		// Format of the environment.
		Format Format `spec:"format"`

		schemahcl.DefaultExtension
		cfg *cmdext.AtlasConfig
	}

	// Migration represents the migration directory for the Env.
	Migration struct {
		Dir             string `spec:"dir"`
		Format          string `spec:"format"`
		Baseline        string `spec:"baseline"`
		LockTimeout     string `spec:"lock_timeout"`
		RevisionsSchema string `spec:"revisions_schema"`
	}

	// Lint represents the configuration of migration linting.
	Lint struct {
		// Format configures the --format option.
		Format string `spec:"log"`
		// Latest configures the --latest option.
		Latest int `spec:"latest"`
		Git    struct {
			// Dir configures the --git-dir option.
			Dir string `spec:"dir"`
			// Base configures the --git-base option.
			Base string `spec:"base"`
		} `spec:"git"`
		schemahcl.DefaultExtension
	}

	// Diff represents the schema diffing policy.
	Diff struct {
		// SkipChanges configures the skip changes policy.
		SkipChanges *SkipChanges `spec:"skip"`
		schemahcl.DefaultExtension
	}

	// SkipChanges represents the skip changes policy.
	SkipChanges struct {
		AddSchema        bool `spec:"add_schema"`
		DropSchema       bool `spec:"drop_schema"`
		ModifySchema     bool `spec:"modify_schema"`
		AddTable         bool `spec:"add_table"`
		DropTable        bool `spec:"drop_table"`
		ModifyTable      bool `spec:"modify_table"`
		AddColumn        bool `spec:"add_column"`
		DropColumn       bool `spec:"drop_column"`
		ModifyColumn     bool `spec:"modify_column"`
		AddIndex         bool `spec:"add_index"`
		DropIndex        bool `spec:"drop_index"`
		ModifyIndex      bool `spec:"modify_index"`
		AddForeignKey    bool `spec:"add_foreign_key"`
		DropForeignKey   bool `spec:"drop_foreign_key"`
		ModifyForeignKey bool `spec:"modify_foreign_key"`
	}

	// Format represents the output formatting configuration of an environment.
	Format struct {
		Migrate struct {
			// Apply configures the formatting for 'migrate apply'.
			Apply string `spec:"apply"`
			// Lint configures the formatting for 'migrate lint'.
			Lint string `spec:"lint"`
			// Status configures the formatting for 'migrate status'.
			Status string `spec:"status"`
			// Apply configures the formatting for 'migrate diff'.
			Diff string `spec:"diff"`
		} `spec:"migrate"`
		Schema struct {
			// Apply configures the formatting for 'schema apply'.
			Apply string `spec:"apply"`
			// Apply configures the formatting for 'schema diff'.
			Diff string `spec:"diff"`
		} `spec:"schema"`
		schemahcl.DefaultExtension
	}
)

// support backward compatibility with the 'log' attribute.
func (e *Env) remainedLog() error {
	r, ok := e.Remain().Resource("log")
	if ok {
		return r.As(&e.Format)
	}
	return nil
}

// Extend allows extending environment blocks with
// a global one. For example:
//
//	lint {
//	  log = <<EOS
//	    ...
//	  EOS
//	}
//
//	env "local" {
//	  ...
//	  lint {
//	    latest = 1
//	  }
//	}
//
//	env "ci" {
//	  ...
//	  lint {
//	    git {
//	      dir = "../"
//	      base = "master"
//	    }
//	  }
//	}
func (l *Lint) Extend(global *Lint) *Lint {
	if l == nil {
		return global
	}
	if l.Format == "" {
		l.Format = global.Format
	}
	l.Extra = global.Extra
	switch {
	// Changes detector was configured on the env.
	case l.Git.Dir != "" && l.Git.Base != "" || l.Latest != 0:
	// Inherit global git detection.
	case global.Git.Dir != "" || global.Git.Base != "":
		if global.Git.Dir != "" {
			l.Git.Dir = global.Git.Dir
		}
		if global.Git.Base != "" {
			l.Git.Base = global.Git.Base
		}
	// Inherit latest files configuration.
	case global.Latest != 0:
		l.Latest = global.Latest
	}
	return l
}

// support backward compatibility with the 'log' attribute.
func (l *Lint) remainedLog() error {
	at, ok := l.Remain().Attr("log")
	if !ok {
		return nil
	}
	if l.Format != "" {
		return fmt.Errorf("cannot use both 'log' and 'format' in the same lint block")
	}
	s, err := at.String()
	if err != nil {
		return err
	}
	l.Format = s
	return nil
}

// Extend allows extending environment blocks with
// a global one. For example:
//
//	diff {
//	  skip {
//	    drop_schema = true
//	  }
//	}
//
//	env "local" {
//	  ...
//	  diff {
//	    concurrent_index {
//	      create = true
//	      drop = true
//	    }
//	  }
//	}
func (d *Diff) Extend(global *Diff) *Diff {
	if d == nil {
		return global
	}
	if d.SkipChanges == nil {
		d.SkipChanges = global.SkipChanges
	}
	return d
}

// Options converts the diff policy into options.
func (d *Diff) Options() (opts []schema.DiffOption) {
	// Per-driver configuration.
	opts = append(opts, func(opts *schema.DiffOptions) {
		opts.Extra = d.DefaultExtension
	})
	if d.SkipChanges == nil {
		return
	}
	var (
		changes schema.Changes
		rv      = reflect.ValueOf(d.SkipChanges).Elem()
	)
	for _, c := range []schema.Change{
		&schema.AddSchema{}, &schema.DropSchema{}, &schema.ModifySchema{},
		&schema.AddTable{}, &schema.DropTable{}, &schema.ModifyTable{},
		&schema.AddColumn{}, &schema.DropColumn{}, &schema.ModifyColumn{},
		&schema.AddIndex{}, &schema.DropIndex{}, &schema.ModifyIndex{},
		&schema.AddForeignKey{}, &schema.DropForeignKey{}, &schema.ModifyForeignKey{},
	} {
		if rt := reflect.TypeOf(c).Elem(); rv.FieldByName(rt.Name()).Bool() {
			changes = append(changes, c)
		}
	}
	if len(changes) > 0 {
		opts = append(opts, schema.DiffSkipChanges(changes...))
	}
	return opts
}

// DiffOptions returns the diff options configured for the environment,
// or nil if no environment or diff policy were set.
func (e *Env) DiffOptions() []schema.DiffOption {
	if e == nil || e.Diff == nil {
		return nil
	}
	return e.Diff.Options()
}

// Sources returns the paths containing the Atlas schema.
func (e *Env) Sources() ([]string, error) {
	attr, exists := e.Attr("src")
	if !exists {
		return nil, nil
	}
	switch attr.V.Type() {
	case cty.String:
		s, err := attr.String()
		if err != nil {
			return nil, err
		}
		return []string{s}, nil
	case cty.List(cty.String):
		return attr.Strings()
	default:
		return nil, fmt.Errorf("expected src to be either a string or strings, got: %s", attr.V.Type().FriendlyName())
	}
}

// asMap returns the extra attributes stored in the Env as a map[string]string.
func (e *Env) asMap() (map[string]string, error) {
	m := make(map[string]string, len(e.Extra.Attrs))
	for _, attr := range e.Extra.Attrs {
		if attr.K == "src" {
			continue
		}
		if v, err := attr.String(); err == nil {
			m[attr.K] = v
			continue
		}
		return nil, fmt.Errorf("expecting attr %q to be a literal, got: %T", attr.K, attr.V)
	}
	return m, nil
}

// LoadEnv reads the project file in path, and loads
// the environment instances with the provided name.
func LoadEnv(name string, opts ...LoadOption) ([]*Env, error) {
	cfg := &loadConfig{}
	for _, f := range opts {
		f(cfg)
	}
	u, err := url.Parse(GlobalFlags.ConfigURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "file" {
		return nil, fmt.Errorf("unsupported project file driver %q", u.Scheme)
	}
	path := filepath.Join(u.Host, u.Path)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			err = fmt.Errorf("project file %q was not found: %w", path, err)
		}
		return nil, err
	}
	project, err := parseConfig(path, name, cfg.inputValues)
	if err != nil {
		return nil, err
	}
	if err := project.Lint.remainedLog(); err != nil {
		return nil, err
	}
	envs := make(map[string][]*Env)
	for _, e := range project.Envs {
		if e.Name == "" {
			return nil, fmt.Errorf("all envs must have names on file %q", path)
		}
		if _, err := e.Sources(); err != nil {
			return nil, err
		}
		if e.Migration == nil {
			e.Migration = &Migration{}
		}
		if err := e.remainedLog(); err != nil {
			return nil, err
		}
		e.Diff = e.Diff.Extend(project.Diff)
		e.Lint = e.Lint.Extend(project.Lint)
		if err := e.Lint.remainedLog(); err != nil {
			return nil, err
		}
		envs[e.Name] = append(envs[e.Name], e)
	}
	selected, ok := envs[name]
	if !ok {
		return nil, fmt.Errorf("env %q not defined in project file", name)
	}
	return selected, nil
}

func parseConfig(path, env string, values map[string]cty.Value) (*Project, error) {
	cfg := &cmdext.AtlasConfig{}
	opts := append(
		cmdext.DataSources,
		cfg.InitBlock(),
		schemahcl.WithScopedEnums(
			"env.migration.format",
			formatAtlas, formatFlyway,
			formatLiquibase, formatGoose,
			formatGolangMigrate,
		),
		schemahcl.WithVariables(map[string]cty.Value{
			"atlas": cty.ObjectVal(map[string]cty.Value{
				"env": cty.StringVal(env),
			}),
		}),
	)
	p := &Project{Lint: &Lint{}, Diff: &Diff{}}
	if err := schemahcl.New(opts...).EvalFiles([]string{path}, p, values); err != nil {
		return nil, err
	}
	for _, e := range p.Envs {
		e.cfg = cfg
	}
	return p, nil
}

func init() {
	schemahcl.Register("env", &Env{})
}
