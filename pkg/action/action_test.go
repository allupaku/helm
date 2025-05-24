/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package action

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	fakeclientset "k8s.io/client-go/kubernetes/fake"

	"helm.sh/helm/v4/internal/logging"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	chartutil "helm.sh/helm/v4/pkg/chart/v2/util"
	"helm.sh/helm/v4/pkg/kube"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	"helm.sh/helm/v4/pkg/registry"
	release "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"
	"helm.sh/helm/v4/pkg/time"
)

var verbose = flag.Bool("test.log", false, "enable test logging (debug by default)")

func actionConfigFixture(t *testing.T) *Configuration {
	t.Helper()
	return actionConfigFixtureWithDummyResources(t, nil)
}

func actionConfigFixtureWithDummyResources(t *testing.T, dummyResources kube.ResourceList) *Configuration {
	t.Helper()

	logger := logging.NewLogger(func() bool {
		return *verbose
	})
	slog.SetDefault(logger)

	registryClient, err := registry.NewClient()
	if err != nil {
		t.Fatal(err)
	}

	return &Configuration{
		Releases:       storage.Init(driver.NewMemory()),
		KubeClient:     &kubefake.FailingKubeClient{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}, DummyResources: dummyResources},
		Capabilities:   chartutil.DefaultCapabilities,
		RegistryClient: registryClient,
	}
}

var manifestWithHook = `kind: ConfigMap
metadata:
  name: test-cm
  annotations:
    "helm.sh/hook": post-install,pre-delete,post-upgrade
data:
  name: value`

var manifestWithTestHook = `kind: Pod
  metadata:
	name: finding-nemo,
	annotations:
	  "helm.sh/hook": test
  spec:
	containers:
	- name: nemo-test
	  image: fake-image
	  cmd: fake-command
  `

var rbacManifests = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: schedule-agents
rules:
- apiGroups: [""]
  resources: ["pods", "pods/exec", "pods/log"]
  verbs: ["*"]

---

apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: schedule-agents
  namespace: {{ default .Release.Namespace}}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: schedule-agents
subjects:
- kind: ServiceAccount
  name: schedule-agents
  namespace: {{ .Release.Namespace }}
`

type chartOptions struct {
	*chart.Chart
}

type chartOption func(*chartOptions)

func buildChart(opts ...chartOption) *chart.Chart {
	defaultTemplates := []*chart.File{
		{Name: "templates/hello", Data: []byte("hello: world")},
		{Name: "templates/hooks", Data: []byte(manifestWithHook)},
	}
	return buildChartWithTemplates(defaultTemplates, opts...)
}

func buildChartWithTemplates(templates []*chart.File, opts ...chartOption) *chart.Chart {
	c := &chartOptions{
		Chart: &chart.Chart{
			// TODO: This should be more complete.
			Metadata: &chart.Metadata{
				APIVersion: "v1",
				Name:       "hello",
				Version:    "0.1.0",
			},
			Templates: templates,
		},
	}

	for _, opt := range opts {
		opt(c)
	}
	return c.Chart
}

func withName(name string) chartOption {
	return func(opts *chartOptions) {
		opts.Metadata.Name = name
	}
}

func withSampleValues() chartOption {
	values := map[string]interface{}{
		"someKey": "someValue",
		"nestedKey": map[string]interface{}{
			"simpleKey": "simpleValue",
			"anotherNestedKey": map[string]interface{}{
				"yetAnotherNestedKey": map[string]interface{}{
					"youReadyForAnotherNestedKey": "No",
				},
			},
		},
	}
	return func(opts *chartOptions) {
		opts.Values = values
	}
}

func withValues(values map[string]interface{}) chartOption {
	return func(opts *chartOptions) {
		opts.Values = values
	}
}

func withNotes(notes string) chartOption {
	return func(opts *chartOptions) {
		opts.Templates = append(opts.Templates, &chart.File{
			Name: "templates/NOTES.txt",
			Data: []byte(notes),
		})
	}
}

func withDependency(dependencyOpts ...chartOption) chartOption {
	return func(opts *chartOptions) {
		opts.AddDependency(buildChart(dependencyOpts...))
	}
}

func withMetadataDependency(dependency chart.Dependency) chartOption {
	return func(opts *chartOptions) {
		opts.Metadata.Dependencies = append(opts.Metadata.Dependencies, &dependency)
	}
}

func withSampleTemplates() chartOption {
	return func(opts *chartOptions) {
		sampleTemplates := []*chart.File{
			// This adds basic templates and partials.
			{Name: "templates/goodbye", Data: []byte("goodbye: world")},
			{Name: "templates/empty", Data: []byte("")},
			{Name: "templates/with-partials", Data: []byte(`hello: {{ template "_planet" . }}`)},
			{Name: "templates/partials/_planet", Data: []byte(`{{define "_planet"}}Earth{{end}}`)},
		}
		opts.Templates = append(opts.Templates, sampleTemplates...)
	}
}

func withSampleSecret() chartOption {
	return func(opts *chartOptions) {
		sampleSecret := &chart.File{Name: "templates/secret.yaml", Data: []byte("apiVersion: v1\nkind: Secret\n")}
		opts.Templates = append(opts.Templates, sampleSecret)
	}
}

func withSampleIncludingIncorrectTemplates() chartOption {
	return func(opts *chartOptions) {
		sampleTemplates := []*chart.File{
			// This adds basic templates and partials.
			{Name: "templates/goodbye", Data: []byte("goodbye: world")},
			{Name: "templates/empty", Data: []byte("")},
			{Name: "templates/incorrect", Data: []byte("{{ .Values.bad.doh }}")},
			{Name: "templates/with-partials", Data: []byte(`hello: {{ template "_planet" . }}`)},
			{Name: "templates/partials/_planet", Data: []byte(`{{define "_planet"}}Earth{{end}}`)},
		}
		opts.Templates = append(opts.Templates, sampleTemplates...)
	}
}

func withMultipleManifestTemplate() chartOption {
	return func(opts *chartOptions) {
		sampleTemplates := []*chart.File{
			{Name: "templates/rbac", Data: []byte(rbacManifests)},
		}
		opts.Templates = append(opts.Templates, sampleTemplates...)
	}
}

func withKube(version string) chartOption {
	return func(opts *chartOptions) {
		opts.Metadata.KubeVersion = version
	}
}

// releaseStub creates a release stub, complete with the chartStub as its chart.
func releaseStub() *release.Release {
	return namedReleaseStub("angry-panda", release.StatusDeployed)
}

func namedReleaseStub(name string, status release.Status) *release.Release {
	now := time.Now()
	return &release.Release{
		Name: name,
		Info: &release.Info{
			FirstDeployed: now,
			LastDeployed:  now,
			Status:        status,
			Description:   "Named Release Stub",
		},
		Chart:   buildChart(withSampleTemplates()),
		Config:  map[string]interface{}{"name": "value"},
		Version: 1,
		Hooks: []*release.Hook{
			{
				Name:     "test-cm",
				Kind:     "ConfigMap",
				Path:     "test-cm",
				Manifest: manifestWithHook,
				Events: []release.HookEvent{
					release.HookPostInstall,
					release.HookPreDelete,
				},
			},
			{
				Name:     "finding-nemo",
				Kind:     "Pod",
				Path:     "finding-nemo",
				Manifest: manifestWithTestHook,
				Events: []release.HookEvent{
					release.HookTest,
				},
			},
		},
	}
}

func TestConfiguration_Init(t *testing.T) {
	tests := []struct {
		name               string
		helmDriver         string
		expectedDriverType interface{}
		expectErr          bool
		errMsg             string
	}{
		{
			name:               "Test secret driver",
			helmDriver:         "secret",
			expectedDriverType: &driver.Secrets{},
		},
		{
			name:               "Test secrets driver",
			helmDriver:         "secrets",
			expectedDriverType: &driver.Secrets{},
		},
		{
			name:               "Test empty driver",
			helmDriver:         "",
			expectedDriverType: &driver.Secrets{},
		},
		{
			name:               "Test configmap driver",
			helmDriver:         "configmap",
			expectedDriverType: &driver.ConfigMaps{},
		},
		{
			name:               "Test configmaps driver",
			helmDriver:         "configmaps",
			expectedDriverType: &driver.ConfigMaps{},
		},
		{
			name:               "Test memory driver",
			helmDriver:         "memory",
			expectedDriverType: &driver.Memory{},
		},
		{
			name:       "Test sql driver",
			helmDriver: "sql",
			expectErr:  true,
			errMsg:     "unable to instantiate SQL driver",
		},
		{
			name:       "Test unknown driver",
			helmDriver: "someDriver",
			expectErr:  true,
			errMsg:     fmt.Sprintf("unknown driver %q", "someDriver"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Configuration{}

			actualErr := cfg.Init(nil, "default", tt.helmDriver)
			if tt.expectErr {
				assert.Error(t, actualErr)
				assert.Contains(t, actualErr.Error(), tt.errMsg)
			} else {
				assert.NoError(t, actualErr)
				assert.IsType(t, tt.expectedDriverType, cfg.Releases.Driver)
			}
		})
	}
}

func TestGetVersionSet(t *testing.T) {
	client := fakeclientset.NewClientset()

	vs, err := GetVersionSet(client.Discovery())
	if err != nil {
		t.Error(err)
	}

	if !vs.Has("v1") {
		t.Errorf("Expected supported versions to at least include v1.")
	}
	if vs.Has("nosuchversion/v1") {
		t.Error("Non-existent version is reported found.")
	}
}

func TestInstallAction_ToTomlEncoding(t *testing.T) {
	releaseName := "tভিউ-toml-encoding"
	chartPath := "testdata/charts/toTomlTestChart"

	cfg := actionConfigFixture(t)
	// For ClientOnly, KubeClient should be a specific type that allows rendering without a cluster.
	// Using a simple fake client here as the actual interaction is DryRun.
	// If a more specific fake is needed (e.g. for discovery), this might need adjustment.
	cfg.KubeClient = &kubefake.PrintingKubeClient{Out: io.Discard}

	install := NewInstall(cfg)
	install.ReleaseName = releaseName
	install.DryRun = true
	install.ClientOnly = true // Important for rendering without cluster interaction

	// Load chart values.
	// Note: install.Run will load values from the chart's values.yaml by default.
	// If we wanted to override with a specific values file, we'd load it here.
	// For this test, the chart's internal values.yaml is sufficient.

	rel, err := install.Run(chartPath, nil) // Pass nil for values to use the chart's default values.yaml
	if err != nil {
		t.Fatalf("Install.Run() failed: %v", err)
	}

	if rel == nil {
		t.Fatal("Install.Run() returned a nil release")
	}
	if rel.Manifest == "" {
		t.Fatal("Install.Run() returned a release with an empty manifest")
	}

	// Find the ConfigMap and extract TOML data
	var configMapData string
	// Manifests are separated by "---". Split them and parse.
	manifests := strings.Split(rel.Manifest, "---")
	foundCM := false
	for _, manifest := range manifests {
		if strings.TrimSpace(manifest) == "" {
			continue
		}
		var obj map[string]interface{}
		if err := yaml.Unmarshal([]byte(manifest), &obj); err != nil {
			t.Fatalf("Failed to unmarshal manifest part: %v\nManifest part:\n%s", err, manifest)
		}

		kind, _ := obj["kind"].(string)
		if kind != "ConfigMap" {
			continue
		}

		metadata, ok := obj["metadata"].(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := metadata["name"].(string)

		if name == releaseName+"-toml-test-config" {
			data, ok := obj["data"].(map[string]interface{})
			if !ok {
				t.Fatalf("ConfigMap %s has no data field or it's not a map", name)
			}
			tomlString, ok := data["myConfig.toml"].(string)
			if !ok {
				t.Fatalf("ConfigMap %s data has no myConfig.toml field or it's not a string", name)
			}
			configMapData = tomlString
			foundCM = true
			break
		}
	}

	if !foundCM {
		t.Fatalf("ConfigMap %s-toml-test-config not found in rendered manifests", releaseName)
	}

	var decodedToml map[string]interface{}
	if _, err := toml.Decode(configMapData, &decodedToml); err != nil {
		t.Fatalf("Failed to decode TOML data: %v\nTOML data:\n%s", err, configMapData)
	}

	// Assertions
	assertTomlType(t, decodedToml, "integerValue", int64(42), "int64")
	assertTomlType(t, decodedToml, "wholeFloatValue", int64(58), "int64")
	assertTomlType(t, decodedToml, "realFloatValue", float64(3.14159), "float64")

	nested, ok := decodedToml["nestedValues"].(map[string]interface{})
	if !ok {
		t.Fatalf("nestedValues is not a map[string]interface{}")
	}
	assertTomlType(t, nested, "deepInt", int64(100), "int64")
	assertTomlType(t, nested, "deepWholeFloat", int64(200), "int64")
	assertTomlType(t, nested, "deepRealFloat", float64(2.718), "float64")

	arrayValues, ok := decodedToml["arrayValues"].([]interface{})
	if !ok {
		t.Fatalf("arrayValues is not a []interface{}")
	}
	if len(arrayValues) != 6 {
		t.Fatalf("Expected 6 elements in arrayValues, got %d", len(arrayValues))
	}

	assertTomlType(t, arrayValues, 0, int64(1), "int64")
	assertTomlType(t, arrayValues, 1, int64(2), "int64")
	assertTomlType(t, arrayValues, 2, float64(3.5), "float64")

	itemIntMap, ok := arrayValues[3].(map[string]interface{})
	if !ok {
		t.Fatalf("arrayValues[3] is not a map")
	}
	assertTomlType(t, itemIntMap, "value", int64(7), "int64")

	itemWholeFloatMap, ok := arrayValues[4].(map[string]interface{})
	if !ok {
		t.Fatalf("arrayValues[4] is not a map")
	}
	assertTomlType(t, itemWholeFloatMap, "value", int64(8), "int64")

	itemRealFloatMap, ok := arrayValues[5].(map[string]interface{})
	if !ok {
		t.Fatalf("arrayValues[5] is not a map")
	}
	assertTomlType(t, itemRealFloatMap, "value", float64(9.9), "float64")
}

// assertTomlType is a helper to check type and value of a key in a map or an index in a slice.
func assertTomlType(t *testing.T, data interface{}, keyOrIndex interface{}, expectedValue interface{}, expectedType string) {
	t.Helper()
	var value interface{}
	var found bool

	switch d := data.(type) {
	case map[string]interface{}:
		key, ok := keyOrIndex.(string)
		if !ok {
			t.Fatalf("keyOrIndex must be string for map, got %T", keyOrIndex)
		}
		value, found = d[key]
		if !found {
			t.Errorf("Key %q not found in TOML data", key)
			return
		}
	case []interface{}:
		index, ok := keyOrIndex.(int)
		if !ok {
			t.Fatalf("keyOrIndex must be int for slice, got %T", keyOrIndex)
		}
		if index < 0 || index >= len(d) {
			t.Errorf("Index %d out of bounds for TOML array (len %d)", index, len(d))
			return
		}
		value = d[index]
	default:
		t.Fatalf("Unsupported data type for assertion: %T", data)
		return
	}

	switch expectedType {
	case "int64":
		v, ok := value.(int64)
		if !ok {
			t.Errorf("Expected key/index '%v' to be int64, got %T (value: %v)", keyOrIndex, value, value)
			return
		}
		if expected, ok := expectedValue.(int64); ok && v != expected {
			t.Errorf("Expected key/index '%v' to have value %d, got %d", keyOrIndex, expected, v)
		}
	case "float64":
		v, ok := value.(float64)
		if !ok {
			t.Errorf("Expected key/index '%v' to be float64, got %T (value: %v)", keyOrIndex, value, value)
			return
		}
		if expected, ok := expectedValue.(float64); ok && v != expected {
			// Comparing floats for exact equality can be tricky due to precision.
			// For this test, direct comparison should be fine as values are hardcoded.
			t.Errorf("Expected key/index '%v' to have value %f, got %f", keyOrIndex, expected, v)
		}
	default:
		t.Errorf("Unsupported expectedType for assertion: %s", expectedType)
	}
}
