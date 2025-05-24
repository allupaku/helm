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
	"strings"

	"sigs.k8s.io/yaml"

	chartloader "helm.sh/helm/v4/pkg/chart/v2/loader"
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
	releaseName := "test-view-toml-encoding"
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

	chartRequested, err := chartloader.Load(chartPath)
	if err != nil {
		t.Fatalf("Failed to load chart %s: %v", chartPath, err)
	}

	rel, err := install.Run(chartRequested, nil) // Pass loaded chart and nil for values.
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

	// Assertions: Check the string content of the TOML data
	// Note: The BurntSushi/toml encoder sorts map keys alphabetically.
	expectedTomlSnippets := []string{
		"integerValue = 42\n",
		"realFloatValue = 3.14159\n",
		"wholeFloatValue = 58\n", // Should be encoded as integer
		"[nestedValues]\n",
		"  deepInt = 100\n",
		"  deepRealFloat = 2.718\n",
		"  deepWholeFloat = 200\n", // Should be encoded as integer
		// For arrays with maps, the exact order within the map literal might vary based on map iteration order in Go before encoding.
		// However, BurntSushi/toml sorts keys within tables it creates. For inline maps in arrays, it's trickier.
		// A more robust check for array elements with maps might involve decoding that specific part if necessary,
		// or checking for the presence of key-value pairs within the array structure string representation.
		// For simplicity, we'll check for easily verifiable parts.
		// Example: items = [1, 2, 3.5, {name = "itemInt", value = 7}, ... ]
		// We expect value = 7 (integer), value = 8 (integer), value = 9.9 (float)
	}

	for _, snippet := range expectedTomlSnippets {
		if !strings.Contains(configMapData, snippet) {
			t.Errorf("Expected TOML data to contain snippet:\n%s\n\nGot TOML data:\n%s", snippet, configMapData)
		}
	}

	// More specific checks for array items if direct string matching is too fragile due to map key ordering in arrays.
	// For items like `{name = "itemInt", value = 7}` (value should be int)
	// and `{name = "itemWholeFloat", value = 8}` (value should be int)
	// and `{name = "itemRealFloat", value = 9.9}` (value should be float)

	// A simple check for this known structure:
	// arrayValues = [1, 2, 3.5, {name = "itemInt", value = 7}, {name = "itemRealFloat", value = 9.9}, {name = "itemWholeFloat", value = 8}]
	// Note: The order of maps within the array depends on the original order in values.yaml
	// The order of keys within each map literal is sorted by the TOML encoder.
	// values.yaml has: itemInt, itemWholeFloat, itemRealFloat
	// Expected encoded order of map keys: name, value
	// Expected order of items in array:
	// 1
	// 2.0 -> 2
	// 3.5
	// {name = "itemInt", value = 7}
	// {name = "itemWholeFloat", value = 8}
	// {name = "itemRealFloat", value = 9.9}

	// TOML output for arrayValues based on values.yaml and sorting:
	// arrayValues = [1, 2, 3.5, {name = "itemInt", value = 7}, {name = "itemWholeFloat", value = 8}, {name = "itemRealFloat", value = 9.9}]
	// The values.yaml order is:
	// - 1
	// - 2.0
	// - 3.5
	// - name: itemInt, value: 7
	// - name: itemWholeFloat, value: 8.0
	// - name: itemRealFloat, value: 9.9

	expectedArrayString := `arrayValues = [1, 2, 3.5, {name = "itemInt", value = 7}, {name = "itemWholeFloat", value = 8}, {name = "itemRealFloat", value = 9.9}]`
	if !strings.Contains(configMapData, expectedArrayString) {
		t.Errorf("Expected TOML data to contain exact array string:\n%s\n\nGot TOML data:\n%s", expectedArrayString, configMapData)
	}

}
